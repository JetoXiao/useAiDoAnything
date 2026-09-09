package service

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	ErrAffiliateProfileNotFound             = infraerrors.NotFound("AFFILIATE_PROFILE_NOT_FOUND", "affiliate profile not found")
	ErrAffiliateCodeInvalid                 = infraerrors.BadRequest("AFFILIATE_CODE_INVALID", "invalid affiliate code")
	ErrAffiliateCodeTaken                   = infraerrors.Conflict("AFFILIATE_CODE_TAKEN", "affiliate code already in use")
	ErrAffiliateAlreadyBound                = infraerrors.Conflict("AFFILIATE_ALREADY_BOUND", "affiliate inviter already bound")
	ErrAffiliateSelfBinding                 = infraerrors.BadRequest("AFFILIATE_SELF_BINDING", "invitee cannot be inviter")
	ErrAffiliateQuotaEmpty                  = infraerrors.BadRequest("AFFILIATE_QUOTA_EMPTY", "no affiliate quota available to transfer")
	ErrAffiliatePartnerTransferUnsupported  = infraerrors.Forbidden("AFFILIATE_PARTNER_TRANSFER_UNSUPPORTED", "partner rebates are settled separately")
	ErrAffiliatePartnerLevelInvalid         = infraerrors.BadRequest("AFFILIATE_PARTNER_LEVEL_INVALID", "invalid partner level")
	ErrAffiliatePartnerApplicationPending   = infraerrors.Conflict("AFFILIATE_PARTNER_APPLICATION_PENDING", "pending partner application already exists")
	ErrAffiliatePartnerApplicationNotFound  = infraerrors.NotFound("AFFILIATE_PARTNER_APPLICATION_NOT_FOUND", "partner application not found")
	ErrAffiliatePartnerApplicationFinalized = infraerrors.Conflict("AFFILIATE_PARTNER_APPLICATION_FINALIZED", "partner application already reviewed")
)

var affiliatePartnerLevelOrder = []string{
	AffiliatePartnerLevelSpark,
	AffiliatePartnerLevelVoyage,
	AffiliatePartnerLevelSummit,
	AffiliatePartnerLevelCoCreate,
}

const (
	affiliateInviteesLimit = 100
	// AffiliateUsageCommissionRateDefault is the default percentage used by the
	// admin usage-share report when an inviter has no per-user override.
	AffiliateUsageCommissionRateDefault = 5.0
	// AffiliateCodeMinLength / AffiliateCodeMaxLength bound both system-generated
	// 12-char codes and admin-customized codes (e.g. "VIP2026").
	AffiliateCodeMinLength = 4
	AffiliateCodeMaxLength = 32

	AffiliatePartnerLevelNone     = "none"
	AffiliatePartnerLevelSpark    = "spark"
	AffiliatePartnerLevelVoyage   = "voyage"
	AffiliatePartnerLevelSummit   = "summit"
	AffiliatePartnerLevelCoCreate = "cocreate"

	AffiliatePartnerApplicationStatusPending  = "pending"
	AffiliatePartnerApplicationStatusApproved = "approved"
	AffiliatePartnerApplicationStatusRejected = "rejected"
)

type AffiliatePartnerTier struct {
	Level                string  `json:"level"`
	Name                 string  `json:"name"`
	RebateRatePercent    float64 `json:"rebate_rate_percent"`
	RequiredInvitees     int     `json:"required_invitees"`
	NextRequiredInvitees *int    `json:"next_required_invitees,omitempty"`
}

var affiliatePartnerTierDefaults = []AffiliatePartnerTier{
	{Level: AffiliatePartnerLevelSpark, Name: "Spark", RebateRatePercent: 40, RequiredInvitees: 10},
	{Level: AffiliatePartnerLevelVoyage, Name: "Voyage", RebateRatePercent: 50, RequiredInvitees: 30},
	{Level: AffiliatePartnerLevelSummit, Name: "Summit", RebateRatePercent: 60, RequiredInvitees: 50},
	{Level: AffiliatePartnerLevelCoCreate, Name: "Co-create", RebateRatePercent: 70, RequiredInvitees: 100},
}

func AffiliatePartnerTiers() []AffiliatePartnerTier {
	return withAffiliatePartnerTierProgress(affiliatePartnerTierDefaults)
}

func NormalizeAffiliatePartnerTiers(input []AffiliatePartnerTier) []AffiliatePartnerTier {
	byLevel := make(map[string]AffiliatePartnerTier, len(input))
	for _, tier := range input {
		level := NormalizeAffiliatePartnerLevel(tier.Level)
		if level == AffiliatePartnerLevelNone || AffiliatePartnerLevelRank(level) <= 0 {
			continue
		}
		byLevel[level] = tier
	}

	tiers := make([]AffiliatePartnerTier, 0, len(affiliatePartnerTierDefaults))
	minInvitees := 0
	for _, defaults := range affiliatePartnerTierDefaults {
		tier := defaults
		if raw, ok := byLevel[defaults.Level]; ok {
			if !math.IsNaN(raw.RebateRatePercent) && !math.IsInf(raw.RebateRatePercent, 0) {
				tier.RebateRatePercent = clampAffiliateRebateRate(raw.RebateRatePercent)
			}
			if raw.RequiredInvitees >= 0 {
				tier.RequiredInvitees = raw.RequiredInvitees
			}
		}
		if tier.RequiredInvitees < minInvitees {
			tier.RequiredInvitees = minInvitees
		}
		minInvitees = tier.RequiredInvitees + 1
		tier.NextRequiredInvitees = nil
		tiers = append(tiers, tier)
	}
	return withAffiliatePartnerTierProgress(tiers)
}

func AffiliatePartnerTierByLevelFrom(tiers []AffiliatePartnerTier, level string) (AffiliatePartnerTier, bool) {
	level = NormalizeAffiliatePartnerLevel(level)
	for _, tier := range NormalizeAffiliatePartnerTiers(tiers) {
		if tier.Level == level {
			return tier, true
		}
	}
	return AffiliatePartnerTier{}, false
}

func AffiliatePartnerTierByInviteCountFrom(tiers []AffiliatePartnerTier, invitees int) (AffiliatePartnerTier, bool) {
	var best AffiliatePartnerTier
	found := false
	for _, tier := range NormalizeAffiliatePartnerTiers(tiers) {
		if invitees >= tier.RequiredInvitees {
			best = tier
			found = true
		}
	}
	return best, found
}

