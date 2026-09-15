package service

import (
	"container/heap"
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	openAIAccountScheduleLayerPreviousResponse = "previous_response_id"
	openAIAccountScheduleLayerSessionSticky    = "session_hash"
	openAIAccountScheduleLayerLoadBalance      = "load_balance"
	openAIAdvancedSchedulerSettingKey          = "openai_advanced_scheduler_enabled"
)

const (
	openAIAdvancedSchedulerSettingCacheTTL  = 5 * time.Second
	openAIAdvancedSchedulerSettingDBTimeout = 2 * time.Second
)

const (
	openAIStickyTTFTMinSamples              int64 = 8
	openAIStickyTTFTAlternativeMinSamples   int64 = 5
	openAIStickyTTFTThresholdMs                   = 2500.0
	openAIStickyTTFTMinImprovementMs              = 750.0
	openAIStickyTTFTMinImprovementRatio           = 1.25
	openAIStickyTTFTUnknownAlternativeMs          = 10000.0
	openAILegacyTTFTTieBreakMinDifferenceMs       = 250.0
)

type cachedOpenAIAdvancedSchedulerSetting struct {
	enabled   bool
	expiresAt int64
}

var openAIAdvancedSchedulerSettingCache atomic.Value // *cachedOpenAIAdvancedSchedulerSetting
var openAIAdvancedSchedulerSettingSF singleflight.Group

type OpenAIAccountScheduleRequest struct {
	GroupID                 *int64
	SessionHash             string
	StickyAccountID         int64
	PreviousResponseID      string
	RequestedModel          string
	RequiredTransport       OpenAIUpstreamTransport
	RequiredImageCapability OpenAIImagesCapability
	RequireCompact          bool
	ExcludedIDs             map[int64]struct{}
}

type OpenAIAccountScheduleDecision struct {
	Layer               string
	StickyPreviousHit   bool
	StickySessionHit    bool
	StickyTTFTBypass    bool
	CandidateCount      int
	TopK                int
	LatencyMs           int64
	LoadSkew            float64
	SelectedAccountID   int64
	SelectedAccountType string
	Candidates          []OpenAIAccountScheduleCandidateDiagnostic
}

type OpenAIAccountScheduleCandidateDiagnostic struct {
	AccountID          int64   `json:"account_id"`
	AccountName        string  `json:"account_name,omitempty"`
	AccountType        string  `json:"account_type,omitempty"`
	Priority           int     `json:"priority"`
	Concurrency        int     `json:"concurrency"`
	LoadFactor         int     `json:"load_factor"`
	CurrentConcurrency int     `json:"current_concurrency"`
	WaitingCount       int     `json:"waiting_count"`
	LoadRate           int     `json:"load_rate"`
	Score              float64 `json:"score,omitempty"`
	ErrorRate          float64 `json:"error_rate,omitempty"`
	TTFTMs             int     `json:"ttft_ms,omitempty"`
	HasTTFT            bool    `json:"has_ttft,omitempty"`
	Rank               int     `json:"rank,omitempty"`
	Order              int     `json:"order,omitempty"`
	InTopK             bool    `json:"in_top_k,omitempty"`
	Selected           bool    `json:"selected,omitempty"`
	LastUsedAt         string  `json:"last_used_at,omitempty"`
}

type openAILegacyScheduleDecisionContextKey struct{}

func withOpenAILegacyScheduleDecision(ctx context.Context, decision *OpenAIAccountScheduleDecision) context.Context {
	if ctx == nil || decision == nil {
		return ctx
	}
	return context.WithValue(ctx, openAILegacyScheduleDecisionContextKey{}, decision)
}

func openAILegacyScheduleDecisionFromContext(ctx context.Context) *OpenAIAccountScheduleDecision {
	if ctx == nil {
		return nil
	}
	decision, _ := ctx.Value(openAILegacyScheduleDecisionContextKey{}).(*OpenAIAccountScheduleDecision)
	return decision
}

type OpenAIAccountSchedulerMetricsSnapshot struct {
	SelectTotal              int64
	StickyPreviousHitTotal   int64
	StickySessionHitTotal    int64
	LoadBalanceSelectTotal   int64
	AccountSwitchTotal       int64
	SchedulerLatencyMsTotal  int64
	SchedulerLatencyMsAvg    float64
	StickyHitRatio           float64
	AccountSwitchRate        float64
	LoadSkewAvg              float64
	RuntimeStatsAccountCount int
}

type OpenAIAccountScheduler interface {
	Select(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error)
	ReportResult(accountID int64, success bool, firstTokenMs *int)
	ReportSwitch()
	SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot
}

type openAIAccountSchedulerMetrics struct {
	selectTotal            atomic.Int64
	stickyPreviousHitTotal atomic.Int64
	stickySessionHitTotal  atomic.Int64
	loadBalanceSelectTotal atomic.Int64
	accountSwitchTotal     atomic.Int64
	latencyMsTotal         atomic.Int64
	loadSkewMilliTotal     atomic.Int64
}

type openAIAccountLoadPlan struct {
	allCandidates             []openAIAccountCandidateScore
	candidates                []openAIAccountCandidateScore
	staleSnapshotCompactRetry []openAIAccountCandidateScore
	selectionOrder            []openAIAccountCandidateScore
	candidateCount            int
	topK                      int
	loadSkew                  float64
}

func (m *openAIAccountSchedulerMetrics) recordSelect(decision OpenAIAccountScheduleDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

func (m *openAIAccountSchedulerMetrics) recordSwitch() {
	if m == nil {
		return
	}
	m.accountSwitchTotal.Add(1)
}

type openAIAccountRuntimeStats struct {
	accounts     sync.Map
	models       sync.Map
	accountCount atomic.Int64
}

type openAIAccountModelRuntimeKey struct {
	accountID int64
	model     string
}

type openAIAccountRuntimeStat struct {
	errorRateEWMABits atomic.Uint64
	ttftEWMABits      atomic.Uint64
	ttftSampleCount   atomic.Int64
}

func newOpenAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	return &openAIAccountRuntimeStats{}
}

func (s *openAIAccountRuntimeStats) loadOrCreate(accountID int64) *openAIAccountRuntimeStat {
	if value, ok := s.accounts.Load(accountID); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &openAIAccountRuntimeStat{}
	stat.ttftEWMABits.Store(math.Float64bits(math.NaN()))
	actual, loaded := s.accounts.LoadOrStore(accountID, stat)
	if !loaded {
		s.accountCount.Add(1)
		return stat
	}
	existing, _ := actual.(*openAIAccountRuntimeStat)
	if existing != nil {
		return existing
	}
	return stat
}

func normalizeOpenAIAccountRuntimeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(NormalizeOpenAICompatRequestedModel(model)))
}

func (s *openAIAccountRuntimeStats) loadOrCreateModel(accountID int64, model string) *openAIAccountRuntimeStat {
	model = normalizeOpenAIAccountRuntimeModel(model)
	if s == nil || accountID <= 0 || model == "" {
		return nil
	}
	key := openAIAccountModelRuntimeKey{accountID: accountID, model: model}
	if value, ok := s.models.Load(key); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &openAIAccountRuntimeStat{}
	stat.ttftEWMABits.Store(math.Float64bits(math.NaN()))
	actual, _ := s.models.LoadOrStore(key, stat)
	existing, _ := actual.(*openAIAccountRuntimeStat)
	return existing
}

func updateEWMAAtomic(target *atomic.Uint64, sample float64, alpha float64) {
	for {
		oldBits := target.Load()
		oldValue := math.Float64frombits(oldBits)
		newValue := alpha*sample + (1-alpha)*oldValue
		if target.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

func (s *openAIAccountRuntimeStats) report(accountID int64, success bool, firstTokenMs *int) {
	s.reportForModel(accountID, "", success, firstTokenMs)
}

func updateOpenAIAccountTTFT(stat *openAIAccountRuntimeStat, firstTokenMs *int, alpha float64) {
	if stat == nil || firstTokenMs == nil || *firstTokenMs <= 0 {
		return
	}
	stat.ttftSampleCount.Add(1)
	ttft := float64(*firstTokenMs)
	ttftBits := math.Float64bits(ttft)
	for {
		oldBits := stat.ttftEWMABits.Load()
		oldValue := math.Float64frombits(oldBits)
		if math.IsNaN(oldValue) {
			if stat.ttftEWMABits.CompareAndSwap(oldBits, ttftBits) {
				return
			}
			continue
		}
		newValue := alpha*ttft + (1-alpha)*oldValue
		if stat.ttftEWMABits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

func (s *openAIAccountRuntimeStats) reportForModel(accountID int64, model string, success bool, firstTokenMs *int) {
	if s == nil || accountID <= 0 {
		return
	}
	const alpha = 0.2
	stat := s.loadOrCreate(accountID)

	errorSample := 1.0
	if success {
		errorSample = 0.0
	}
	updateEWMAAtomic(&stat.errorRateEWMABits, errorSample, alpha)
	updateOpenAIAccountTTFT(stat, firstTokenMs, alpha)
	modelStat := s.loadOrCreateModel(accountID, model)
	if modelStat != nil {
		updateEWMAAtomic(&modelStat.errorRateEWMABits, errorSample, alpha)
		updateOpenAIAccountTTFT(modelStat, firstTokenMs, alpha)
	}
}

func (s *openAIAccountRuntimeStats) ttftSnapshot(accountID int64) (ttft float64, sampleCount int64, ok bool) {
	if s == nil || accountID <= 0 {
		return 0, 0, false
	}
	value, found := s.accounts.Load(accountID)
	if !found {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	ttft = math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttft) {
		return 0, stat.ttftSampleCount.Load(), false
	}
	return ttft, stat.ttftSampleCount.Load(), true
}

func (s *openAIAccountRuntimeStats) ttftSnapshotForModel(accountID int64, model string) (ttft float64, sampleCount int64, ok bool) {
	model = normalizeOpenAIAccountRuntimeModel(model)
	if s == nil || accountID <= 0 || model == "" {
		return s.ttftSnapshot(accountID)
	}
	value, found := s.models.Load(openAIAccountModelRuntimeKey{accountID: accountID, model: model})
	if !found {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	ttft = math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttft) {
		return 0, stat.ttftSampleCount.Load(), false
	}
	return ttft, stat.ttftSampleCount.Load(), true
}

func (s *openAIAccountRuntimeStats) snapshot(accountID int64) (errorRate float64, ttft float64, hasTTFT bool) {
	if s == nil || accountID <= 0 {
		return 0, 0, false
	}
	value, ok := s.accounts.Load(accountID)
	if !ok {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	errorRate = clamp01(math.Float64frombits(stat.errorRateEWMABits.Load()))
	ttftValue := math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttftValue) {
		return errorRate, 0, false
	}
	return errorRate, ttftValue, true
}

func (s *openAIAccountRuntimeStats) snapshotForModel(accountID int64, model string) (errorRate float64, ttft float64, hasTTFT bool) {
	model = normalizeOpenAIAccountRuntimeModel(model)
	if s != nil && accountID > 0 && model != "" {
		if value, ok := s.models.Load(openAIAccountModelRuntimeKey{accountID: accountID, model: model}); ok {
			if stat, _ := value.(*openAIAccountRuntimeStat); stat != nil {
				errorRate = clamp01(math.Float64frombits(stat.errorRateEWMABits.Load()))
				ttftValue := math.Float64frombits(stat.ttftEWMABits.Load())
				if !math.IsNaN(ttftValue) {
					return errorRate, ttftValue, true
				}
				return errorRate, 0, false
			}
		}
	}
	return s.snapshot(accountID)
}

func (s *openAIAccountRuntimeStats) size() int {
	if s == nil {
		return 0
	}
	return int(s.accountCount.Load())
}

func shouldBypassOpenAIStickyTTFT(
	stickyTTFT float64,
	stickySamples int64,
	alternativeTTFT float64,
	alternativeSamples int64,
	hasUnknownAlternative bool,
) bool {
	if stickySamples < openAIStickyTTFTMinSamples || stickyTTFT < openAIStickyTTFTThresholdMs {
		return false
	}
	if alternativeSamples >= openAIStickyTTFTAlternativeMinSamples && alternativeTTFT > 0 {
		improvement := stickyTTFT - alternativeTTFT
		if improvement >= openAIStickyTTFTMinImprovementMs &&
			stickyTTFT >= alternativeTTFT*openAIStickyTTFTMinImprovementRatio {
			return true
		}
	}
	return hasUnknownAlternative && stickyTTFT >= openAIStickyTTFTUnknownAlternativeMs
}

func (s *OpenAIGatewayService) getOpenAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	if s == nil {
		return nil
	}
	s.openaiAccountStatsOnce.Do(func() {
		if s.openaiAccountStats == nil {
			s.openaiAccountStats = newOpenAIAccountRuntimeStats()
		}
	})
	return s.openaiAccountStats
}

func (s *OpenAIGatewayService) shouldEvaluateOpenAIStickyTTFT(accountID int64, requestedModel string) bool {
	stats := s.getOpenAIAccountRuntimeStats()
	ttft, samples, ok := stats.ttftSnapshotForModel(accountID, requestedModel)
	return ok && samples >= openAIStickyTTFTMinSamples && ttft >= openAIStickyTTFTThresholdMs
}

func (s *OpenAIGatewayService) shouldBypassOpenAIStickyAccount(
	stickyAccountID int64,
	requestedModel string,
	accounts []Account,
	isEligible func(*Account) bool,
) bool {
	stats := s.getOpenAIAccountRuntimeStats()
	stickyTTFT, stickySamples, ok := stats.ttftSnapshotForModel(stickyAccountID, requestedModel)
	if !ok || stickySamples < openAIStickyTTFTMinSamples || stickyTTFT < openAIStickyTTFTThresholdMs {
		return false
	}

	bestAlternativeTTFT := math.MaxFloat64
	var bestAlternativeSamples int64
	hasUnknownAlternative := false
	for i := range accounts {
		account := &accounts[i]
		if account.ID == stickyAccountID || (isEligible != nil && !isEligible(account)) {
			continue
		}
		ttft, samples, hasTTFT := stats.ttftSnapshotForModel(account.ID, requestedModel)
		if !hasTTFT || samples < openAIStickyTTFTAlternativeMinSamples {
			hasUnknownAlternative = true
			continue
		}
		if ttft < bestAlternativeTTFT {
			bestAlternativeTTFT = ttft
			bestAlternativeSamples = samples
		}
	}

	return shouldBypassOpenAIStickyTTFT(
		stickyTTFT,
		stickySamples,
		bestAlternativeTTFT,
		bestAlternativeSamples,
		hasUnknownAlternative,
	)
}

type defaultOpenAIAccountScheduler struct {
	service *OpenAIGatewayService
	metrics openAIAccountSchedulerMetrics
	stats   *openAIAccountRuntimeStats
}

func newDefaultOpenAIAccountScheduler(service *OpenAIGatewayService, stats *openAIAccountRuntimeStats) OpenAIAccountScheduler {
	if stats == nil {
		stats = newOpenAIAccountRuntimeStats()
	}
	return &defaultOpenAIAccountScheduler{
		service: service,
		stats:   stats,
	}
}

func (s *defaultOpenAIAccountScheduler) Select(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	start := time.Now()
	defer func() {
		decision.LatencyMs = time.Since(start).Milliseconds()
		s.metrics.recordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	if previousResponseID != "" {
		selection, err := s.service.SelectAccountByPreviousResponseID(
			ctx,
			req.GroupID,
			previousResponseID,
			req.RequestedModel,
			req.ExcludedIDs,
			req.RequireCompact,
		)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			if !s.isAccountTransportCompatible(selection.Account, req.RequiredTransport) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				selection = nil
			}
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
			}
			return selection, decision, nil
		}
	}

	selection, bypassedStickyAccountID, err := s.selectBySessionHash(ctx, req)
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		return selection, decision, nil
	}
	if bypassedStickyAccountID > 0 {
		decision.StickyTTFTBypass = true
		req.ExcludedIDs = cloneExcludedAccountIDs(req.ExcludedIDs)
		if req.ExcludedIDs == nil {
			req.ExcludedIDs = make(map[int64]struct{}, 1)
		}
		req.ExcludedIDs[bypassedStickyAccountID] = struct{}{}
	}

	selection, candidateCount, topK, loadSkew, candidates, err := s.selectByLoadBalance(ctx, req)
	decision.Layer = openAIAccountScheduleLayerLoadBalance
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	decision.Candidates = candidates
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		markOpenAIScheduleCandidateSelected(decision.Candidates, selection.Account.ID)
		if bypassedStickyAccountID > 0 && req.SessionHash != "" {
			_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
		}
	}
	return selection, decision, nil
}

