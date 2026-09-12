package service

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrSecondLevelAgencyDisabled = infraerrors.Forbidden("SECOND_LEVEL_AGENCY_DISABLED", "second-level agency is not enabled")
	ErrSecondLevelAgencyInvalid  = infraerrors.BadRequest("SECOND_LEVEL_AGENCY_INVALID", "invalid second-level agency")
)

type SecondLevelAgencyCapability struct {
	RootPartnerUserID    int64      `json:"root_partner_user_id"`
	Email                string     `json:"email,omitempty"`
	Username             string     `json:"username,omitempty"`
	PartnerLevel         string     `json:"partner_level,omitempty"`
	AffCode              string     `json:"aff_code,omitempty"`
	AffRebateRatePercent *float64   `json:"aff_rebate_rate_percent,omitempty"`
	Configured           bool       `json:"configured"`
	Enabled              bool       `json:"enabled"`
	DefaultRate          float64    `json:"default_subagent_rate"`
	MaxRate              float64    `json:"max_subagent_rate"`
	GrantedBy            *int64     `json:"granted_by,omitempty"`
	GrantedAt            *time.Time `json:"granted_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
}

type SecondLevelAgent struct {
	ID                int64     `json:"id"`
	RootPartnerUserID int64     `json:"root_partner_user_id"`
	SubagentUserID    int64     `json:"subagent_user_id"`
	Email             string    `json:"email"`
	Username          string    `json:"username"`
	AffCode           string    `json:"aff_code"`
	Status            string    `json:"status"`
	CommissionRate    float64   `json:"commission_rate"`
	InvitedCount      int       `json:"invited_count"`
	CreatedAt         time.Time `json:"created_at"`
}

type SecondLevelAgencyCandidate struct {
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

type SecondLevelAgencyRepository interface {
	GetAgencyCapability(context.Context, int64) (*SecondLevelAgencyCapability, error)
	ListAgencyCapabilities(context.Context) ([]SecondLevelAgencyCapability, error)
	SetAgencyCapability(context.Context, SecondLevelAgencyCapability) error
	ListSecondLevelAgents(context.Context, int64) ([]SecondLevelAgent, error)
	ListSecondLevelAgencyCandidates(context.Context, int64, string) ([]SecondLevelAgencyCandidate, error)
	CreateSecondLevelAgent(context.Context, SecondLevelAgent) (*SecondLevelAgent, error)
	SetSecondLevelAgentStatus(context.Context, int64, int64, string) error
	SetSecondLevelAgentCommissionRate(context.Context, int64, int64, float64) error
	IsSecondLevelAgentUser(context.Context, int64) (bool, error)
}

func (s *AffiliateService) secondLevelRepo() (SecondLevelAgencyRepository, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	repo, ok := s.repo.(SecondLevelAgencyRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("SECOND_LEVEL_AGENCY_UNAVAILABLE", "second-level agency storage unavailable")
	}
	return repo, nil
}

func (s *AffiliateService) GetSecondLevelAgencyCapability(ctx context.Context, userID int64) (*SecondLevelAgencyCapability, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return nil, err
	}
	capability, err := repo.GetAgencyCapability(ctx, userID)
	if err != nil {
		return nil, err
	}
	if capability == nil {
		return &SecondLevelAgencyCapability{RootPartnerUserID: userID, Configured: false, DefaultRate: 30, MaxRate: 50}, nil
	}
	capability.Configured = true
	return capability, nil
}

func (s *AffiliateService) ListSecondLevelAgencyCapabilities(ctx context.Context) ([]SecondLevelAgencyCapability, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListAgencyCapabilities(ctx)
}

func (s *AffiliateService) SetSecondLevelAgencyCapability(ctx context.Context, rootID, adminID int64, enabled bool, defaultRate, maxRate float64) error {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return err
	}
	if rootID <= 0 || adminID <= 0 || defaultRate < 0 || maxRate < defaultRate || maxRate > 100 {
		return ErrSecondLevelAgencyInvalid
	}
	return repo.SetAgencyCapability(ctx, SecondLevelAgencyCapability{RootPartnerUserID: rootID, Enabled: enabled, DefaultRate: defaultRate, MaxRate: maxRate, GrantedBy: &adminID})
}

func (s *AffiliateService) ListSecondLevelAgents(ctx context.Context, rootID int64) ([]SecondLevelAgent, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return nil, err
	}
	if err := s.requireFirstLevelPartner(ctx, rootID); err != nil {
		return nil, err
	}
	capability, err := s.GetSecondLevelAgencyCapability(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if capability == nil || !capability.Enabled {
		return nil, ErrSecondLevelAgencyDisabled
	}
	return repo.ListSecondLevelAgents(ctx, rootID)
}

func (s *AffiliateService) ListSecondLevelAgencyCandidates(ctx context.Context, rootID int64, search string) ([]SecondLevelAgencyCandidate, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return nil, err
	}
	if err := s.requireFirstLevelPartner(ctx, rootID); err != nil {
		return nil, err
	}
	capability, err := s.GetSecondLevelAgencyCapability(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if capability == nil || !capability.Enabled {
		return nil, ErrSecondLevelAgencyDisabled
	}
	return repo.ListSecondLevelAgencyCandidates(ctx, rootID, strings.TrimSpace(search))
}

func (s *AffiliateService) CreateSecondLevelAgent(ctx context.Context, rootID int64, agent SecondLevelAgent) (*SecondLevelAgent, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return nil, err
	}
	if err := s.requireFirstLevelPartner(ctx, rootID); err != nil {
		return nil, err
	}
	capability, err := s.GetSecondLevelAgencyCapability(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if capability == nil || !capability.Enabled {
		return nil, ErrSecondLevelAgencyDisabled
	}
	if agent.SubagentUserID <= 0 {
		return nil, ErrSecondLevelAgencyInvalid
	}
	if agent.CommissionRate <= 0 {
		agent.CommissionRate = capability.DefaultRate
	}
	if agent.CommissionRate > capability.MaxRate {
		return nil, ErrSecondLevelAgencyInvalid
	}
	if strings.TrimSpace(agent.AffCode) != "" {
		agent.AffCode = strings.ToUpper(strings.TrimSpace(agent.AffCode))
	}
	if len(agent.AffCode) > AffiliateCodeMaxLength {
		return nil, ErrSecondLevelAgencyInvalid
	}
	agent.RootPartnerUserID = rootID
	return repo.CreateSecondLevelAgent(ctx, agent)
}

func (s *AffiliateService) SetSecondLevelAgentStatus(ctx context.Context, rootID, agentID int64, status string) error {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return err
	}
	if err := s.requireFirstLevelPartner(ctx, rootID); err != nil {
		return err
	}
	capability, err := s.GetSecondLevelAgencyCapability(ctx, rootID)
	if err != nil {
		return err
	}
	if capability == nil || !capability.Enabled {
		return ErrSecondLevelAgencyDisabled
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "disabled" {
		return ErrSecondLevelAgencyInvalid
	}
	return repo.SetSecondLevelAgentStatus(ctx, rootID, agentID, status)
}

func (s *AffiliateService) SetSecondLevelAgentCommissionRate(ctx context.Context, rootID, agentID int64, rate float64) error {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return err
	}
	if err := s.requireFirstLevelPartner(ctx, rootID); err != nil {
		return err
	}
	capability, err := s.GetSecondLevelAgencyCapability(ctx, rootID)
	if err != nil {
		return err
	}
	if capability == nil || !capability.Enabled || rate < 0 || rate > capability.MaxRate {
		return ErrSecondLevelAgencyInvalid
	}
	return repo.SetSecondLevelAgentCommissionRate(ctx, rootID, agentID, rate)
}

func (s *AffiliateService) IsSecondLevelAgentUser(ctx context.Context, userID int64) (bool, error) {
	repo, err := s.secondLevelRepo()
	if err != nil {
		return false, err
	}
	return repo.IsSecondLevelAgentUser(ctx, userID)
}

func (s *AffiliateService) secondLevelAgentOwned(ctx context.Context, rootID, agentID int64) (bool, error) {
	agents, err := s.ListSecondLevelAgents(ctx, rootID)
	if err != nil {
		return false, err
	}
	for _, agent := range agents {
		if agent.ID == agentID {
			return true, nil
		}
	}
	return false, ErrSecondLevelAgencyInvalid
}

func (s *AffiliateService) ListSecondLevelUsage(ctx context.Context, rootID, agentID int64, filter AffiliateUsageFilter) ([]AffiliateUsageDailyRecord, *AffiliateUsageSummary, int64, error) {
	owned, err := s.secondLevelAgentOwned(ctx, rootID, agentID)
	if err != nil {
		return nil, nil, 0, err
	}
	if !owned {
		return nil, nil, 0, ErrSecondLevelAgencyInvalid
	}
	filter.InviterID = 0
	filter.InviterOnly = false
	// Existing usage SQL is inviter-scoped; the subagent's user id is the
	// direct inviter for all of its downstream users.
	agents, err := s.ListSecondLevelAgents(ctx, rootID)
	if err != nil {
		return nil, nil, 0, err
	}
	for _, agent := range agents {
		if agent.ID == agentID {
			filter.InviterID = agent.SubagentUserID
			break
		}
	}
	if filter.InviterID <= 0 {
		return nil, nil, 0, ErrSecondLevelAgencyInvalid
	}
	filter.InviterOnly = true
	return s.AdminListUsageDailyRecords(ctx, filter)
}

func (s *AffiliateService) ListSecondLevelRebates(ctx context.Context, rootID, agentID int64, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error) {
	agents, err := s.ListSecondLevelAgents(ctx, rootID)
	if err != nil {
		return nil, 0, err
	}
	var subagentID int64
	for _, agent := range agents {
		if agent.ID == agentID {
			subagentID = agent.SubagentUserID
			break
		}
	}
	if subagentID <= 0 {
		return nil, 0, ErrSecondLevelAgencyInvalid
	}
	filter.UserID = subagentID
	return s.AdminListRebateRecords(ctx, filter)
}

func (s *AffiliateService) requireFirstLevelPartner(ctx context.Context, userID int64) error {
	summary, err := s.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return err
	}
	if summary == nil || !isFirstLevelPartner(summary) {
		return ErrSecondLevelAgencyDisabled
	}
	return nil
}

// isFirstLevelPartner is the eligibility rule for first-level agency features.
// An administrator-assigned special rebate is an explicit first-level partner
// designation even when it does not match one of the named partner tiers.
func isFirstLevelPartner(summary *AffiliateSummary) bool {
	if summary == nil {
		return false
	}
	return AffiliatePartnerLevelRank(summary.PartnerLevel) > 0 || summary.AffRebateRatePercent != nil
}

func IsSecondLevelAgencyError(err error) bool { return errors.Is(err, ErrSecondLevelAgencyDisabled) }