func AffiliatePartnerTierByRebateRatePercentFrom(tiers []AffiliatePartnerTier, ratePercent float64) (AffiliatePartnerTier, bool) {
	if math.IsNaN(ratePercent) || math.IsInf(ratePercent, 0) {
		return AffiliatePartnerTier{}, false
	}
	for _, tier := range NormalizeAffiliatePartnerTiers(tiers) {
		if math.Abs(ratePercent-tier.RebateRatePercent) <= 0.0001 {
			return tier, true
		}
	}
	return AffiliatePartnerTier{}, false
}

func withAffiliatePartnerTierProgress(input []AffiliatePartnerTier) []AffiliatePartnerTier {
	tiers := make([]AffiliatePartnerTier, len(input))
	copy(tiers, input)
	for i := range tiers {
		tiers[i].NextRequiredInvitees = nil
		if i+1 < len(tiers) {
			next := tiers[i+1].RequiredInvitees
			tiers[i].NextRequiredInvitees = &next
		}
	}
	return tiers
}

func NormalizeAffiliatePartnerLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "", "normal", "user":
		return AffiliatePartnerLevelNone
	case "spark", "bronze":
		return AffiliatePartnerLevelSpark
	case "voyage", "silver":
		return AffiliatePartnerLevelVoyage
	case "summit", "gold":
		return AffiliatePartnerLevelSummit
	case "cocreate", "co-create", "diamond":
		return AffiliatePartnerLevelCoCreate
	default:
		return level
	}
}

func AffiliatePartnerTierByLevel(level string) (AffiliatePartnerTier, bool) {
	return AffiliatePartnerTierByLevelFrom(AffiliatePartnerTiers(), level)
}

func AffiliatePartnerLevelRank(level string) int {
	level = NormalizeAffiliatePartnerLevel(level)
	if level == AffiliatePartnerLevelNone {
		return 0
	}
	for i, tierLevel := range affiliatePartnerLevelOrder {
		if tierLevel == level {
			return i + 1
		}
	}
	return -1
}

func AffiliatePartnerTierByInviteCount(invitees int) (AffiliatePartnerTier, bool) {
	return AffiliatePartnerTierByInviteCountFrom(AffiliatePartnerTiers(), invitees)
}

func AffiliatePartnerTierByRebateRatePercent(ratePercent float64) (AffiliatePartnerTier, bool) {
	return AffiliatePartnerTierByRebateRatePercentFrom(AffiliatePartnerTiers(), ratePercent)
}