func (s *defaultOpenAIAccountScheduler) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int64, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, 0, nil
	}

	accountID := req.StickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.service.getStickySessionAccountID(ctx, req.GroupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil, 0, nil
		}
	}
	if accountID <= 0 {
		return nil, 0, nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[accountID]; excluded {
			return nil, 0, nil
		}
	}

	account, err := s.service.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, 0, nil
	}
	if shouldClearStickySession(account, req.RequestedModel) || !account.IsOpenAI() || !account.IsSchedulable() {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, 0, nil
	}
	if !s.isAccountRequestCompatible(ctx, account, req) {
		return nil, 0, nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, 0, nil
	}
	account = s.service.recheckSelectedOpenAIAccountFromDB(ctx, account, req.RequestedModel, req.RequireCompact)
	if account == nil || !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, 0, nil
	}

	if strings.TrimSpace(req.PreviousResponseID) == "" && s.service.shouldEvaluateOpenAIStickyTTFT(accountID, req.RequestedModel) {
		accounts, listErr := s.service.listSchedulableAccounts(ctx, req.GroupID)
		if listErr == nil && s.service.shouldBypassOpenAIStickyAccount(accountID, req.RequestedModel, accounts, func(candidate *Account) bool {
			if candidate == nil || !candidate.IsSchedulable() || !candidate.IsOpenAI() || s.service.isOpenAIAccountRuntimeBlocked(candidate) {
				return false
			}
			if req.RequireCompact && openAICompactSupportTier(candidate) == 0 {
				return false
			}
			if req.ExcludedIDs != nil {
				if _, excluded := req.ExcludedIDs[candidate.ID]; excluded {
					return false
				}
			}
			return s.isAccountRequestCompatible(ctx, candidate, req) &&
				s.isAccountTransportCompatible(candidate, req.RequiredTransport)
		}) {
			slog.Info("bypassing slow OpenAI sticky account by TTFT", "account_id", accountID)
			return nil, accountID, nil
		}
	}

	result, acquireErr := s.service.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr == nil && result != nil && result.Acquired {
		_ = s.service.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.service.openAIWSSessionStickyTTL())
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, 0, nil
	}

	// Sticky affinity is soft. A busy sticky account falls through to
	// load-aware selection instead of creating a long head-of-line wait.
	return nil, 0, nil
}

type openAIAccountCandidateScore struct {
	account   *Account
	loadInfo  *AccountLoadInfo
	score     float64
	errorRate float64
	ttft      float64
	hasTTFT   bool
}

type openAIAccountCandidateHeap []openAIAccountCandidateScore

func (h openAIAccountCandidateHeap) Len() int {
	return len(h)
}

func (h openAIAccountCandidateHeap) Less(i, j int) bool {
	// 最小堆根节点保存“最差”候选，便于 O(log k) 维护 topK。
	return isOpenAIAccountCandidateBetter(h[j], h[i])
}

func (h openAIAccountCandidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *openAIAccountCandidateHeap) Push(x any) {
	candidate, ok := x.(openAIAccountCandidateScore)
	if !ok {
		panic("openAIAccountCandidateHeap: invalid element type")
	}
	*h = append(*h, candidate)
}

func (h *openAIAccountCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	*h = old[:n-1]
	return last
}

func isOpenAIAccountCandidateBetter(left openAIAccountCandidateScore, right openAIAccountCandidateScore) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	if left.account.Priority != right.account.Priority {
		return left.account.Priority < right.account.Priority
	}
	if left.loadInfo.LoadRate != right.loadInfo.LoadRate {
		return left.loadInfo.LoadRate < right.loadInfo.LoadRate
	}
	if left.loadInfo.WaitingCount != right.loadInfo.WaitingCount {
		return left.loadInfo.WaitingCount < right.loadInfo.WaitingCount
	}
	return left.account.ID < right.account.ID
}

func selectTopKOpenAICandidates(candidates []openAIAccountCandidateScore, topK int) []openAIAccountCandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	if topK >= len(candidates) {
		ranked := append([]openAIAccountCandidateScore(nil), candidates...)
		sort.Slice(ranked, func(i, j int) bool {
			return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
		})
		return ranked
	}

	best := make(openAIAccountCandidateHeap, 0, topK)
	for _, candidate := range candidates {
		if len(best) < topK {
			heap.Push(&best, candidate)
			continue
		}
		if isOpenAIAccountCandidateBetter(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}

	ranked := make([]openAIAccountCandidateScore, len(best))
	copy(ranked, best)
	sort.Slice(ranked, func(i, j int) bool {
		return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
	})
	return ranked
}

func buildOpenAIScheduleCandidateDiagnostics(
	plan openAIAccountLoadPlan,
	selectedAccountID int64,
) []OpenAIAccountScheduleCandidateDiagnostic {
	if len(plan.candidates) == 0 {
		return nil
	}
	ranked := selectTopKOpenAICandidates(plan.candidates, len(plan.candidates))
	topK := plan.topK
	if topK <= 0 || topK > len(ranked) {
		topK = len(ranked)
	}
	topKIDs := make(map[int64]struct{}, topK)
	for i := 0; i < topK; i++ {
		if ranked[i].account != nil {
			topKIDs[ranked[i].account.ID] = struct{}{}
		}
	}

	orderByAccountID := make(map[int64]int, len(plan.selectionOrder))
	for i, candidate := range plan.selectionOrder {
		if candidate.account != nil {
			orderByAccountID[candidate.account.ID] = i + 1
		}
	}

	out := make([]OpenAIAccountScheduleCandidateDiagnostic, 0, len(ranked))
	for i, candidate := range ranked {
		accountID := int64(0)
		if candidate.account != nil {
			accountID = candidate.account.ID
		}
		_, inTopK := topKIDs[accountID]
		out = append(out, openAIScheduleDiagnosticFromCandidate(
			candidate,
			i+1,
			orderByAccountID[accountID],
			inTopK,
			accountID > 0 && accountID == selectedAccountID,
		))
	}
	return out
}

func openAIScheduleDiagnosticFromCandidate(
	candidate openAIAccountCandidateScore,
	rank int,
	order int,
	inTopK bool,
	selected bool,
) OpenAIAccountScheduleCandidateDiagnostic {
	diag := OpenAIAccountScheduleCandidateDiagnostic{
		Rank:     rank,
		Order:    order,
		InTopK:   inTopK,
		Selected: selected,
	}
	account := candidate.account
	if account != nil {
		diag.AccountID = account.ID
		diag.AccountName = account.Name
		diag.AccountType = account.Type
		diag.Priority = account.Priority
		diag.Concurrency = account.Concurrency
		diag.LoadFactor = account.EffectiveLoadFactor()
		diag.LastUsedAt = formatOpenAIScheduleLastUsedAt(account.LastUsedAt)
	}
	loadInfo := candidate.loadInfo
	if loadInfo != nil {
		diag.CurrentConcurrency = loadInfo.CurrentConcurrency
		diag.WaitingCount = loadInfo.WaitingCount
		diag.LoadRate = loadInfo.LoadRate
	}
	if candidate.score > 0 {
		diag.Score = roundOpenAIScheduleMetric(candidate.score)
	}
	if candidate.errorRate > 0 {
		diag.ErrorRate = roundOpenAIScheduleMetric(candidate.errorRate)
	}
	if candidate.hasTTFT && candidate.ttft > 0 {
		diag.TTFTMs = int(math.Round(candidate.ttft))
		diag.HasTTFT = true
	}
	return diag
}

func markOpenAIScheduleCandidateSelected(
	candidates []OpenAIAccountScheduleCandidateDiagnostic,
	accountID int64,
) []OpenAIAccountScheduleCandidateDiagnostic {
	if accountID <= 0 {
		return candidates
	}
	for i := range candidates {
		candidates[i].Selected = candidates[i].AccountID == accountID
	}
	return candidates
}

func formatOpenAIScheduleLastUsedAt(lastUsedAt *time.Time) string {
	if lastUsedAt == nil {
		return ""
	}
	return lastUsedAt.UTC().Format(time.RFC3339Nano)
}

func roundOpenAIScheduleMetric(value float64) float64 {
	return math.Round(value*10000) / 10000
}

type openAISelectionRNG struct {
	state uint64
}

func newOpenAISelectionRNG(seed uint64) openAISelectionRNG {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return openAISelectionRNG{state: seed}
}