// affiliateCodeValidChar accepts uppercase letters, digits, underscore and dash.
// All input passes through strings.ToUpper before validation, so lowercase from
// users is normalized — admins may supply mixed case in their UI.
var affiliateCodeValidChar = func() [256]bool {
	var tbl [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		tbl[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		tbl[c] = true
	}
	tbl['_'] = true
	tbl['-'] = true
	return tbl
}()

// isValidAffiliateCodeFormat validates code format for both binding (user input)
// and admin updates. Caller is expected to upper-case the input first.
func isValidAffiliateCodeFormat(code string) bool {
	if len(code) < AffiliateCodeMinLength || len(code) > AffiliateCodeMaxLength {
		return false
	}
	for i := 0; i < len(code); i++ {
		if !affiliateCodeValidChar[code[i]] {
			return false
		}
	}
	return true
}

type AffiliateSummary struct {
	UserID               int64     `json:"user_id"`
	AffCode              string    `json:"aff_code"`
	AffCodeCustom        bool      `json:"aff_code_custom"`
	AffRebateRatePercent *float64  `json:"aff_rebate_rate_percent,omitempty"`
	PartnerLevel         string    `json:"partner_level"`
	InviterID            *int64    `json:"inviter_id,omitempty"`
	AffCount             int       `json:"aff_count"`
	AffQuota             float64   `json:"aff_quota"`
	AffFrozenQuota       float64   `json:"aff_frozen_quota"`
	AffHistoryQuota      float64   `json:"aff_history_quota"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type AffiliateInvitee struct {
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	Username           string     `json:"username"`
	CreatedAt          *time.Time `json:"created_at,omitempty"`
	RechargeAmount     float64    `json:"recharge_amount"`
	RechargeAmountCNY  float64    `json:"recharge_amount_cny"`
	RechargeAmountUSDT float64    `json:"recharge_amount_usdt"`
	TotalRebate        float64    `json:"total_rebate"`
}

type AffiliateDetail struct {
	UserID             int64                        `json:"user_id"`
	AffCode            string                       `json:"aff_code"`
	InviterID          *int64                       `json:"inviter_id,omitempty"`
	AffCount           int                          `json:"aff_count"`
	AffQuota           float64                      `json:"aff_quota"`
	AffFrozenQuota     float64                      `json:"aff_frozen_quota"`
	AffHistoryQuota    float64                      `json:"aff_history_quota"`
	PartnerLevel       string                       `json:"partner_level"`
	PartnerTier        *AffiliatePartnerTier        `json:"partner_tier,omitempty"`
	PartnerTiers       []AffiliatePartnerTier       `json:"partner_tiers"`
	PartnerApplication *AffiliatePartnerApplication `json:"partner_application,omitempty"`
	// EffectiveRebateRatePercent 是当前用户作为邀请人时实际生效的返利比例：
	// 优先用户自己的专属比例（aff_rebate_rate_percent），否则回退到全局比例。
	// 用于在用户的 /affiliate 页面直观展示「分享后能拿到多少」。
	EffectiveRebateRatePercent float64            `json:"effective_rebate_rate_percent"`
	Invitees                   []AffiliateInvitee `json:"invitees"`
}

type AffiliateRepository interface {
	EnsureUserAffiliate(ctx context.Context, userID int64) (*AffiliateSummary, error)
	GetAffiliateByCode(ctx context.Context, code string) (*AffiliateSummary, error)
	BindInviter(ctx context.Context, userID, inviterID int64) (bool, error)
	AccrueQuota(ctx context.Context, inviterID, inviteeUserID int64, amount float64, freezeHours int, sourceOrderID *int64) (bool, error)
	GetAccruedRebateFromInvitee(ctx context.Context, inviterID, inviteeUserID int64) (float64, error)
	ThawFrozenQuota(ctx context.Context, userID int64) (float64, error)
	TransferQuotaToBalance(ctx context.Context, userID int64) (float64, float64, error)
	ListInvitees(ctx context.Context, inviterID int64, limit int) ([]AffiliateInvitee, error)

	// 管理端：用户级专属配置
	UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error
	ResetUserAffCode(ctx context.Context, userID int64) (string, error)
	SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error
	BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error
	SetUserPartnerLevel(ctx context.Context, userID int64, level string) error
	PromotePartnerLevelForInviteCount(ctx context.Context, userID int64, tiers []AffiliatePartnerTier) (*AffiliatePartnerTier, bool, error)
	GetPartnerSummariesByUserIDs(ctx context.Context, userIDs []int64) (map[int64]AffiliatePartnerSummary, error)
	CreatePartnerApplication(ctx context.Context, userID int64, input AffiliatePartnerApplicationInput) (*AffiliatePartnerApplication, error)
	GetLatestPartnerApplication(ctx context.Context, userID int64) (*AffiliatePartnerApplication, error)
	ListPartnerApplications(ctx context.Context, filter AffiliatePartnerApplicationFilter) ([]AffiliatePartnerApplication, int64, error)
	ReviewPartnerApplication(ctx context.Context, applicationID, reviewerID int64, input AffiliatePartnerApplicationReviewInput) (*AffiliatePartnerApplication, error)
	ListUsersWithCustomSettings(ctx context.Context, filter AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error)
	AdminAssignInviter(ctx context.Context, inviteeID, inviterID int64) (*AffiliateInviteAssignment, error)
	ListAffiliateInviteRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error)
	ListAffiliateUsageDailyRecords(ctx context.Context, filter AffiliateUsageFilter) ([]AffiliateUsageDailyRecord, *AffiliateUsageSummary, int64, error)
	ListAffiliateRebateRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error)
	ListAffiliateTransferRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateTransferRecord, int64, error)
	CreateAffiliateSettlement(ctx context.Context, input AffiliateSettlementInput) (*AffiliateSettlementRecord, error)
	ListAffiliateSettlementRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateSettlementRecord, int64, error)
	GetAffiliateUserOverview(ctx context.Context, userID int64) (*AffiliateUserOverview, error)
}

// AffiliateAdminFilter 列表筛选条件
type AffiliateAdminFilter struct {
	Search   string
	Page     int
	PageSize int
}

// AffiliateAdminEntry 专属用户列表条目
type AffiliateAdminEntry struct {
	UserID               int64                 `json:"user_id"`
	Email                string                `json:"email"`
	Username             string                `json:"username"`
	AffCode              string                `json:"aff_code"`
	AffCodeCustom        bool                  `json:"aff_code_custom"`
	AffRebateRatePercent *float64              `json:"aff_rebate_rate_percent,omitempty"`
	PartnerLevel         string                `json:"partner_level"`
	PartnerTier          *AffiliatePartnerTier `json:"partner_tier,omitempty"`
	AffCount             int                   `json:"aff_count"`
}

type AffiliatePartnerSummary struct {
	UserID                     int64                 `json:"user_id"`
	AffCode                    string                `json:"aff_code"`
	AffCodeCustom              bool                  `json:"aff_code_custom"`
	AffRebateRatePercent       *float64              `json:"aff_rebate_rate_percent,omitempty"`
	PartnerLevel               string                `json:"partner_level"`
	PartnerTier                *AffiliatePartnerTier `json:"partner_tier,omitempty"`
	AffCount                   int                   `json:"aff_count"`
	EffectiveRebateRatePercent float64               `json:"effective_rebate_rate_percent"`
}

type AffiliatePartnerApplicationInput struct {
	RequestedLevel string `json:"requested_level"`
	Source         string `json:"source"`
	Strengths      string `json:"strengths"`
	PortalURL      string `json:"portal_url"`
}

type AffiliatePartnerApplicationReviewInput struct {
	Status       string `json:"status"`
	GrantedLevel string `json:"granted_level,omitempty"`
	ReviewNote   string `json:"review_note,omitempty"`
}

type AffiliatePartnerApplicationFilter struct {
	Search   string
	Status   string
	Page     int
	PageSize int
}

type AffiliatePartnerApplication struct {
	ID             int64                 `json:"id"`
	UserID         int64                 `json:"user_id"`
	Email          string                `json:"email,omitempty"`
	Username       string                `json:"username,omitempty"`
	RequestedLevel string                `json:"requested_level"`
	RequestedTier  *AffiliatePartnerTier `json:"requested_tier,omitempty"`
	CurrentLevel   string                `json:"current_level,omitempty"`
	CurrentTier    *AffiliatePartnerTier `json:"current_tier,omitempty"`
	GrantedLevel   string                `json:"granted_level,omitempty"`
	GrantedTier    *AffiliatePartnerTier `json:"granted_tier,omitempty"`
	Source         string                `json:"source"`
	Strengths      string                `json:"strengths"`
	PortalURL      string                `json:"portal_url"`
	Status         string                `json:"status"`
	ReviewNote     string                `json:"review_note,omitempty"`
	ReviewerID     *int64                `json:"reviewer_id,omitempty"`
	ReviewedAt     *time.Time            `json:"reviewed_at,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

type AffiliateRecordFilter struct {
	Search   string
	Page     int
	PageSize int
	StartAt  *time.Time
	EndAt    *time.Time
	UserID   int64
	SortBy   string
	SortDesc bool
}

type AffiliateSettlementInput struct {
	UserID    int64     `json:"user_id"`
	Amount    float64   `json:"amount"`
	SettledOn time.Time `json:"settled_on"`
	Note      string    `json:"note"`
	CreatedBy int64     `json:"created_by"`
}

type AffiliateUsageFilter struct {
	Search                   string
	Page                     int
	PageSize                 int
	StartAt                  *time.Time
	EndAt                    *time.Time
	Timezone                 string
	InviterID                int64
	InviteeID                int64
	View                     string
	GroupProfitRates         map[int64]float64
	DefaultRebateRatePercent float64
	SortBy                   string
	SortDesc                 bool
	InviterOnly              bool
}

type AffiliateInviteAssignment struct {
	InviterID int64 `json:"inviter_id"`
	InviteeID int64 `json:"invitee_id"`
	Changed   bool  `json:"changed"`
}

type AffiliateInviteRecord struct {
	InviterID       int64     `json:"inviter_id"`
	InviterEmail    string    `json:"inviter_email"`
	InviterUsername string    `json:"inviter_username"`
	InviteeID       int64     `json:"invitee_id"`
	InviteeEmail    string    `json:"invitee_email"`
	InviteeUsername string    `json:"invitee_username"`
	AffCode         string    `json:"aff_code"`
	TotalRebate     float64   `json:"total_rebate"`
	CreatedAt       time.Time `json:"created_at"`
}

type AffiliateUsageDailyRecord struct {
	Date              string                       `json:"date,omitempty"`
	InviterID         int64                        `json:"inviter_id"`
	InviterEmail      string                       `json:"inviter_email"`
	InviterUsername   string                       `json:"inviter_username"`
	InviteeID         int64                        `json:"invitee_id"`
	InviteeEmail      string                       `json:"invitee_email"`
	InviteeUsername   string                       `json:"invitee_username"`
	InviteeCount      int64                        `json:"invitee_count"`
	Requests          int64                        `json:"requests"`
	TotalTokens       int64                        `json:"total_tokens"`
	ActualCost        float64                      `json:"actual_cost"`
	AccountCost       float64                      `json:"account_cost"`
	NetProfit         float64                      `json:"net_profit"`
	RechargeAmount    float64                      `json:"recharge_amount"`
	RebateRatePercent float64                      `json:"rebate_rate_percent"`
	RebateAmount      float64                      `json:"rebate_amount"`
	SettledAmount     float64                      `json:"settled_amount"`
	PendingAmount     float64                      `json:"pending_amount"`
	Unassigned        bool                         `json:"unassigned"`
	ProfitDetails     []AffiliateUsageProfitDetail `json:"profit_details,omitempty"`
	Members           []AffiliateUsageDailyRecord  `json:"members,omitempty"`
}

type AffiliateUsageProfitDetail struct {
	GroupID           int64   `json:"group_id"`
	GroupName         string  `json:"group_name"`
	Model             string  `json:"model"`
	Source            string  `json:"source,omitempty"`
	Requests          int64   `json:"requests"`
	TotalTokens       int64   `json:"total_tokens"`
	ActualCost        float64 `json:"actual_cost"`
	ProfitRatePercent float64 `json:"profit_rate_percent"`
	NetProfit         float64 `json:"net_profit"`
	RebateAmount      float64 `json:"rebate_amount"`
}

type AffiliateUsageSummary struct {
	TotalRequests      int64   `json:"total_requests"`
	TotalTokens        int64   `json:"total_tokens"`
	TotalActualCost    float64 `json:"total_actual_cost"`
	TotalAccountCost   float64 `json:"total_account_cost"`
	TotalNetProfit     float64 `json:"total_net_profit"`
	TotalRecharge      float64 `json:"total_recharge_amount"`
	TotalRebateAmount  float64 `json:"total_rebate_amount"`
	TotalSettledAmount float64 `json:"total_settled_amount"`
	TotalPendingAmount float64 `json:"total_pending_amount"`
}

type AffiliateRebateRecord struct {
	OrderID         int64     `json:"order_id"`
	OutTradeNo      string    `json:"out_trade_no"`
	InviterID       int64     `json:"inviter_id"`
	InviterEmail    string    `json:"inviter_email"`
	InviterUsername string    `json:"inviter_username"`
	InviteeID       int64     `json:"invitee_id"`
	InviteeEmail    string    `json:"invitee_email"`
	InviteeUsername string    `json:"invitee_username"`
	OrderAmount     float64   `json:"order_amount"`
	PayAmount       float64   `json:"pay_amount"`
	RebateAmount    float64   `json:"rebate_amount"`
	PaymentType     string    `json:"payment_type"`
	OrderStatus     string    `json:"order_status"`
	CreatedAt       time.Time `json:"created_at"`
}

type AffiliateTransferRecord struct {
	LedgerID            int64     `json:"ledger_id"`
	UserID              int64     `json:"user_id"`
	UserEmail           string    `json:"user_email"`
	Username            string    `json:"username"`
	Amount              float64   `json:"amount"`
	BalanceAfter        *float64  `json:"balance_after,omitempty"`
	AvailableQuotaAfter *float64  `json:"available_quota_after,omitempty"`
	FrozenQuotaAfter    *float64  `json:"frozen_quota_after,omitempty"`
	HistoryQuotaAfter   *float64  `json:"history_quota_after,omitempty"`
	SnapshotAvailable   bool      `json:"snapshot_available"`
	CurrentBalance      float64   `json:"-"`
	RemainingQuota      float64   `json:"-"`
	FrozenQuota         float64   `json:"-"`
	HistoryQuota        float64   `json:"-"`
	CreatedAt           time.Time `json:"created_at"`
}

type AffiliateSettlementRecord struct {
	ID                int64     `json:"id"`
	UserID            int64     `json:"user_id"`
	UserEmail         string    `json:"user_email"`
	Username          string    `json:"username"`
	Amount            float64   `json:"amount"`
	SettledOn         time.Time `json:"settled_on"`
	Note              string    `json:"note"`
	CreatedBy         *int64    `json:"created_by,omitempty"`
	CreatedByEmail    string    `json:"created_by_email,omitempty"`
	CreatedByUsername string    `json:"created_by_username,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type AffiliateUserOverview struct {
	UserID              int64                 `json:"user_id"`
	Email               string                `json:"email"`
	Username            string                `json:"username"`
	AffCode             string                `json:"aff_code"`
	RebateRatePercent   float64               `json:"rebate_rate_percent"`
	RebateRateCustom    bool                  `json:"-"`
	PartnerLevel        string                `json:"partner_level"`
	PartnerTier         *AffiliatePartnerTier `json:"partner_tier,omitempty"`
	InvitedCount        int                   `json:"invited_count"`
	RebatedInviteeCount int                   `json:"rebated_invitee_count"`
	AvailableQuota      float64               `json:"available_quota"`
	HistoryQuota        float64               `json:"history_quota"`
}

type AffiliateService struct {
	repo                 AffiliateRepository
	settingService       *SettingService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCacheService  *BillingCacheService
}

func NewAffiliateService(repo AffiliateRepository, settingService *SettingService, authCacheInvalidator APIKeyAuthCacheInvalidator, billingCacheService *BillingCacheService) *AffiliateService {
	return &AffiliateService{
		repo:                 repo,
		settingService:       settingService,
		authCacheInvalidator: authCacheInvalidator,
		billingCacheService:  billingCacheService,
	}
}

func (s *AffiliateService) PartnerTiers(ctx context.Context) []AffiliatePartnerTier {
	if s == nil || s.settingService == nil {
		return AffiliatePartnerTiers()
	}
	return s.settingService.GetAffiliatePartnerTiers(ctx)
}

// IsEnabled reports whether the affiliate (邀请返利) feature is turned on.
func (s *AffiliateService) IsEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return AffiliateEnabledDefault
	}
	return s.settingService.IsAffiliateEnabled(ctx)
}

func (s *AffiliateService) EnsureUserAffiliate(ctx context.Context, userID int64) (*AffiliateSummary, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.EnsureUserAffiliate(ctx, userID)
}

func (s *AffiliateService) GetAffiliateDetail(ctx context.Context, userID int64) (*AffiliateDetail, error) {
	// Lazy thaw: move any matured frozen quota to available before reading.
	if s != nil && s.repo != nil {
		// best-effort: thaw failure is non-fatal
		_, _ = s.repo.ThawFrozenQuota(ctx, userID)
	}

	summary, err := s.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return nil, err
	}
	invitees, err := s.listInvitees(ctx, userID)
	if err != nil {
		return nil, err
	}
	effectiveRate := s.resolveRebateRatePercent(ctx, summary)
	partnerLevel := s.resolveEffectivePartnerLevel(ctx, summary)
	partnerTier := s.affiliatePartnerTierPtr(ctx, partnerLevel)
	application, appErr := s.repo.GetLatestPartnerApplication(ctx, userID)
	if appErr != nil && !errors.Is(appErr, ErrAffiliatePartnerApplicationNotFound) {
		return nil, appErr
	}
	s.applyPartnerApplicationTiers(ctx, application)
	return &AffiliateDetail{
		UserID:                     summary.UserID,
		AffCode:                    summary.AffCode,
		InviterID:                  summary.InviterID,
		AffCount:                   summary.AffCount,
		AffQuota:                   summary.AffQuota,
		AffFrozenQuota:             summary.AffFrozenQuota,
		AffHistoryQuota:            summary.AffHistoryQuota,
		PartnerLevel:               partnerLevel,
		PartnerTier:                partnerTier,
		PartnerTiers:               s.PartnerTiers(ctx),
		PartnerApplication:         application,
		EffectiveRebateRatePercent: effectiveRate,
		Invitees:                   invitees,
	}, nil
}

func (s *AffiliateService) BindInviterByCode(ctx context.Context, userID int64, rawCode string) error {
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if code == "" {
		return nil
	}
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	// 总开关关闭时，注册阶段静默忽略 aff 参数（不报错，避免阻断注册流程）
	if !s.IsEnabled(ctx) {
		return nil
	}
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateCodeInvalid
	}

	selfSummary, err := s.repo.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return err
	}
	if selfSummary.InviterID != nil {
		return nil
	}

	inviterSummary, err := s.repo.GetAffiliateByCode(ctx, code)
	if err != nil {
		if errors.Is(err, ErrAffiliateProfileNotFound) {
			return ErrAffiliateCodeInvalid
		}
		return err
	}
	if inviterSummary == nil || inviterSummary.UserID <= 0 || inviterSummary.UserID == userID {
		return ErrAffiliateCodeInvalid
	}

	bound, err := s.repo.BindInviter(ctx, userID, inviterSummary.UserID)
	if err != nil {
		return err
	}
	if !bound {
		return ErrAffiliateAlreadyBound
	}
	_, _, _ = s.promotePartnerLevelForInviteCount(ctx, inviterSummary.UserID)
	return nil
}

func (s *AffiliateService) AccrueInviteRebate(ctx context.Context, inviteeUserID int64, baseRechargeAmount float64) (float64, error) {
	return s.AccrueInviteRebateForOrder(ctx, inviteeUserID, baseRechargeAmount, nil)
}

func (s *AffiliateService) AccrueInviteRebateForOrder(ctx context.Context, inviteeUserID int64, baseRechargeAmount float64, sourceOrderID *int64) (float64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	if inviteeUserID <= 0 || baseRechargeAmount <= 0 || math.IsNaN(baseRechargeAmount) || math.IsInf(baseRechargeAmount, 0) {
		return 0, nil
	}
	// 总开关关闭时，新充值不再产生返利
	if !s.IsEnabled(ctx) {
		return 0, nil
	}

	inviteeSummary, err := s.repo.EnsureUserAffiliate(ctx, inviteeUserID)
	if err != nil {
		return 0, err
	}
	if inviteeSummary.InviterID == nil || *inviteeSummary.InviterID <= 0 {
		return 0, nil
	}

	// 加载邀请人 profile，优先使用专属比例（覆盖全局）
	inviterSummary, err := s.repo.EnsureUserAffiliate(ctx, *inviteeSummary.InviterID)
	if err != nil {
		return 0, err
	}
	if s.isEffectivePartner(ctx, inviterSummary) {
		return 0, nil
	}
	// 有效期检查：超过返利有效期后不再产生返利
	if s.settingService != nil {
		if durationDays := s.settingService.GetAffiliateRebateDurationDays(ctx); durationDays > 0 {
			if time.Now().After(inviteeSummary.CreatedAt.AddDate(0, 0, durationDays)) {
				return 0, nil
			}
		}
	}

	rebateRatePercent := s.resolveRebateRatePercent(ctx, inviterSummary)
	rebate := roundTo(baseRechargeAmount*(rebateRatePercent/100), 8)
	if rebate <= 0 {
		return 0, nil
	}

	// 单人上限检查：精确截断到剩余额度
	if s.settingService != nil {
		if perInviteeCap := s.settingService.GetAffiliateRebatePerInviteeCap(ctx); perInviteeCap > 0 {
			existing, err := s.repo.GetAccruedRebateFromInvitee(ctx, *inviteeSummary.InviterID, inviteeUserID)
			if err != nil {
				return 0, err
			}
			if existing >= perInviteeCap {
				return 0, nil
			}
			if remaining := perInviteeCap - existing; rebate > remaining {
				rebate = roundTo(remaining, 8)
			}
		}
	}

	var freezeHours int
	if s.settingService != nil {
		freezeHours = s.settingService.GetAffiliateRebateFreezeHours(ctx)
	}

	applied, err := s.repo.AccrueQuota(ctx, *inviteeSummary.InviterID, inviteeUserID, rebate, freezeHours, sourceOrderID)
	if err != nil {
		return 0, err
	}
	if !applied {
		return 0, nil
	}
	_, _, _ = s.promotePartnerLevelForInviteCount(ctx, *inviteeSummary.InviterID)
	return rebate, nil
}

// resolveRebateRatePercent returns the inviter's exclusive rate when set,
// otherwise partner level rate, then the global setting value.
func (s *AffiliateService) resolveRebateRatePercent(ctx context.Context, inviter *AffiliateSummary) float64 {
	if inviter != nil && inviter.AffRebateRatePercent != nil {
		v := *inviter.AffRebateRatePercent
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return s.globalRebateRatePercent(ctx)
		}
		return clampAffiliateRebateRate(v)
	}
	if inviter != nil {
		if tier, ok := s.affiliatePartnerTierByLevel(ctx, inviter.PartnerLevel); ok {
			return clampAffiliateRebateRate(tier.RebateRatePercent)
		}
	}
	return s.globalRebateRatePercent(ctx)
}

// globalRebateRatePercent reads the system-wide rebate rate via SettingService,
// returning the documented default when SettingService is unavailable.
func (s *AffiliateService) globalRebateRatePercent(ctx context.Context) float64 {
	if s == nil || s.settingService == nil {
		return AffiliateRebateRateDefault
	}
	return s.settingService.GetAffiliateRebateRatePercent(ctx)
}

func (s *AffiliateService) TransferAffiliateQuota(ctx context.Context, userID int64) (float64, float64, error) {
	if s == nil || s.repo == nil {
		return 0, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	summary, err := s.repo.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	if s.isEffectivePartner(ctx, summary) {
		return 0, 0, ErrAffiliatePartnerTransferUnsupported
	}

	transferred, balance, err := s.repo.TransferQuotaToBalance(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	if transferred > 0 {
		s.invalidateAffiliateCaches(ctx, userID)
	}
	return transferred, balance, nil
}

func (s *AffiliateService) resolveEffectivePartnerLevel(ctx context.Context, summary *AffiliateSummary) string {
	if summary == nil {
		return AffiliatePartnerLevelNone
	}
	level := NormalizeAffiliatePartnerLevel(summary.PartnerLevel)
	if AffiliatePartnerLevelRank(level) > 0 {
		return level
	}
	if summary.AffRebateRatePercent != nil {
		if tier, ok := s.affiliatePartnerTierByRebateRatePercent(ctx, clampAffiliateRebateRate(*summary.AffRebateRatePercent)); ok {
			return tier.Level
		}
	}
	return AffiliatePartnerLevelNone
}

func (s *AffiliateService) isEffectivePartner(ctx context.Context, summary *AffiliateSummary) bool {
	return AffiliatePartnerLevelRank(s.resolveEffectivePartnerLevel(ctx, summary)) > 0
}

func (s *AffiliateService) listInvitees(ctx context.Context, inviterID int64) ([]AffiliateInvitee, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	invitees, err := s.repo.ListInvitees(ctx, inviterID, affiliateInviteesLimit)
	if err != nil {
		return nil, err
	}
	for i := range invitees {
		invitees[i].Email = maskEmail(invitees[i].Email)
	}
	return invitees, nil
}

func roundTo(v float64, scale int) float64 {
	factor := math.Pow10(scale)
	return math.Round(v*factor) / factor
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at <= 0 || at >= len(email)-1 {
		return "***"
	}

	local := email[:at]
	domain := email[at+1:]
	dot := strings.LastIndex(domain, ".")

	maskedLocal := maskSegment(local)
	if dot <= 0 || dot >= len(domain)-1 {
		return maskedLocal + "@" + maskSegment(domain)
	}

	domainName := domain[:dot]
	tld := domain[dot:]
	return maskedLocal + "@" + maskSegment(domainName) + tld
}

func maskSegment(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return "***"
	}
	if len(r) == 1 {
		return string(r[0]) + "***"
	}
	return string(r[0]) + "***"
}

func (s *AffiliateService) invalidateAffiliateCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		if err := s.billingCacheService.InvalidateUserBalance(ctx, userID); err != nil {
			logger.LegacyPrintf("service.affiliate", "[Affiliate] Failed to invalidate billing cache for user %d: %v", userID, err)
		}
	}
}

// =========================
// Admin: 专属配置管理
// =========================

// validateExclusiveRate ensures a per-user override is finite and within
// [Min, Max]. nil is always valid (means "clear / fall back to global").
func validateExclusiveRate(ratePercent *float64) error {
	if ratePercent == nil {
		return nil
	}
	v := *ratePercent
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return infraerrors.BadRequest("INVALID_RATE", "invalid rebate rate")
	}
	if v < AffiliateRebateRateMin || v > AffiliateRebateRateMax {
		return infraerrors.BadRequest("INVALID_RATE", "rebate rate out of range")
	}
	return nil
}

// AdminUpdateUserAffCode 管理员改写用户的邀请码（专属邀请码）。
func (s *AffiliateService) AdminUpdateUserAffCode(ctx context.Context, userID int64, rawCode string) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateCodeInvalid
	}
	return s.repo.UpdateUserAffCode(ctx, userID, code)
}

// AdminResetUserAffCode 重置用户邀请码为系统随机码。
func (s *AffiliateService) AdminResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.repo == nil {
		return "", infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ResetUserAffCode(ctx, userID)
}

// AdminSetUserRebateRate 设置/清除用户专属返利比例。ratePercent==nil 表示清除。
func (s *AffiliateService) AdminSetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if err := validateExclusiveRate(ratePercent); err != nil {
		return err
	}
	return s.repo.SetUserRebateRate(ctx, userID, ratePercent)
}

// AdminBatchSetUserRebateRate 批量设置/清除用户专属返利比例。
func (s *AffiliateService) AdminBatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if err := validateExclusiveRate(ratePercent); err != nil {
		return err
	}
	cleaned := make([]int64, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid > 0 {
			cleaned = append(cleaned, uid)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return s.repo.BatchSetUserRebateRate(ctx, cleaned, ratePercent)
}

func (s *AffiliateService) AdminSetUserPartnerLevel(ctx context.Context, userID int64, level string) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	level = NormalizeAffiliatePartnerLevel(level)
	if AffiliatePartnerLevelRank(level) < 0 {
		return ErrAffiliatePartnerLevelInvalid
	}
	return s.repo.SetUserPartnerLevel(ctx, userID, level)
}

func (s *AffiliateService) AdminGetPartnerSummaries(ctx context.Context, userIDs []int64) (map[int64]AffiliatePartnerSummary, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	cleaned := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, uid := range userIDs {
		if uid <= 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		cleaned = append(cleaned, uid)
	}
	if len(cleaned) == 0 {
		return map[int64]AffiliatePartnerSummary{}, nil
	}
	summaries, err := s.repo.GetPartnerSummariesByUserIDs(ctx, cleaned)
	if err != nil {
		return nil, err
	}
	for uid, summary := range summaries {
		summary.EffectiveRebateRatePercent = s.resolveRebateRatePercent(ctx, &AffiliateSummary{
			UserID:               summary.UserID,
			AffCode:              summary.AffCode,
			AffCodeCustom:        summary.AffCodeCustom,
			AffRebateRatePercent: summary.AffRebateRatePercent,
			PartnerLevel:         summary.PartnerLevel,
			AffCount:             summary.AffCount,
		})
		summary.PartnerLevel = s.resolveEffectivePartnerLevel(ctx, &AffiliateSummary{
			AffRebateRatePercent: summary.AffRebateRatePercent,
			PartnerLevel:         summary.PartnerLevel,
		})
		summary.PartnerTier = s.affiliatePartnerTierPtr(ctx, summary.PartnerLevel)
		summaries[uid] = summary
	}
	defaultRate := s.globalRebateRatePercent(ctx)
	for _, uid := range cleaned {
		if _, ok := summaries[uid]; ok {
			continue
		}
		summaries[uid] = AffiliatePartnerSummary{
			UserID:                     uid,
			PartnerLevel:               AffiliatePartnerLevelNone,
			EffectiveRebateRatePercent: defaultRate,
		}
	}
	return summaries, nil
}

func (s *AffiliateService) ApplyForPartner(ctx context.Context, userID int64, input AffiliatePartnerApplicationInput) (*AffiliatePartnerApplication, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if _, err := s.EnsureUserAffiliate(ctx, userID); err != nil {
		return nil, err
	}
	input, err := normalizePartnerApplicationInput(input)
	if err != nil {
		return nil, err
	}
	application, err := s.repo.CreatePartnerApplication(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	s.applyPartnerApplicationTiers(ctx, application)
	return application, nil
}

func (s *AffiliateService) AdminListPartnerApplications(ctx context.Context, filter AffiliatePartnerApplicationFilter) ([]AffiliatePartnerApplication, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.Status = normalizePartnerApplicationStatus(filter.Status)
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	items, total, err := s.repo.ListPartnerApplications(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		s.applyPartnerApplicationTiers(ctx, &items[i])
	}
	return items, total, nil
}

func (s *AffiliateService) AdminReviewPartnerApplication(ctx context.Context, applicationID, reviewerID int64, input AffiliatePartnerApplicationReviewInput) (*AffiliatePartnerApplication, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if applicationID <= 0 {
		return nil, ErrAffiliatePartnerApplicationNotFound
	}
	input.Status = normalizePartnerApplicationStatus(input.Status)
	if input.Status != AffiliatePartnerApplicationStatusApproved && input.Status != AffiliatePartnerApplicationStatusRejected {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "invalid application status")
	}
	input.ReviewNote = strings.TrimSpace(input.ReviewNote)
	if input.Status == AffiliatePartnerApplicationStatusApproved {
		input.GrantedLevel = NormalizeAffiliatePartnerLevel(input.GrantedLevel)
		if AffiliatePartnerLevelRank(input.GrantedLevel) <= 0 {
			return nil, ErrAffiliatePartnerLevelInvalid
		}
	} else {
		input.GrantedLevel = AffiliatePartnerLevelNone
	}
	application, err := s.repo.ReviewPartnerApplication(ctx, applicationID, reviewerID, input)
	if err != nil {
		return nil, err
	}
	s.applyPartnerApplicationTiers(ctx, application)
	return application, nil
}

// AdminListCustomUsers 列出有专属配置的用户。
func (s *AffiliateService) AdminListCustomUsers(ctx context.Context, filter AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	entries, total, err := s.repo.ListUsersWithCustomSettings(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	for i := range entries {
		entries[i].PartnerLevel = NormalizeAffiliatePartnerLevel(entries[i].PartnerLevel)
		entries[i].PartnerTier = s.affiliatePartnerTierPtr(ctx, entries[i].PartnerLevel)
	}
	return entries, total, nil
}

func (s *AffiliateService) AdminListInviteRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateInviteRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

func (s *AffiliateService) AdminAssignInviter(ctx context.Context, inviteeID, inviterID int64) (*AffiliateInviteAssignment, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if inviteeID <= 0 || inviterID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if inviteeID == inviterID {
		return nil, ErrAffiliateSelfBinding
	}
	assignment, err := s.repo.AdminAssignInviter(ctx, inviteeID, inviterID)
	if err != nil {
		return nil, err
	}
	if assignment != nil && assignment.Changed {
		_, _, _ = s.promotePartnerLevelForInviteCount(ctx, inviterID)
	}
	return assignment, nil
}

func (s *AffiliateService) AdminListUsageDailyRecords(ctx context.Context, filter AffiliateUsageFilter) ([]AffiliateUsageDailyRecord, *AffiliateUsageSummary, int64, error) {
	if s == nil || s.repo == nil {
		return nil, nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	filter = normalizeAffiliateUsageFilter(filter)
	if s.settingService != nil {
		filter.DefaultRebateRatePercent = s.settingService.GetAffiliateRebateRatePercent(ctx)
		filter.GroupProfitRates = affiliateGroupProfitRatesByID(s.settingService.GetAffiliateGroupProfitRates(ctx))
	} else {
		filter.DefaultRebateRatePercent = AffiliateUsageCommissionRateDefault
	}
	return s.repo.ListAffiliateUsageDailyRecords(ctx, filter)
}

func (s *AffiliateService) ListMyUsageDailyRecords(ctx context.Context, inviterID int64, filter AffiliateUsageFilter) ([]AffiliateUsageDailyRecord, *AffiliateUsageSummary, int64, error) {
	if inviterID <= 0 {
		return nil, nil, 0, infraerrors.Unauthorized("UNAUTHORIZED", "user not authenticated")
	}
	filter.InviterID = inviterID
	filter.InviterOnly = true
	if filter.View == "" {
		filter.View = "users"
	}
	return s.AdminListUsageDailyRecords(ctx, filter)
}

func affiliateGroupProfitRatesByID(input map[string]float64) map[int64]float64 {
	if len(input) == 0 {
		return nil
	}
	result := make(map[int64]float64, len(input))
	for rawID, rate := range input {
		groupID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || groupID <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			continue
		}
		if rate > 100 {
			rate = 100
		}
		result[groupID] = rate
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (s *AffiliateService) AdminListRebateRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateRebateRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

func (s *AffiliateService) AdminListTransferRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateTransferRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateTransferRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

func (s *AffiliateService) AdminCreateSettlement(ctx context.Context, input AffiliateSettlementInput) (*AffiliateSettlementRecord, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if input.UserID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if agencyRepo, ok := s.repo.(SecondLevelAgencyRepository); ok {
		isSubagent, err := agencyRepo.IsSecondLevelAgentUser(ctx, input.UserID)
		if err != nil {
			return nil, err
		}
		if isSubagent {
			return nil, ErrAffiliatePartnerTransferUnsupported
		}
	}
	if math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) || input.Amount <= 0 {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "settlement amount must be greater than 0")
	}
	if input.SettledOn.IsZero() {
		return nil, infraerrors.BadRequest("INVALID_SETTLED_ON", "settlement date is required")
	}
	input.Amount = roundTo(input.Amount, 8)
	input.Note = strings.TrimSpace(input.Note)
	if len(input.Note) > 1000 {
		input.Note = input.Note[:1000]
	}
	return s.repo.CreateAffiliateSettlement(ctx, input)
}

func (s *AffiliateService) AdminListSettlementRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateSettlementRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.ListAffiliateSettlementRecords(ctx, normalizeAffiliateRecordFilter(filter))
}

func (s *AffiliateService) ListMySettlementRecords(ctx context.Context, userID int64, filter AffiliateRecordFilter) ([]AffiliateSettlementRecord, int64, error) {
	if userID <= 0 {
		return nil, 0, infraerrors.Unauthorized("UNAUTHORIZED", "user not authenticated")
	}
	filter.UserID = userID
	return s.AdminListSettlementRecords(ctx, filter)
}

func (s *AffiliateService) AdminGetUserOverview(ctx context.Context, userID int64) (*AffiliateUserOverview, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER", "invalid user")
	}
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	overview, err := s.repo.GetAffiliateUserOverview(ctx, userID)
	if err != nil {
		return nil, err
	}
	if overview != nil {
		if !overview.RebateRateCustom {
			if tier, ok := s.affiliatePartnerTierByLevel(ctx, overview.PartnerLevel); ok {
				overview.RebateRatePercent = tier.RebateRatePercent
			} else {
				overview.RebateRatePercent = s.globalRebateRatePercent(ctx)
			}
		}
		overview.PartnerLevel = NormalizeAffiliatePartnerLevel(overview.PartnerLevel)
		overview.PartnerTier = s.affiliatePartnerTierPtr(ctx, overview.PartnerLevel)
		overview.RebateRatePercent = clampAffiliateRebateRate(overview.RebateRatePercent)
	}
	return overview, nil
}

func (s *AffiliateService) affiliatePartnerTierByLevel(ctx context.Context, level string) (AffiliatePartnerTier, bool) {
	return AffiliatePartnerTierByLevelFrom(s.PartnerTiers(ctx), level)
}

func (s *AffiliateService) affiliatePartnerTierByRebateRatePercent(ctx context.Context, ratePercent float64) (AffiliatePartnerTier, bool) {
	return AffiliatePartnerTierByRebateRatePercentFrom(s.PartnerTiers(ctx), ratePercent)
}

func (s *AffiliateService) affiliatePartnerTierPtr(ctx context.Context, level string) *AffiliatePartnerTier {
	tier, ok := s.affiliatePartnerTierByLevel(ctx, level)
	if !ok {
		return nil
	}
	return &tier
}

func (s *AffiliateService) promotePartnerLevelForInviteCount(ctx context.Context, userID int64) (*AffiliatePartnerTier, bool, error) {
	if s == nil || s.repo == nil {
		return nil, false, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	return s.repo.PromotePartnerLevelForInviteCount(ctx, userID, s.PartnerTiers(ctx))
}

func (s *AffiliateService) applyPartnerApplicationTiers(ctx context.Context, app *AffiliatePartnerApplication) {
	if app == nil {
		return
	}
	app.RequestedTier = s.affiliatePartnerTierPtr(ctx, app.RequestedLevel)
	app.CurrentTier = s.affiliatePartnerTierPtr(ctx, app.CurrentLevel)
	if app.GrantedLevel != "" {
		app.GrantedTier = s.affiliatePartnerTierPtr(ctx, app.GrantedLevel)
	}
}

func normalizePartnerApplicationInput(input AffiliatePartnerApplicationInput) (AffiliatePartnerApplicationInput, error) {
	input.RequestedLevel = AffiliatePartnerLevelNone
	input.Source = strings.TrimSpace(input.Source)
	input.Strengths = strings.TrimSpace(input.Strengths)
	input.PortalURL = strings.TrimSpace(input.PortalURL)
	if input.Source == "" {
		return input, infraerrors.BadRequest("INVALID_SOURCE", "source is required")
	}
	if input.Strengths == "" {
		return input, infraerrors.BadRequest("INVALID_STRENGTHS", "strengths are required")
	}
	if input.PortalURL == "" {
		return input, infraerrors.BadRequest("INVALID_PORTAL_URL", "portal url is required")
	}
	parsed, err := url.ParseRequestURI(input.PortalURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return input, infraerrors.BadRequest("INVALID_PORTAL_URL", "invalid portal url")
	}
	if len(input.Source) > 128 {
		input.Source = input.Source[:128]
	}
	if len(input.PortalURL) > 512 {
		return input, infraerrors.BadRequest("INVALID_PORTAL_URL", "portal url is too long")
	}
	return input, nil
}

func normalizePartnerApplicationStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "", "all":
		return ""
	case AffiliatePartnerApplicationStatusPending, AffiliatePartnerApplicationStatusApproved, AffiliatePartnerApplicationStatusRejected:
		return status
	default:
		return status
	}
}

func normalizeAffiliateRecordFilter(filter AffiliateRecordFilter) AffiliateRecordFilter {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.SortBy = strings.TrimSpace(filter.SortBy)
	return filter
}

func normalizeAffiliateUsageFilter(filter AffiliateUsageFilter) AffiliateUsageFilter {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.SortBy = strings.TrimSpace(filter.SortBy)
	filter.Timezone = strings.TrimSpace(filter.Timezone)
	filter.View = strings.TrimSpace(filter.View)
	if filter.View != "groups" {
		filter.View = "users"
	}
	return filter
}