func (r *openAISelectionRNG) nextUint64() uint64 {
	// xorshift64*
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 2685821657736338717
}

func (r *openAISelectionRNG) nextFloat64() float64 {
	// [0,1)
	return float64(r.nextUint64()>>11) / (1 << 53)
}

func deriveOpenAISelectionSeed(req OpenAIAccountScheduleRequest) uint64 {
	hasher := fnv.New64a()
	writeValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		_, _ = hasher.Write([]byte(trimmed))
		_, _ = hasher.Write([]byte{0})
	}

	writeValue(req.SessionHash)
	writeValue(req.PreviousResponseID)
	writeValue(req.RequestedModel)
	if req.GroupID != nil {
		_, _ = hasher.Write([]byte(strconv.FormatInt(*req.GroupID, 10)))
	}

	seed := hasher.Sum64()
	// 对“无会话锚点”的纯负载均衡请求引入时间熵，避免固定命中同一账号。
	if strings.TrimSpace(req.SessionHash) == "" && strings.TrimSpace(req.PreviousResponseID) == "" {
		seed ^= uint64(time.Now().UnixNano())
	}
	if seed == 0 {
		seed = uint64(time.Now().UnixNano()) ^ 0x9e3779b97f4a7c15
	}
	return seed
}

func buildOpenAIWeightedSelectionOrder(
	candidates []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if len(candidates) <= 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}

	pool := append([]openAIAccountCandidateScore(nil), candidates...)
	weights := make([]float64, len(pool))
	minScore := pool[0].score
	for i := 1; i < len(pool); i++ {
		if pool[i].score < minScore {
			minScore = pool[i].score
		}
	}
	for i := range pool {
		// 将 top-K 分值平移到正区间，避免“单一最高分账号”长期垄断。
		weight := (pool[i].score - minScore) + 1.0
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			weight = 1.0
		}
		weights[i] = weight
	}

	order := make([]openAIAccountCandidateScore, 0, len(pool))
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	for len(pool) > 0 {
		total := 0.0
		for _, w := range weights {
			total += w
		}

		selectedIdx := 0
		if total > 0 {
			r := rng.nextFloat64() * total
			acc := 0.0
			for i, w := range weights {
				acc += w
				if r <= acc {
					selectedIdx = i
					break
				}
			}
		} else {
			selectedIdx = int(rng.nextUint64() % uint64(len(pool)))
		}

		order = append(order, pool[selectedIdx])
		pool = append(pool[:selectedIdx], pool[selectedIdx+1:]...)
		weights = append(weights[:selectedIdx], weights[selectedIdx+1:]...)
	}
	return order
}

func (s *defaultOpenAIAccountScheduler) buildOpenAIAccountLoadPlan(
	req OpenAIAccountScheduleRequest,
	filtered []*Account,
	loadMap map[int64]*AccountLoadInfo,
) openAIAccountLoadPlan {
	allCandidates := make([]openAIAccountCandidateScore, 0, len(filtered))
	for _, account := range filtered {
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		errorRate, ttft, hasTTFT := 0.0, 0.0, false
		if s.stats != nil {
			errorRate, ttft, hasTTFT = s.stats.snapshotForModel(account.ID, req.RequestedModel)
		}
		allCandidates = append(allCandidates, openAIAccountCandidateScore{
			account:   account,
			loadInfo:  loadInfo,
			errorRate: errorRate,
			ttft:      ttft,
			hasTTFT:   hasTTFT,
		})
	}

	candidates := allCandidates
	staleSnapshotCompactRetry := make([]openAIAccountCandidateScore, 0, len(allCandidates))
	if req.RequireCompact {
		candidates = make([]openAIAccountCandidateScore, 0, len(allCandidates))
		for _, candidate := range allCandidates {
			if openAICompactSupportTier(candidate.account) == 0 {
				staleSnapshotCompactRetry = append(staleSnapshotCompactRetry, candidate)
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	plan := openAIAccountLoadPlan{
		allCandidates:             allCandidates,
		candidates:                candidates,
		staleSnapshotCompactRetry: staleSnapshotCompactRetry,
		candidateCount:            len(candidates),
	}
	if len(candidates) == 0 {
		plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
		return plan
	}

	minPriority, maxPriority := candidates[0].account.Priority, candidates[0].account.Priority
	maxWaiting := 1
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	minTTFT, maxTTFT := 0.0, 0.0
	hasTTFTSample := false
	for _, candidate := range candidates {
		if candidate.account.Priority < minPriority {
			minPriority = candidate.account.Priority
		}
		if candidate.account.Priority > maxPriority {
			maxPriority = candidate.account.Priority
		}
		if candidate.loadInfo.WaitingCount > maxWaiting {
			maxWaiting = candidate.loadInfo.WaitingCount
		}
		if candidate.hasTTFT && candidate.ttft > 0 {
			if !hasTTFTSample {
				minTTFT, maxTTFT = candidate.ttft, candidate.ttft
				hasTTFTSample = true
			} else {
				if candidate.ttft < minTTFT {
					minTTFT = candidate.ttft
				}
				if candidate.ttft > maxTTFT {
					maxTTFT = candidate.ttft
				}
			}
		}
		loadRate := float64(candidate.loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate
	}
	plan.loadSkew = calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, len(candidates))

	weights := s.service.openAIWSSchedulerWeights()
	for i := range candidates {
		item := &candidates[i]
		priorityFactor := 1.0
		if maxPriority > minPriority {
			priorityFactor = 1 - float64(item.account.Priority-minPriority)/float64(maxPriority-minPriority)
		}
		loadFactor := 1 - clamp01(float64(item.loadInfo.LoadRate)/100.0)
		queueFactor := 1 - clamp01(float64(item.loadInfo.WaitingCount)/float64(maxWaiting))
		errorFactor := 1 - clamp01(item.errorRate)
		ttftFactor := 0.5
		if item.hasTTFT && hasTTFTSample && maxTTFT > minTTFT {
			ttftFactor = 1 - clamp01((item.ttft-minTTFT)/(maxTTFT-minTTFT))
		}

		item.score = weights.Priority*priorityFactor +
			weights.Load*loadFactor +
			weights.Queue*queueFactor +
			weights.ErrorRate*errorFactor +
			weights.TTFT*ttftFactor
	}
	plan.candidates = candidates

	plan.topK = s.service.openAIWSLBTopK()
	if plan.topK > len(candidates) {
		plan.topK = len(candidates)
	}
	if plan.topK <= 0 {
		plan.topK = 1
	}

	plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
	return plan
}

func (s *defaultOpenAIAccountScheduler) buildOpenAISelectionOrder(
	req OpenAIAccountScheduleRequest,
	plan openAIAccountLoadPlan,
) []openAIAccountCandidateScore {
	buildSelectionOrder := func(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
		if len(pool) == 0 || plan.topK <= 0 {
			return nil
		}
		groupTopK := plan.topK
		if groupTopK > len(pool) {
			groupTopK = len(pool)
		}
		ranked := selectTopKOpenAICandidates(pool, groupTopK)
		return buildOpenAIWeightedSelectionOrder(ranked, req)
	}

	if req.RequireCompact {
		supported := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		unknown := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		for _, candidate := range plan.candidates {
			switch openAICompactSupportTier(candidate.account) {
			case 2:
				supported = append(supported, candidate)
			case 1:
				unknown = append(unknown, candidate)
			}
		}
		selectionOrder := make([]openAIAccountCandidateScore, 0, len(plan.allCandidates))
		selectionOrder = append(selectionOrder, buildSelectionOrder(supported)...)
		selectionOrder = append(selectionOrder, buildSelectionOrder(unknown)...)
		if len(plan.staleSnapshotCompactRetry) > 0 && s.service.schedulerSnapshot != nil {
			selectionOrder = append(selectionOrder, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
		}
		return selectionOrder
	}

	return buildSelectionOrder(plan.candidates)
}

func sortOpenAICompactRetryCandidates(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	if len(pool) == 0 {
		return nil
	}
	ordered := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
			return a.loadInfo.LoadRate < b.loadInfo.LoadRate
		}
		if a.loadInfo.WaitingCount != b.loadInfo.WaitingCount {
			return a.loadInfo.WaitingCount < b.loadInfo.WaitingCount
		}
		switch {
		case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
			return true
		case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
			return false
		case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
			return false
		default:
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})
	return ordered
}

func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAISelectionOrder(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selectionOrder []openAIAccountCandidateScore,
) (*AccountSelectionResult, bool, error) {
	compactBlocked := false
	for i := 0; i < len(selectionOrder); i++ {
		candidate := selectionOrder[i]
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.RequestedModel, false)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.RequestedModel, false)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		result, acquireErr := s.service.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
		if acquireErr != nil {
			return nil, compactBlocked, acquireErr
		}
		if result != nil && result.Acquired {
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)
			}
			return &AccountSelectionResult{
				Account:     fresh,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, compactBlocked, nil
		}
	}
	return nil, compactBlocked, nil
}

func (s *defaultOpenAIAccountScheduler) selectByLoadBalance(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int, int, float64, []OpenAIAccountScheduleCandidateDiagnostic, error) {
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID)
	if err != nil {
		return nil, 0, 0, 0, nil, err
	}
	if len(accounts) == 0 {
		return nil, 0, 0, 0, nil, noAvailableOpenAISelectionError(req.RequestedModel, false)
	}

	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if req.GroupID != nil && s.service.schedulerSnapshot != nil {
		schedGroup, _ = s.service.schedulerSnapshot.GetGroupByID(ctx, *req.GroupID)
	}

	filtered := make([]*Account, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !account.IsSchedulable() || !account.IsOpenAI() {
			continue
		}
		if s.service.isOpenAIAccountRuntimeBlocked(account) {
			continue
		}
		// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
		if schedGroup != nil && schedGroup.RequirePrivacySet && !account.IsPrivacySet() {
			s.service.BlockAccountScheduling(account, time.Time{}, "privacy_not_set")
			_ = s.service.accountRepo.SetError(ctx, account.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		if !s.isAccountRequestCompatible(ctx, account, req) {
			continue
		}
		if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
			continue
		}
		filtered = append(filtered, account)
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.EffectiveLoadFactor(),
		})
	}
	if len(filtered) == 0 {
		return nil, 0, 0, 0, nil, noAvailableOpenAISelectionError(req.RequestedModel, false)
	}

	loadMap := map[int64]*AccountLoadInfo{}
	if s.service.concurrencyService != nil {
		if batchLoad, loadErr := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
			loadMap = batchLoad
		}
	}

	plan := s.buildOpenAIAccountLoadPlan(req, filtered, loadMap)
	candidateCount := plan.candidateCount
	topK := plan.topK
	loadSkew := plan.loadSkew
	selectionOrder := plan.selectionOrder
	diagnostics := buildOpenAIScheduleCandidateDiagnostics(plan, 0)
	if req.RequireCompact && len(plan.candidates) == 0 && len(plan.staleSnapshotCompactRetry) == 0 {
		return nil, 0, 0, 0, diagnostics, ErrNoAvailableCompactAccounts
	}
	if req.RequireCompact && len(selectionOrder) == 0 && s.service.schedulerSnapshot == nil {
		return nil, candidateCount, topK, loadSkew, diagnostics, ErrNoAvailableCompactAccounts
	}
	if len(selectionOrder) == 0 {
		return nil, candidateCount, topK, loadSkew, diagnostics, noAvailableOpenAISelectionError(req.RequestedModel, req.RequireCompact && len(plan.allCandidates) > 0)
	}

	result, compactBlocked, acquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, selectionOrder)
	if acquireErr != nil {
		return nil, candidateCount, topK, loadSkew, diagnostics, acquireErr
	}
	if result != nil {
		if result.Account != nil {
			markOpenAIScheduleCandidateSelected(diagnostics, result.Account.ID)
		}
		return result, candidateCount, topK, loadSkew, diagnostics, nil
	}

	if s.service.concurrencyService != nil {
		if freshLoadMap, loadErr := s.service.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadReq); loadErr == nil {
			freshPlan := s.buildOpenAIAccountLoadPlan(req, filtered, freshLoadMap)
			if len(freshPlan.selectionOrder) > 0 {
				freshDiagnostics := buildOpenAIScheduleCandidateDiagnostics(freshPlan, 0)
				freshResult, freshCompactBlocked, freshAcquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, freshPlan.selectionOrder)
				if freshAcquireErr != nil {
					return nil, candidateCount, topK, loadSkew, freshDiagnostics, freshAcquireErr
				}
				if freshResult != nil {
					if freshResult.Account != nil {
						markOpenAIScheduleCandidateSelected(freshDiagnostics, freshResult.Account.ID)
					}
					return freshResult, freshPlan.candidateCount, freshPlan.topK, freshPlan.loadSkew, freshDiagnostics, nil
				}
				compactBlocked = compactBlocked || freshCompactBlocked
				selectionOrder = freshPlan.selectionOrder
				candidateCount = freshPlan.candidateCount
				topK = freshPlan.topK
				loadSkew = freshPlan.loadSkew
				diagnostics = freshDiagnostics
			}
		}
	}

	cfg := s.service.schedulingConfig()
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	for _, candidate := range selectionOrder {
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.RequestedModel, false)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.RequestedModel, false)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		return &AccountSelectionResult{
			Account: fresh,
			WaitPlan: &AccountWaitPlan{
				AccountID:      fresh.ID,
				MaxConcurrency: fresh.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
				SessionID:      req.SessionHash,
			},
		}, candidateCount, topK, loadSkew, markOpenAIScheduleCandidateSelected(diagnostics, fresh.ID), nil
	}

	return nil, candidateCount, topK, loadSkew, diagnostics, noAvailableOpenAISelectionError(req.RequestedModel, compactBlocked)
}

func (s *defaultOpenAIAccountScheduler) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil {
		return false
	}
	return s.service.isOpenAIAccountTransportCompatible(account, requiredTransport)
}

func (s *defaultOpenAIAccountScheduler) isAccountRequestCompatible(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) bool {
	if account == nil {
		return false
	}
	if s != nil && s.service != nil && s.service.isOpenAIAccountRuntimeBlocked(account) {
		return false
	}
	if s != nil && s.service != nil && s.service.isOpenAIAccountModelRuntimeBlocked(account, req.RequestedModel) {
		return false
	}
	if req.RequestedModel != "" && !account.IsModelSupported(req.RequestedModel) {
		return false
	}
	if req.GroupID != nil && s != nil && s.service != nil &&
		s.service.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
		s.service.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
		return false
	}
	return account.SupportsOpenAIImageCapability(req.RequiredImageCapability)
}

func (s *defaultOpenAIAccountScheduler) ReportResult(accountID int64, success bool, firstTokenMs *int) {
	if s == nil || s.stats == nil {
		return
	}
	s.stats.report(accountID, success, firstTokenMs)
}

func (s *defaultOpenAIAccountScheduler) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

func (s *defaultOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	if s == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}

	selectTotal := s.metrics.selectTotal.Load()
	prevHit := s.metrics.stickyPreviousHitTotal.Load()
	sessionHit := s.metrics.stickySessionHitTotal.Load()
	switchTotal := s.metrics.accountSwitchTotal.Load()
	latencyTotal := s.metrics.latencyMsTotal.Load()
	loadSkewTotal := s.metrics.loadSkewMilliTotal.Load()

	snapshot := OpenAIAccountSchedulerMetricsSnapshot{
		SelectTotal:              selectTotal,
		StickyPreviousHitTotal:   prevHit,
		StickySessionHitTotal:    sessionHit,
		LoadBalanceSelectTotal:   s.metrics.loadBalanceSelectTotal.Load(),
		AccountSwitchTotal:       switchTotal,
		SchedulerLatencyMsTotal:  latencyTotal,
		RuntimeStatsAccountCount: s.stats.size(),
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.AccountSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

func (s *OpenAIGatewayService) openAIAdvancedSchedulerSettingRepo() SettingRepository {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.settingService == nil {
		return nil
	}
	return s.rateLimitService.settingService.settingRepo
}

func (s *OpenAIGatewayService) isOpenAIAdvancedSchedulerEnabled(ctx context.Context) bool {
	if cached, ok := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}

	result, _, _ := openAIAdvancedSchedulerSettingSF.Do(openAIAdvancedSchedulerSettingKey, func() (any, error) {
		if cached, ok := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}

		enabled := false
		if repo := s.openAIAdvancedSchedulerSettingRepo(); repo != nil {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAdvancedSchedulerSettingDBTimeout)
			defer cancel()

			value, err := repo.GetValue(dbCtx, openAIAdvancedSchedulerSettingKey)
			if err == nil {
				enabled = strings.EqualFold(strings.TrimSpace(value), "true")
			}
		}

		openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
			enabled:   enabled,
			expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
		})
		return enabled, nil
	})

	enabled, _ := result.(bool)
	return enabled
}

func (s *OpenAIGatewayService) getOpenAIAccountScheduler(ctx context.Context) OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	if !s.isOpenAIAdvancedSchedulerEnabled(ctx) {
		return nil
	}
	s.openaiSchedulerOnce.Do(func() {
		if s.openaiScheduler == nil {
			s.openaiScheduler = newDefaultOpenAIAccountScheduler(s, s.getOpenAIAccountRuntimeStats())
		}
	})
	return s.openaiScheduler
}

func resetOpenAIAdvancedSchedulerSettingCacheForTest() {
	openAIAdvancedSchedulerSettingCache = atomic.Value{}
	openAIAdvancedSchedulerSettingSF = singleflight.Group{}
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requireCompact bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, "", requireCompact)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForImages(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIImagesCapability,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	selection, decision, err := s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, requiredCapability, false)
	if err == nil && selection != nil && selection.Account != nil {
		return selection, decision, nil
	}
	// 如果要求 native 能力（如指定了模型）但没有可用的 APIKey 账号，回退到 basic（OAuth 账号）
	if requiredCapability == OpenAIImagesCapabilityNative {
		return s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, OpenAIImagesCapabilityBasic, false)
	}
	return selection, decision, err
}

func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerLoadBalance}
	scheduler := s.getOpenAIAccountScheduler(ctx)
	if scheduler == nil {
		ctx = withOpenAILegacyScheduleDecision(ctx, &decision)
		if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
			effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
			for {
				selection, err := s.selectAccountWithLoadAwareness(
					ctx,
					groupID,
					sessionHash,
					requestedModel,
					effectiveExcludedIDs,
					requiredTransport,
					requiredImageCapability,
					requireCompact,
					strings.TrimSpace(previousResponseID) == "",
				)
				if err != nil {
					return nil, decision, err
				}
				if selection == nil || selection.Account == nil {
					return selection, decision, nil
				}
				if selection.Account.SupportsOpenAIImageCapability(requiredImageCapability) {
					decision.SelectedAccountID = selection.Account.ID
					decision.SelectedAccountType = selection.Account.Type
					decision.Candidates = markOpenAIScheduleCandidateSelected(decision.Candidates, selection.Account.ID)
					return selection, decision, nil
				}
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if effectiveExcludedIDs == nil {
					effectiveExcludedIDs = make(map[int64]struct{})
				}
				if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
					return nil, decision, ErrNoAvailableAccounts
				}
				effectiveExcludedIDs[selection.Account.ID] = struct{}{}
			}
		}

		effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
		for {
			selection, err := s.selectAccountWithLoadAwareness(
				ctx,
				groupID,
				sessionHash,
				requestedModel,
				effectiveExcludedIDs,
				requiredTransport,
				requiredImageCapability,
				requireCompact,
				strings.TrimSpace(previousResponseID) == "",
			)
			if err != nil {
				return nil, decision, err
			}
			if selection == nil || selection.Account == nil {
				return selection, decision, nil
			}
			if s.isOpenAIAccountTransportCompatible(selection.Account, requiredTransport) {
				decision.SelectedAccountID = selection.Account.ID
				decision.SelectedAccountType = selection.Account.Type
				decision.Candidates = markOpenAIScheduleCandidateSelected(decision.Candidates, selection.Account.ID)
				return selection, decision, nil
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if effectiveExcludedIDs == nil {
				effectiveExcludedIDs = make(map[int64]struct{})
			}
			if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
				return nil, decision, ErrNoAvailableAccounts
			}
			effectiveExcludedIDs[selection.Account.ID] = struct{}{}
		}
	}

	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, decision, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			stickyAccountID = accountID
		}
	}

	return scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID:                 groupID,
		SessionHash:             sessionHash,
		StickyAccountID:         stickyAccountID,
		PreviousResponseID:      previousResponseID,
		RequestedModel:          requestedModel,
		RequiredTransport:       requiredTransport,
		RequiredImageCapability: requiredImageCapability,
		RequireCompact:          requireCompact,
		ExcludedIDs:             excludedIDs,
	})
}

func (s *OpenAIGatewayService) countLegacyOpenAISchedulerCandidates(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
) int {
	if s == nil {
		return 0
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID)
	if err != nil {
		return 0
	}
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	count := 0
	for i := range accounts {
		account := &accounts[i]
		if excludedIDs != nil {
			if _, excluded := excludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !isOpenAIAccountEligibleForRequest(account, requestedModel, requireCompact) {
			continue
		}
		if s.isOpenAIAccountRuntimeBlocked(account) {
			continue
		}
		if s.isOpenAIAccountModelRuntimeBlocked(account, requestedModel) {
			continue
		}
		if needsUpstreamCheck && groupID != nil && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
			continue
		}
		if !s.isOpenAIAccountTransportCompatible(account, requiredTransport) {
			continue
		}
		if !account.SupportsOpenAIImageCapability(requiredImageCapability) {
			continue
		}
		count++
	}
	return count
}

func buildLegacyOpenAISchedulerCandidateDiagnostics(available []accountWithLoad) []OpenAIAccountScheduleCandidateDiagnostic {
	if len(available) == 0 {
		return nil
	}
	out := make([]OpenAIAccountScheduleCandidateDiagnostic, 0, len(available))
	for i, item := range available {
		candidate := openAIAccountCandidateScore{
			account:  item.account,
			loadInfo: item.loadInfo,
		}
		out = append(out, openAIScheduleDiagnosticFromCandidate(candidate, i+1, i+1, true, false))
	}
	return out
}

func accountsWithDefaultLoad(accounts []*Account) []accountWithLoad {
	if len(accounts) == 0 {
		return nil
	}
	out := make([]accountWithLoad, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		out = append(out, accountWithLoad{
			account:  account,
			loadInfo: &AccountLoadInfo{AccountID: account.ID},
		})
	}
	return out
}

func cloneExcludedAccountIDs(excludedIDs map[int64]struct{}) map[int64]struct{} {
	if len(excludedIDs) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(excludedIDs))
	for id := range excludedIDs {
		cloned[id] = struct{}{}
	}
	return cloned
}

func (s *OpenAIGatewayService) isOpenAIAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || account == nil {
		return false
	}
	return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResult(accountID int64, success bool, firstTokenMs *int) {
	s.ReportOpenAIAccountScheduleResultForModel(accountID, "", success, firstTokenMs)
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResultForModel(accountID int64, requestedModel string, success bool, firstTokenMs *int) {
	if s == nil {
		return
	}
	if success {
		s.clearOpenAIAccountConsecutiveFailures(accountID)
		s.clearOpenAIAccountModelFailures(accountID, requestedModel)
	}
	stats := s.getOpenAIAccountRuntimeStats()
	stats.reportForModel(accountID, requestedModel, success, firstTokenMs)
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return
	}
	if defaultScheduler, ok := scheduler.(*defaultOpenAIAccountScheduler); ok && defaultScheduler.stats == stats {
		return
	}
	scheduler.ReportResult(accountID, success, firstTokenMs)
}

func (s *OpenAIGatewayService) RecordOpenAIAccountSwitch() {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *OpenAIGatewayService) SnapshotOpenAIAccountSchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return openaiStickySessionTTL
}

func (s *OpenAIGatewayService) openAIWSLBTopK() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.LBTopK > 0 {
		return s.cfg.Gateway.OpenAIWS.LBTopK
	}
	return 7
}

func (s *OpenAIGatewayService) openAIWSSchedulerWeights() GatewayOpenAIWSSchedulerScoreWeightsView {
	if s != nil && s.cfg != nil {
		return GatewayOpenAIWSSchedulerScoreWeightsView{
			Priority:  s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority,
			Load:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load,
			Queue:     s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue,
			ErrorRate: s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate,
			TTFT:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT,
		}
	}
	return GatewayOpenAIWSSchedulerScoreWeightsView{
		Priority:  1.0,
		Load:      1.0,
		Queue:     0.7,
		ErrorRate: 0.8,
		TTFT:      0.5,
	}
}

type GatewayOpenAIWSSchedulerScoreWeightsView struct {
	Priority  float64
	Load      float64
	Queue     float64
	ErrorRate float64
	TTFT      float64
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func calcLoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
