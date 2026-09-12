package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const (
	affiliateCodeLength      = 12
	affiliateCodeMaxAttempts = 12
)

var affiliateCodeCharset = []byte("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")

const listAgencyCapabilitiesSQL = `
SELECT u.id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       COALESCE(NULLIF(ua.partner_level, ''), 'none'),
       COALESCE(ua.aff_code, ''),
       ua.aff_rebate_rate_percent,
       c.root_partner_user_id IS NOT NULL,
       COALESCE(c.enabled, FALSE),
       COALESCE(c.default_subagent_rate, 30)::double precision,
       COALESCE(c.max_subagent_rate, 50)::double precision,
       c.granted_by,
       c.granted_at,
       c.revoked_at
FROM users u
JOIN user_affiliates ua ON ua.user_id = u.id
LEFT JOIN affiliate_agency_capabilities c ON c.root_partner_user_id = u.id
WHERE u.deleted_at IS NULL
  AND u.status = 'active'
  AND (
      COALESCE(NULLIF(ua.partner_level, ''), 'none') <> 'none'
      OR ua.aff_rebate_rate_percent IS NOT NULL
  )
ORDER BY c.updated_at DESC NULLS LAST, ua.updated_at DESC, u.id DESC`

const affiliateUsageExchangeRateCTE = `exchange_rate AS (
    SELECT COALESCE(
        NULLIF((
            SELECT CASE
                WHEN value ~ '^[0-9]+(\.[0-9]+)?$' THEN value::double precision
                ELSE NULL
            END
            FROM settings
            WHERE key = 'USDT_CNY_EXCHANGE_RATE'
            LIMIT 1
        ), 0),
        7
    )::double precision AS usd_cny_rate
)`

const affiliateEligiblePayAmountSQL = `(po.pay_amount / NULLIF(1 + (COALESCE(po.fee_rate, 0) / 100), 0))`

const affiliateUsageRechargeAmountSQL = `CASE
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('usdt', 'infini')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('usdt', 'infini')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'USDT'
               THEN ` + affiliateEligiblePayAmountSQL + `
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'CNY'
               THEN ` + affiliateEligiblePayAmountSQL + ` / er.usd_cny_rate
               ELSE 0
           END`

const affiliateUsageRechargeAmountCNYSQL = `CASE
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('usdt', 'infini')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('usdt', 'infini')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'USDT'
               THEN ` + affiliateEligiblePayAmountSQL + ` * er.usd_cny_rate
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'CNY'
               THEN ` + affiliateEligiblePayAmountSQL + `
               ELSE 0
           END`

const affiliatePartnerRateSQL = `COALESCE(
    inviter_aff.aff_rebate_rate_percent,
    CASE COALESCE(inviter_aff.partner_level, 'none')
        WHEN 'spark' THEN 40
        WHEN 'voyage' THEN 50
        WHEN 'summit' THEN 60
        WHEN 'cocreate' THEN 70
        ELSE %[1]s
    END
)`

const affiliateUserOverviewSQL = `
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.aff_code,
       COALESCE(ua.aff_rebate_rate_percent, 0)::double precision,
       (ua.aff_rebate_rate_percent IS NOT NULL) AS has_custom_rate,
       COALESCE(ua.partner_level, 'none'),
       ua.aff_count,
       COALESCE(rebated.rebated_invitee_count, 0),
       (ua.aff_quota + COALESCE(matured.matured_frozen_quota, 0))::double precision,
       ua.aff_history_quota::double precision
FROM user_affiliates ua
JOIN users u ON u.id = ua.user_id
LEFT JOIN (
    SELECT user_id, COUNT(DISTINCT source_user_id)::integer AS rebated_invitee_count
    FROM user_affiliate_ledger
    WHERE action = 'accrue' AND source_user_id IS NOT NULL
    GROUP BY user_id
) rebated ON rebated.user_id = ua.user_id
LEFT JOIN (
    SELECT user_id, COALESCE(SUM(amount), 0)::double precision AS matured_frozen_quota
    FROM user_affiliate_ledger
    WHERE action = 'accrue' AND frozen_until IS NOT NULL AND frozen_until <= NOW()
    GROUP BY user_id
) matured ON matured.user_id = ua.user_id
WHERE ua.user_id = $1
LIMIT 1`

type affiliateQueryExecer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type affiliateRepository struct {
	client *dbent.Client
}

func NewAffiliateRepository(client *dbent.Client, _ *sql.DB) service.AffiliateRepository {
	return &affiliateRepository{client: client}
}

func (r *affiliateRepository) GetAgencyCapability(ctx context.Context, rootID int64) (*service.SecondLevelAgencyCapability, error) {
	rows, err := r.client.QueryContext(ctx, `SELECT root_partner_user_id, enabled, default_subagent_rate::double precision, max_subagent_rate::double precision, granted_by, granted_at, revoked_at FROM affiliate_agency_capabilities WHERE root_partner_user_id = $1`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var out service.SecondLevelAgencyCapability
	if err := rows.Scan(&out.RootPartnerUserID, &out.Enabled, &out.DefaultRate, &out.MaxRate, &out.GrantedBy, &out.GrantedAt, &out.RevokedAt); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *affiliateRepository) ListAgencyCapabilities(ctx context.Context) ([]service.SecondLevelAgencyCapability, error) {
	rows, err := r.client.QueryContext(ctx, listAgencyCapabilitiesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.SecondLevelAgencyCapability, 0)
	for rows.Next() {
		var item service.SecondLevelAgencyCapability
		var rebate sql.NullFloat64
		if err := rows.Scan(
			&item.RootPartnerUserID,
			&item.Email,
			&item.Username,
			&item.PartnerLevel,
			&item.AffCode,
			&rebate,
			&item.Configured,
			&item.Enabled,
			&item.DefaultRate,
			&item.MaxRate,
			&item.GrantedBy,
			&item.GrantedAt,
			&item.RevokedAt,
		); err != nil {
			return nil, err
		}
		if rebate.Valid {
			rate := rebate.Float64
			item.AffRebateRatePercent = &rate
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *affiliateRepository) SetAgencyCapability(ctx context.Context, input service.SecondLevelAgencyCapability) error {
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := txClient.ExecContext(txCtx, `INSERT INTO affiliate_agency_capabilities (root_partner_user_id, enabled, default_subagent_rate, max_subagent_rate, granted_by, granted_at, revoked_at, revoke_reason, updated_at) VALUES ($1,$2,$3,$4,$5,CASE WHEN $2 THEN NOW() ELSE NULL END,CASE WHEN $2 THEN NULL ELSE NOW() END,CASE WHEN $2 THEN '' ELSE 'revoked by administrator' END,NOW()) ON CONFLICT (root_partner_user_id) DO UPDATE SET enabled=EXCLUDED.enabled, default_subagent_rate=EXCLUDED.default_subagent_rate, max_subagent_rate=EXCLUDED.max_subagent_rate, granted_by=EXCLUDED.granted_by, granted_at=EXCLUDED.granted_at, revoked_at=EXCLUDED.revoked_at, revoke_reason=EXCLUDED.revoke_reason, updated_at=NOW()`, input.RootPartnerUserID, input.Enabled, input.DefaultRate, input.MaxRate, input.GrantedBy); err != nil {
			return err
		}
		return syncSecondLevelAgencyMenuPermission(txCtx, txClient, input.RootPartnerUserID, input.Enabled)
	})
}

// syncSecondLevelAgencyMenuPermission keeps the user-facing route permission
// in lockstep with the administrator's agency capability toggle. This is
// intentionally persisted beside the capability so generic user updates
// cannot leave the two settings out of sync.
func syncSecondLevelAgencyMenuPermission(ctx context.Context, client *dbent.Client, userID int64, enabled bool) error {
	rows, err := client.QueryContext(ctx, `SELECT role, admin_menu_permissions FROM users WHERE id = $1 FOR UPDATE`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return service.ErrUserNotFound
	}
	var role, raw string
	if err := rows.Scan(&role, &raw); err != nil {
		return err
	}
	var items []string
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &items)
	}
	items = service.NormalizeAdminMenuPermissions(items)
	filtered := make([]string, 0, len(items)+1)
	for _, item := range items {
		if item != "second_level_agency" {
			filtered = append(filtered, item)
		}
	}
	if enabled && role == service.RoleUser {
		filtered = append(filtered, "second_level_agency")
	}
	encoded, err := json.Marshal(service.NormalizeAdminMenuPermissions(filtered))
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `UPDATE users SET admin_menu_permissions = $1, updated_at = NOW() WHERE id = $2`, string(encoded), userID)
	return err
}

func (r *affiliateRepository) ListSecondLevelAgents(ctx context.Context, rootID int64) ([]service.SecondLevelAgent, error) {
	rows, err := r.client.QueryContext(ctx, `SELECT sa.id, sa.root_partner_user_id, sa.subagent_user_id, COALESCE(u.email,''), COALESCE(u.username,''), sa.aff_code, sa.status, sa.commission_rate::double precision, COALESCE(ua.aff_count,0), sa.created_at FROM affiliate_subagents sa JOIN users u ON u.id=sa.subagent_user_id LEFT JOIN user_affiliates ua ON ua.user_id=sa.subagent_user_id WHERE sa.root_partner_user_id=$1 ORDER BY sa.created_at DESC, sa.id DESC`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.SecondLevelAgent, 0)
	for rows.Next() {
		var item service.SecondLevelAgent
		if err := rows.Scan(&item.ID, &item.RootPartnerUserID, &item.SubagentUserID, &item.Email, &item.Username, &item.AffCode, &item.Status, &item.CommissionRate, &item.InvitedCount, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *affiliateRepository) ListSecondLevelAgencyCandidates(ctx context.Context, rootID int64, search string) ([]service.SecondLevelAgencyCandidate, error) {
	pattern := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
	rows, err := r.client.QueryContext(ctx, `SELECT u.id, COALESCE(u.email,''), COALESCE(u.username,'') FROM users u WHERE u.deleted_at IS NULL AND u.status='active' AND u.role='user' AND u.id <> $1 AND (LOWER(u.email) LIKE $2 OR LOWER(u.username) LIKE $2 OR u.id::text LIKE $2) AND NOT EXISTS (SELECT 1 FROM affiliate_subagents sa WHERE sa.subagent_user_id=u.id) ORDER BY u.created_at DESC, u.id DESC LIMIT 20`, rootID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.SecondLevelAgencyCandidate, 0)
	for rows.Next() {
		var item service.SecondLevelAgencyCandidate
		if err := rows.Scan(&item.UserID, &item.Email, &item.Username); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *affiliateRepository) CreateSecondLevelAgent(ctx context.Context, input service.SecondLevelAgent) (*service.SecondLevelAgent, error) {
	if input.AffCode == "" {
		input.AffCode = "AG" + strings.ToUpper(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	rows, err := r.client.QueryContext(ctx, `INSERT INTO affiliate_subagents (root_partner_user_id, subagent_user_id, aff_code, status, commission_rate) VALUES ($1,$2,$3,'active',$4) RETURNING id, created_at`, input.RootPartnerUserID, input.SubagentUserID, input.AffCode, input.CommissionRate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	if err := rows.Scan(&input.ID, &input.CreatedAt); err != nil {
		return nil, err
	}
	return &input, nil
}

func (r *affiliateRepository) SetSecondLevelAgentStatus(ctx context.Context, rootID, agentID int64, status string) error {
	_, err := r.client.ExecContext(ctx, `UPDATE affiliate_subagents SET status=$1, disabled_at=CASE WHEN $1='disabled' THEN NOW() ELSE NULL END, updated_at=NOW() WHERE id=$2 AND root_partner_user_id=$3`, status, agentID, rootID)
	return err
}

func (r *affiliateRepository) SetSecondLevelAgentCommissionRate(ctx context.Context, rootID, agentID int64, rate float64) error {
	_, err := r.client.ExecContext(ctx, `UPDATE affiliate_subagents SET commission_rate=$1, updated_at=NOW() WHERE id=$2 AND root_partner_user_id=$3`, rate, agentID, rootID)
	return err
}

func (r *affiliateRepository) IsSecondLevelAgentUser(ctx context.Context, userID int64) (bool, error) {
	rows, err := r.client.QueryContext(ctx, `SELECT 1 FROM affiliate_subagents WHERE subagent_user_id=$1 LIMIT 1`, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func (r *affiliateRepository) EnsureUserAffiliate(ctx context.Context, userID int64) (*service.AffiliateSummary, error) {
	if userID <= 0 {
		return nil, service.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	return ensureUserAffiliateWithClient(ctx, client, userID)
}

func (r *affiliateRepository) GetAffiliateByCode(ctx context.Context, code string) (*service.AffiliateSummary, error) {
	client := clientFromContext(ctx, r.client)
	if summary, err := queryAffiliateByCode(ctx, client, code); err == nil {
		return summary, nil
	} else if !errors.Is(err, service.ErrAffiliateProfileNotFound) {
		return nil, err
	}
	// A second-level agent owns a dedicated code in affiliate_subagents while
	// still using the normal user_affiliates inviter relation for attribution.
	rows, err := client.QueryContext(ctx, `SELECT sa.subagent_user_id, sa.aff_code, COALESCE(ua.aff_count,0), COALESCE(ua.aff_quota,0)::double precision, COALESCE(ua.aff_history_quota,0)::double precision, COALESCE(ua.partner_level,'none') FROM affiliate_subagents sa LEFT JOIN user_affiliates ua ON ua.user_id=sa.subagent_user_id WHERE sa.aff_code=$1 AND sa.status='active' LIMIT 1`, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, service.ErrAffiliateProfileNotFound
	}
	var summary service.AffiliateSummary
	if err := rows.Scan(&summary.UserID, &summary.AffCode, &summary.AffCount, &summary.AffQuota, &summary.AffHistoryQuota, &summary.PartnerLevel); err != nil {
		return nil, err
	}
	return &summary, nil
}

func (r *affiliateRepository) BindInviter(ctx context.Context, userID, inviterID int64) (bool, error) {
	var bound bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, inviterID); err != nil {
			return err
		}

		res, err := txClient.ExecContext(txCtx,
			"UPDATE user_affiliates SET inviter_id = $1, updated_at = NOW() WHERE user_id = $2 AND inviter_id IS NULL",
			inviterID, userID,
		)
		if err != nil {
			return fmt.Errorf("bind inviter: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			bound = false
			return nil
		}

		if _, err = txClient.ExecContext(txCtx,
			"UPDATE user_affiliates SET aff_count = aff_count + 1, updated_at = NOW() WHERE user_id = $1",
			inviterID,
		); err != nil {
			return fmt.Errorf("increment inviter aff_count: %w", err)
		}
		bound = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return bound, nil
}

func (r *affiliateRepository) AccrueQuota(ctx context.Context, inviterID, inviteeUserID int64, amount float64, freezeHours int, sourceOrderID *int64) (bool, error) {
	if amount <= 0 {
		return false, nil
	}

	var applied bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// If the direct inviter is a second-level agent, split the existing
		// platform rebate pool between the subagent and its first-level owner.
		// The total amount remains unchanged and both ledger rows retain the
		// historical rate snapshot for audit/reconciliation.
		var rootID int64
		var subRate float64
		rows, lookupErr := txClient.QueryContext(txCtx, `SELECT root_partner_user_id, commission_rate::double precision FROM affiliate_subagents WHERE subagent_user_id=$1 AND status='active' LIMIT 1`, inviterID)
		if lookupErr != nil {
			return lookupErr
		}
		if rows.Next() {
			if err := rows.Scan(&rootID, &subRate); err != nil {
				_ = rows.Close()
				return err
			}
		}
		_ = rows.Close()
		subAmount, rootAmount := amount, 0.0
		ledgerLevel := int16(1)
		if rootID > 0 && subRate > 0 {
			ledgerLevel = 2
			if subRate > 100 {
				subRate = 100
			}
			subAmount = amount * subRate / 100
			rootAmount = amount - subAmount
		}
		// freezeHours > 0: add to frozen quota; == 0: add to available quota directly
		var updateSQL string
		if freezeHours > 0 {
			updateSQL = "UPDATE user_affiliates SET aff_frozen_quota = aff_frozen_quota + $1, aff_history_quota = aff_history_quota + $1, updated_at = NOW() WHERE user_id = $2"
		} else {
			updateSQL = "UPDATE user_affiliates SET aff_quota = aff_quota + $1, aff_history_quota = aff_history_quota + $1, updated_at = NOW() WHERE user_id = $2"
		}
		res, err := txClient.ExecContext(txCtx, updateSQL, subAmount, inviterID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			applied = false
			return nil
		}

		ledgerInsert := func(userID int64, ledgerAmount float64, level int16, rootPartner *int64) error {
			if ledgerAmount <= 0 {
				return nil
			}
			if freezeHours > 0 {
				_, err = txClient.ExecContext(txCtx, `INSERT INTO user_affiliate_ledger (user_id, action, amount, source_user_id, source_order_id, frozen_until, agency_level, root_partner_user_id, commission_rate_snapshot, root_commission_amount, created_at, updated_at) VALUES ($1, 'accrue', $2, $3, $4, NOW() + make_interval(hours => $5), $6, $7, $8, $9, NOW(), NOW())`, userID, ledgerAmount, inviteeUserID, nullableInt64Arg(sourceOrderID), freezeHours, level, rootPartner, subRate, rootAmount)
			} else {
				_, err = txClient.ExecContext(txCtx, `INSERT INTO user_affiliate_ledger (user_id, action, amount, source_user_id, source_order_id, agency_level, root_partner_user_id, commission_rate_snapshot, root_commission_amount, created_at, updated_at) VALUES ($1, 'accrue', $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())`, userID, ledgerAmount, inviteeUserID, nullableInt64Arg(sourceOrderID), level, rootPartner, subRate, rootAmount)
			}
			return err
		}
		if freezeHours > 0 {
			if err = ledgerInsert(inviterID, subAmount, ledgerLevel, func() *int64 {
				if rootID > 0 {
					return &rootID
				}
				return nil
			}()); err != nil {
				return fmt.Errorf("insert affiliate accrue ledger: %w", err)
			}
		} else {
			if err = ledgerInsert(inviterID, subAmount, ledgerLevel, func() *int64 {
				if rootID > 0 {
					return &rootID
				}
				return nil
			}()); err != nil {
				return fmt.Errorf("insert affiliate accrue ledger: %w", err)
			}
		}
		if rootID > 0 && rootAmount > 0 {
			if _, err = txClient.ExecContext(txCtx, updateSQL, rootAmount, rootID); err != nil {
				return err
			}
			if err = ledgerInsert(rootID, rootAmount, 1, &rootID); err != nil {
				return fmt.Errorf("insert root affiliate accrue ledger: %w", err)
			}
		}

		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (r *affiliateRepository) GetAccruedRebateFromInvitee(ctx context.Context, inviterID, inviteeUserID int64) (float64, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx,
		`SELECT COALESCE(SUM(amount), 0)::double precision FROM user_affiliate_ledger WHERE user_id = $1 AND source_user_id = $2 AND action = 'accrue'`,
		inviterID, inviteeUserID)
	if err != nil {
		return 0, fmt.Errorf("query accrued rebate from invitee: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var total float64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, err
		}
	}
	return total, rows.Close()
}

func (r *affiliateRepository) ThawFrozenQuota(ctx context.Context, userID int64) (float64, error) {
	var thawed float64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		var err error
		thawed, err = thawFrozenQuotaTx(txCtx, txClient, userID)
		return err
	})
	return thawed, err
}

// thawFrozenQuotaTx moves matured frozen quota to available quota within an existing tx.
func thawFrozenQuotaTx(txCtx context.Context, txClient *dbent.Client, userID int64) (float64, error) {
	rows, err := txClient.QueryContext(txCtx, `
WITH matured AS (
    UPDATE user_affiliate_ledger
    SET frozen_until = NULL, updated_at = NOW()
    WHERE user_id = $1
      AND frozen_until IS NOT NULL
      AND frozen_until <= NOW()
    RETURNING amount
)
SELECT COALESCE(SUM(amount), 0) FROM matured`, userID)
	if err != nil {
		return 0, fmt.Errorf("thaw frozen quota: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var thawed float64
	if rows.Next() {
		if err := rows.Scan(&thawed); err != nil {
			return 0, err
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if thawed <= 0 {
		return 0, nil
	}

	_, err = txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_quota = aff_quota + $1,
    aff_frozen_quota = GREATEST(aff_frozen_quota - $1, 0),
    updated_at = NOW()
WHERE user_id = $2`, thawed, userID)
	if err != nil {
		return 0, fmt.Errorf("move thawed quota: %w", err)
	}
	return thawed, nil
}

func (r *affiliateRepository) TransferQuotaToBalance(ctx context.Context, userID int64) (float64, float64, error) {
	var transferred float64
	var newBalance float64

	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}

		// Thaw any matured frozen quota before transfer.
		if _, err := thawFrozenQuotaTx(txCtx, txClient, userID); err != nil {
			return fmt.Errorf("thaw before transfer: %w", err)
		}

		rows, err := txClient.QueryContext(txCtx, `
WITH claimed AS (
	SELECT aff_quota::double precision AS amount
	FROM user_affiliates
	WHERE user_id = $1
	  AND aff_quota > 0
	FOR UPDATE
),
cleared AS (
	UPDATE user_affiliates ua
	SET aff_quota = 0,
	    updated_at = NOW()
	FROM claimed c
	WHERE ua.user_id = $1
	RETURNING c.amount
)
SELECT amount
FROM cleared`, userID)
		if err != nil {
			return fmt.Errorf("claim affiliate quota: %w", err)
		}

		if !rows.Next() {
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			return service.ErrAffiliateQuotaEmpty
		}
		if err := rows.Scan(&transferred); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if transferred <= 0 {
			return service.ErrAffiliateQuotaEmpty
		}

		affected, err := txClient.User.Update().
			Where(user.IDEQ(userID)).
			AddBalance(transferred).
			AddTotalRecharged(transferred).
			Save(txCtx)
		if err != nil {
			return fmt.Errorf("credit user balance by affiliate quota: %w", err)
		}
		if affected == 0 {
			return service.ErrUserNotFound
		}

		newBalance, err = queryUserBalance(txCtx, txClient, userID)
		if err != nil {
			return err
		}

		snapshot, err := queryAffiliateTransferSnapshot(txCtx, txClient, userID)
		if err != nil {
			return err
		}

		if _, err = txClient.ExecContext(txCtx, `
INSERT INTO user_affiliate_ledger (
    user_id,
    action,
    amount,
    source_user_id,
    balance_after,
    aff_quota_after,
    aff_frozen_quota_after,
    aff_history_quota_after,
    created_at,
    updated_at
)
VALUES ($1, 'transfer', $2, NULL, $3, $4, $5, $6, NOW(), NOW())`,
			userID,
			transferred,
			snapshot.BalanceAfter,
			snapshot.AvailableQuotaAfter,
			snapshot.FrozenQuotaAfter,
			snapshot.HistoryQuotaAfter,
		); err != nil {
			return fmt.Errorf("insert affiliate transfer ledger: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	return transferred, newBalance, nil
}

func (r *affiliateRepository) ListInvitees(ctx context.Context, inviterID int64, limit int) ([]service.AffiliateInvitee, error) {
	if limit <= 0 {
		limit = 100
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
WITH `+affiliateUsageExchangeRateCTE+`,
recharge_by_user AS (
    SELECT po.user_id,
           COALESCE(SUM(`+affiliateUsageRechargeAmountSQL+`), 0)::double precision AS recharge_amount,
           COALESCE(SUM(CASE
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'CNY'
               THEN `+affiliateEligiblePayAmountSQL+`
               ELSE 0
           END), 0)::double precision AS recharge_amount_cny,
           COALESCE(SUM(CASE
               WHEN LOWER(COALESCE(po.payment_type, '')) IN ('usdt', 'infini')
                    OR LOWER(COALESCE(po.provider_key, '')) IN ('usdt', 'infini')
                    OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) = 'USDT'
               THEN `+affiliateEligiblePayAmountSQL+`
               ELSE 0
           END), 0)::double precision AS recharge_amount_usdt
    FROM payment_orders po
    CROSS JOIN exchange_rate er
    WHERE po.status = 'COMPLETED'
      AND po.order_type = 'balance'
      AND po.paid_at IS NOT NULL
      AND (
            LOWER(COALESCE(po.payment_type, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay', 'usdt', 'infini')
            OR LOWER(COALESCE(po.provider_key, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay', 'usdt', 'infini')
            OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) IN ('CNY', 'USDT')
      )
    GROUP BY po.user_id
)
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.created_at,
       COALESCE(rb.recharge_amount, 0)::double precision AS recharge_amount,
       COALESCE(rb.recharge_amount_cny, 0)::double precision AS recharge_amount_cny,
       COALESCE(rb.recharge_amount_usdt, 0)::double precision AS recharge_amount_usdt,
       COALESCE(SUM(ual.amount), 0)::double precision AS total_rebate
FROM user_affiliates ua
LEFT JOIN users u ON u.id = ua.user_id
LEFT JOIN recharge_by_user rb ON rb.user_id = ua.user_id
LEFT JOIN user_affiliate_ledger ual
       ON ual.user_id = $1
      AND ual.source_user_id = ua.user_id
      AND ual.action = 'accrue'
WHERE ua.inviter_id = $1
GROUP BY ua.user_id, u.email, u.username, ua.created_at, rb.recharge_amount, rb.recharge_amount_cny, rb.recharge_amount_usdt
ORDER BY ua.created_at DESC
LIMIT $2`, inviterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	invitees := make([]service.AffiliateInvitee, 0)
	for rows.Next() {
		var item service.AffiliateInvitee
		var createdAt time.Time
		if err := rows.Scan(&item.UserID, &item.Email, &item.Username, &createdAt, &item.RechargeAmount, &item.RechargeAmountCNY, &item.RechargeAmountUSDT, &item.TotalRebate); err != nil {
			return nil, err
		}
		item.CreatedAt = &createdAt
		invitees = append(invitees, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return invitees, nil
}

func (r *affiliateRepository) ListAffiliateInviteRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateInviteRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ua.created_at", []string{
		"inviter.email", "inviter.username", "invitee.email", "invitee.username",
		"ua.inviter_id::text", "ua.user_id::text", "inviter_aff.aff_code",
	})

	total, err := queryAffiliateRecordCount(ctx, client, `
SELECT COUNT(*)
FROM user_affiliates ua
JOIN users invitee ON invitee.id = ua.user_id
JOIN users inviter ON inviter.id = ua.inviter_id
JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
`+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"inviter":      "inviter.email",
		"invitee":      "invitee.email",
		"aff_code":     "inviter_aff.aff_code",
		"total_rebate": "total_rebate",
		"created_at":   "ua.created_at",
	}, "ua.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT ua.inviter_id,
       COALESCE(inviter.email, ''),
       COALESCE(inviter.username, ''),
       ua.user_id,
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       COALESCE(inviter_aff.aff_code, ''),
       COALESCE(SUM(ual.amount), 0)::double precision AS total_rebate,
       ua.created_at
FROM user_affiliates ua
JOIN users invitee ON invitee.id = ua.user_id
JOIN users inviter ON inviter.id = ua.inviter_id
JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
LEFT JOIN user_affiliate_ledger ual
       ON ual.user_id = ua.inviter_id
      AND ual.source_user_id = ua.user_id
      AND ual.action = 'accrue'
`+where+`
GROUP BY ua.inviter_id, inviter.email, inviter.username, ua.user_id, invitee.email, invitee.username, inviter_aff.aff_code, ua.created_at
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateInviteRecord, 0)
	for rows.Next() {
		var item service.AffiliateInviteRecord
		if err := rows.Scan(
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.AffCode,
			&item.TotalRebate,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *affiliateRepository) AdminAssignInviter(ctx context.Context, inviteeID, inviterID int64) (*service.AffiliateInviteAssignment, error) {
	if inviteeID <= 0 || inviterID <= 0 {
		return nil, service.ErrUserNotFound
	}
	if inviteeID == inviterID {
		return nil, service.ErrAffiliateSelfBinding
	}

	assignment := &service.AffiliateInviteAssignment{
		InviterID: inviterID,
		InviteeID: inviteeID,
		Changed:   false,
	}

	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, inviteeID); err != nil {
			return err
		}
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, inviterID); err != nil {
			return err
		}

		rows, err := txClient.QueryContext(txCtx, `
SELECT inviter_id
FROM user_affiliates
WHERE user_id = $1
FOR UPDATE`, inviteeID)
		if err != nil {
			return fmt.Errorf("query current inviter: %w", err)
		}
		var currentInviter sql.NullInt64
		if rows.Next() {
			if err := rows.Scan(&currentInviter); err != nil {
				_ = rows.Close()
				return err
			}
		} else {
			_ = rows.Close()
			return service.ErrUserNotFound
		}
		if err := rows.Close(); err != nil {
			return err
		}

		if currentInviter.Valid && currentInviter.Int64 == inviterID {
			return nil
		}

		if currentInviter.Valid && currentInviter.Int64 > 0 {
			if _, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_count = GREATEST(aff_count - 1, 0),
    updated_at = NOW()
WHERE user_id = $1`, currentInviter.Int64); err != nil {
				return fmt.Errorf("decrement previous inviter aff_count: %w", err)
			}
		}

		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET inviter_id = $1,
    updated_at = NOW()
WHERE user_id = $2`, inviterID, inviteeID); err != nil {
			return fmt.Errorf("assign inviter: %w", err)
		}

		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_count = aff_count + 1,
    updated_at = NOW()
WHERE user_id = $1`, inviterID); err != nil {
			return fmt.Errorf("increment inviter aff_count: %w", err)
		}
		assignment.Changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assignment, nil
}

func (r *affiliateRepository) ListAffiliateUsageDailyRecords(ctx context.Context, filter service.AffiliateUsageFilter) ([]service.AffiliateUsageDailyRecord, *service.AffiliateUsageSummary, int64, error) {
	client := clientFromContext(ctx, r.client)
	cte, args := buildAffiliateUsageCTE(filter)

	var total int64
	summary := &service.AffiliateUsageSummary{}
	rows, err := client.QueryContext(ctx, cte+`
SELECT COUNT(*)::bigint,
       COALESCE(SUM(requests), 0)::bigint,
       COALESCE(SUM(total_tokens), 0)::bigint,
       COALESCE(SUM(actual_cost), 0)::double precision,
       COALESCE(SUM(account_cost), 0)::double precision,
       COALESCE(SUM(net_profit), 0)::double precision,
       COALESCE(SUM(recharge_amount), 0)::double precision,
       COALESCE(SUM(rebate_amount), 0)::double precision,
       COALESCE(SUM(settled_amount), 0)::double precision,
       COALESCE(SUM(pending_amount), 0)::double precision
FROM records`, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	if rows.Next() {
		if err := rows.Scan(&total, &summary.TotalRequests, &summary.TotalTokens, &summary.TotalActualCost, &summary.TotalAccountCost, &summary.TotalNetProfit, &summary.TotalRecharge, &summary.TotalRebateAmount, &summary.TotalSettledAmount, &summary.TotalPendingAmount); err != nil {
			_ = rows.Close()
			return nil, nil, 0, err
		}
	}
	if err := rows.Close(); err != nil {
		return nil, nil, 0, err
	}
	if filter.InviterOnly && filter.View == "users" && filter.InviterID > 0 {
		settledAmount, err := queryAffiliateUsageSettledAmount(ctx, client, filter)
		if err != nil {
			return nil, nil, 0, err
		}
		summary.TotalSettledAmount = settledAmount
		summary.TotalPendingAmount = math.Max(summary.TotalRebateAmount-settledAmount, 0)
	}

	orderBy := buildAffiliateUsageOrderBy(filter)
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	args = append(args, pageSize, (page-1)*pageSize)
	limitPlaceholder := "$" + fmt.Sprint(len(args)-1)
	offsetPlaceholder := "$" + fmt.Sprint(len(args))

	rows, err = client.QueryContext(ctx, cte+`
SELECT usage_date,
       inviter_id,
       inviter_email,
       inviter_username,
       invitee_id,
       invitee_email,
       invitee_username,
       invitee_count,
       requests,
       total_tokens,
       actual_cost,
       account_cost,
       net_profit,
       recharge_amount,
       rebate_rate_percent,
       rebate_amount,
       settled_amount,
       pending_amount,
       unassigned,
       profit_details,
       members
FROM records
`+orderBy+`
LIMIT `+limitPlaceholder+` OFFSET `+offsetPlaceholder, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateUsageDailyRecord, 0)
	for rows.Next() {
		var item service.AffiliateUsageDailyRecord
		var profitDetailsRaw []byte
		var membersRaw []byte
		if err := rows.Scan(
			&item.Date,
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.InviteeCount,
			&item.Requests,
			&item.TotalTokens,
			&item.ActualCost,
			&item.AccountCost,
			&item.NetProfit,
			&item.RechargeAmount,
			&item.RebateRatePercent,
			&item.RebateAmount,
			&item.SettledAmount,
			&item.PendingAmount,
			&item.Unassigned,
			&profitDetailsRaw,
			&membersRaw,
		); err != nil {
			return nil, nil, 0, err
		}
		if len(profitDetailsRaw) > 0 {
			if err := json.Unmarshal(profitDetailsRaw, &item.ProfitDetails); err != nil {
				return nil, nil, 0, fmt.Errorf("decode affiliate usage profit details: %w", err)
			}
		}
		if len(membersRaw) > 0 {
			if err := json.Unmarshal(membersRaw, &item.Members); err != nil {
				return nil, nil, 0, fmt.Errorf("decode affiliate usage members: %w", err)
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	return items, summary, total, nil
}

func queryAffiliateUsageSettledAmount(ctx context.Context, client affiliateQueryExecer, filter service.AffiliateUsageFilter) (float64, error) {
	args := []any{filter.InviterID}
	clauses := []string{"uas.user_id = $1", "uas.amount > 0"}
	tz := strings.ReplaceAll(affiliateUsageTimezone(filter.Timezone), "'", "''")
	if filter.StartAt != nil {
		args = append(args, *filter.StartAt)
		clauses = append(clauses, fmt.Sprintf("uas.settled_on >= ($%d AT TIME ZONE '%s')::date", len(args), tz))
	}
	if filter.EndAt != nil {
		args = append(args, *filter.EndAt)
		clauses = append(clauses, fmt.Sprintf("uas.settled_on < ($%d AT TIME ZONE '%s')::date", len(args), tz))
	}

	rows, err := client.QueryContext(ctx, `
SELECT COALESCE(SUM(uas.amount), 0)::double precision
FROM user_affiliate_settlements uas
WHERE `+strings.Join(clauses, " AND "), args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var settledAmount float64
	if rows.Next() {
		if err := rows.Scan(&settledAmount); err != nil {
			return 0, err
		}
	}
	return settledAmount, rows.Err()
}

func (r *affiliateRepository) ListAffiliateRebateRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateRebateRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ual.created_at", []string{
		"inviter.email", "inviter.username", "invitee.email", "invitee.username",
		"po.id::text", "po.out_trade_no", "po.payment_type", "po.status",
	})
	baseJoin := `
FROM user_affiliate_ledger ual
JOIN payment_orders po ON po.id = ual.source_order_id
JOIN users invitee ON invitee.id = ual.source_user_id
JOIN users inviter ON inviter.id = ual.user_id
WHERE ual.action = 'accrue'
  AND ual.source_order_id IS NOT NULL`
	if where != "" {
		where = strings.Replace(where, "WHERE ", " AND ", 1)
	}

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"order":         "po.id",
		"inviter":       "inviter.email",
		"invitee":       "invitee.email",
		"order_amount":  "po.amount",
		"pay_amount":    "po.pay_amount",
		"rebate_amount": "ual.amount",
		"payment_type":  "po.payment_type",
		"order_status":  "po.status",
		"created_at":    "ual.created_at",
	}, "ual.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT po.id,
       po.out_trade_no,
       ual.user_id,
       COALESCE(inviter.email, ''),
       COALESCE(inviter.username, ''),
       ual.source_user_id,
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       po.amount::double precision,
       po.pay_amount::double precision,
       ual.amount::double precision,
       po.payment_type,
       po.status,
       ual.created_at
`+baseJoin+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateRebateRecord, 0)
	for rows.Next() {
		var item service.AffiliateRebateRecord
		if err := rows.Scan(
			&item.OrderID,
			&item.OutTradeNo,
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.OrderAmount,
			&item.PayAmount,
			&item.RebateAmount,
			&item.PaymentType,
			&item.OrderStatus,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *affiliateRepository) ListAffiliateTransferRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateTransferRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ual.created_at", []string{
		"u.email", "u.username", "u.id::text",
	})
	baseJoin := `
FROM user_affiliate_ledger ual
JOIN users u ON u.id = ual.user_id
WHERE ual.action = 'transfer'`
	if where != "" {
		where = strings.Replace(where, "WHERE ", " AND ", 1)
	}

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"user":                  "u.email",
		"amount":                "ual.amount",
		"balance_after":         "ual.balance_after",
		"available_quota_after": "ual.aff_quota_after",
		"frozen_quota_after":    "ual.aff_frozen_quota_after",
		"history_quota_after":   "ual.aff_history_quota_after",
		"created_at":            "ual.created_at",
	}, "ual.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT ual.id,
       ual.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ual.amount::double precision,
       ual.balance_after::double precision,
       ual.aff_quota_after::double precision,
       ual.aff_frozen_quota_after::double precision,
       ual.aff_history_quota_after::double precision,
       ual.created_at
`+baseJoin+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateTransferRecord, 0)
	for rows.Next() {
		var item service.AffiliateTransferRecord
		var balanceAfter sql.NullFloat64
		var availableQuotaAfter sql.NullFloat64
		var frozenQuotaAfter sql.NullFloat64
		var historyQuotaAfter sql.NullFloat64
		if err := rows.Scan(
			&item.LedgerID,
			&item.UserID,
			&item.UserEmail,
			&item.Username,
			&item.Amount,
			&balanceAfter,
			&availableQuotaAfter,
			&frozenQuotaAfter,
			&historyQuotaAfter,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		item.BalanceAfter = nullableFloat64Ptr(balanceAfter)
		item.AvailableQuotaAfter = nullableFloat64Ptr(availableQuotaAfter)
		item.FrozenQuotaAfter = nullableFloat64Ptr(frozenQuotaAfter)
		item.HistoryQuotaAfter = nullableFloat64Ptr(historyQuotaAfter)
		item.SnapshotAvailable = balanceAfter.Valid &&
			availableQuotaAfter.Valid &&
			frozenQuotaAfter.Valid &&
			historyQuotaAfter.Valid
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *affiliateRepository) CreateAffiliateSettlement(ctx context.Context, input service.AffiliateSettlementInput) (*service.AffiliateSettlementRecord, error) {
	client := clientFromContext(ctx, r.client)
	createdBy := sql.NullInt64{Int64: input.CreatedBy, Valid: input.CreatedBy > 0}
	rows, err := client.QueryContext(ctx, `
WITH inserted AS (
    INSERT INTO user_affiliate_settlements (user_id, amount, settled_on, note, created_by)
    SELECT u.id, $2, $3::date, $4, $5::bigint
    FROM users u
    WHERE u.id = $1 AND u.deleted_at IS NULL
    RETURNING id, user_id, amount::double precision, settled_on, note, created_by, created_at, updated_at
)
SELECT i.id,
       i.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       i.amount,
       i.settled_on,
       i.note,
       i.created_by,
       COALESCE(admin.email, ''),
       COALESCE(admin.username, ''),
       i.created_at,
       i.updated_at
FROM inserted i
JOIN users u ON u.id = i.user_id
LEFT JOIN users admin ON admin.id = i.created_by`, input.UserID, input.Amount, input.SettledOn, input.Note, createdBy)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrUserNotFound
	}
	item, err := scanAffiliateSettlementRecord(rows)
	if err != nil {
		return nil, err
	}
	return &item, rows.Err()
}

func (r *affiliateRepository) ListAffiliateSettlementRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateSettlementRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "uas.settled_on::timestamptz", []string{
		"u.email", "u.username", "u.id::text", "uas.note", "admin.email", "admin.username",
	})
	if filter.UserID > 0 {
		args = append(args, filter.UserID)
		condition := fmt.Sprintf("uas.user_id = $%d", len(args))
		if where == "" {
			where = "WHERE " + condition
		} else {
			where += " AND " + condition
		}
	}
	baseJoin := `
FROM user_affiliate_settlements uas
JOIN users u ON u.id = uas.user_id
LEFT JOIN users admin ON admin.id = uas.created_by`

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+" "+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"user":       "u.email",
		"amount":     "uas.amount",
		"settled_on": "uas.settled_on",
		"note":       "uas.note",
		"created_by": "admin.email",
		"created_at": "uas.created_at",
	}, "uas.settled_on")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT uas.id,
       uas.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       uas.amount::double precision,
       uas.settled_on,
       uas.note,
       uas.created_by,
       COALESCE(admin.email, ''),
       COALESCE(admin.username, ''),
       uas.created_at,
       uas.updated_at
`+baseJoin+`
`+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateSettlementRecord, 0)
	for rows.Next() {
		item, err := scanAffiliateSettlementRecord(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *affiliateRepository) GetAffiliateUserOverview(ctx context.Context, userID int64) (*service.AffiliateUserOverview, error) {
	if userID <= 0 {
		return nil, service.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, affiliateUserOverviewSQL, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrUserNotFound
	}

	var overview service.AffiliateUserOverview
	var customRate float64
	var hasCustomRate bool
	if err := rows.Scan(
		&overview.UserID,
		&overview.Email,
		&overview.Username,
		&overview.AffCode,
		&customRate,
		&hasCustomRate,
		&overview.PartnerLevel,
		&overview.InvitedCount,
		&overview.RebatedInviteeCount,
		&overview.AvailableQuota,
		&overview.HistoryQuota,
	); err != nil {
		return nil, err
	}
	if hasCustomRate {
		overview.RebateRatePercent = customRate
		overview.RebateRateCustom = true
	}
	return &overview, rows.Err()
}

func buildAffiliateRecordWhere(filter service.AffiliateRecordFilter, timeColumn string, searchColumns []string) (string, []any) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if filter.StartAt != nil {
		args = append(args, *filter.StartAt)
		clauses = append(clauses, fmt.Sprintf("%s >= $%d", timeColumn, len(args)))
	}
	if filter.EndAt != nil {
		args = append(args, *filter.EndAt)
		clauses = append(clauses, fmt.Sprintf("%s <= $%d", timeColumn, len(args)))
	}
	search := strings.TrimSpace(filter.Search)
	if search != "" && len(searchColumns) > 0 {
		args = append(args, "%"+strings.ToLower(search)+"%")
		parts := make([]string, 0, len(searchColumns))
		for _, col := range searchColumns {
			parts = append(parts, fmt.Sprintf("LOWER(%s) LIKE $%d", col, len(args)))
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func buildAffiliateRecordOrderBy(filter service.AffiliateRecordFilter, sortColumns map[string]string, fallbackColumn string) string {
	column := sortColumns[filter.SortBy]
	if column == "" {
		column = fallbackColumn
	}
	direction := "DESC"
	if !filter.SortDesc {
		direction = "ASC"
	}
	return "ORDER BY " + column + " " + direction + " NULLS LAST"
}

func buildAffiliateUsageCTE(filter service.AffiliateUsageFilter) (string, []any) {
	args := make([]any, 0, 6)
	usageTimeClauses := make([]string, 0, 2)
	startAtArg := 0
	endAtArg := 0
	if filter.StartAt != nil {
		args = append(args, *filter.StartAt)
		startAtArg = len(args)
		usageTimeClauses = append(usageTimeClauses, fmt.Sprintf("ul.created_at >= $%d", len(args)))
	}
	if filter.EndAt != nil {
		args = append(args, *filter.EndAt)
		endAtArg = len(args)
		usageTimeClauses = append(usageTimeClauses, fmt.Sprintf("ul.created_at < $%d", len(args)))
	}

	paymentClauses := []string{
		"po.status = 'COMPLETED'",
		"po.order_type = 'balance'",
		"po.paid_at IS NOT NULL",
		`(
            LOWER(COALESCE(po.payment_type, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay', 'usdt', 'infini')
            OR LOWER(COALESCE(po.provider_key, '')) IN ('alipay', 'wxpay', 'alipay_direct', 'wxpay_direct', 'easypay', 'usdt', 'infini')
            OR UPPER(COALESCE(po.provider_snapshot->>'currency', '')) IN ('CNY', 'USDT')
        )`,
	}
	if startAtArg > 0 {
		paymentClauses = append(paymentClauses, fmt.Sprintf("po.paid_at >= $%d", startAtArg))
	}
	if endAtArg > 0 {
		paymentClauses = append(paymentClauses, fmt.Sprintf("po.paid_at < $%d", endAtArg))
	}
	paymentWhere := strings.Join(paymentClauses, " AND ")
	subscriptionOrderWhere := buildAffiliateSubscriptionOrderWhere(endAtArg, filter.Timezone)
	subscriptionEffectiveWhere := buildAffiliateSubscriptionEffectiveWhere(startAtArg, endAtArg, filter.Timezone)
	groupProfitRatesCTE := buildAffiliateGroupProfitRatesCTE(&args, filter.GroupProfitRates)

	if filter.View == "groups" {
		userClauses := []string{"u.deleted_at IS NULL"}
		if filter.InviterID > 0 {
			args = append(args, filter.InviterID)
			userClauses = append(userClauses, fmt.Sprintf("ua.inviter_id = $%d", len(args)))
		}
		if filter.InviteeID > 0 {
			args = append(args, filter.InviteeID)
			userClauses = append(userClauses, fmt.Sprintf("u.id = $%d", len(args)))
		}
		search := strings.TrimSpace(filter.Search)
		if search != "" {
			args = append(args, "%"+search+"%")
			searchArg := len(args)
			userClauses = append(userClauses, fmt.Sprintf(`(
inviter.email ILIKE $%[1]d OR inviter.username ILIKE $%[1]d OR inviter.id::text ILIKE $%[1]d OR
u.email ILIKE $%[1]d OR u.username ILIKE $%[1]d OR u.id::text ILIKE $%[1]d OR
inviter_aff.aff_code ILIKE $%[1]d OR
CASE WHEN ua.inviter_id IS NULL THEN 'unassigned' ELSE '' END ILIKE $%[1]d
)`, searchArg))
		}
		defaultRate := filter.DefaultRebateRatePercent
		if defaultRate < service.AffiliateRebateRateMin {
			defaultRate = service.AffiliateRebateRateMin
		}
		if defaultRate > service.AffiliateRebateRateMax {
			defaultRate = service.AffiliateRebateRateMax
		}
		args = append(args, defaultRate)
		ratePlaceholder := "$" + fmt.Sprint(len(args))
		rateExpr := fmt.Sprintf(affiliatePartnerRateSQL, ratePlaceholder)
		usageWhere := ""
		if len(usageTimeClauses) > 0 {
			usageWhere = "WHERE " + strings.Join(usageTimeClauses, " AND ")
		}
		userWhere := "WHERE " + strings.Join(userClauses, " AND ")
		return fmt.Sprintf(`
WITH RECURSIVE %[7]s,
%[3]s,
usage_by_user_detail AS (
    SELECT ul.user_id,
           COALESCE(ul.group_id, 0)::bigint AS group_id,
           COALESCE(NULLIF(g.name, ''), CASE WHEN ul.group_id IS NULL THEN '' ELSE '#' || ul.group_id::text END) AS group_name,
           COALESCE(NULLIF(BTRIM(ul.requested_model), ''), NULLIF(BTRIM(ul.model), ''), '-') AS model,
           COUNT(*)::bigint AS requests,
           COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(ul.actual_cost), 0)::double precision AS actual_cost,
           COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0)::double precision AS account_cost,
           CASE WHEN ul.subscription_id IS NULL THEN COALESCE(agpr.profit_rate_percent, 0) ELSE 0 END::double precision AS profit_rate_percent,
           COALESCE(SUM(CASE WHEN ul.subscription_id IS NULL THEN GREATEST(ul.actual_cost, 0) * COALESCE(agpr.profit_rate_percent, 0) / 100 ELSE 0 END), 0)::double precision AS net_profit
    FROM usage_logs ul
    LEFT JOIN groups g ON g.id = ul.group_id
    LEFT JOIN affiliate_group_profit_rates agpr ON agpr.group_id = ul.group_id
    %[2]s
    GROUP BY ul.user_id,
             COALESCE(ul.group_id, 0),
             COALESCE(NULLIF(g.name, ''), CASE WHEN ul.group_id IS NULL THEN '' ELSE '#' || ul.group_id::text END),
             COALESCE(NULLIF(BTRIM(ul.requested_model), ''), NULLIF(BTRIM(ul.model), ''), '-'),
             CASE WHEN ul.subscription_id IS NULL THEN COALESCE(agpr.profit_rate_percent, 0) ELSE 0 END
),
usage_by_user AS (
    SELECT user_id,
           COALESCE(SUM(requests), 0)::bigint AS requests,
           COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(actual_cost), 0)::double precision AS actual_cost,
           COALESCE(SUM(account_cost), 0)::double precision AS account_cost,
           COALESCE(SUM(net_profit), 0)::double precision AS net_profit
    FROM usage_by_user_detail
    GROUP BY user_id
),
usage_user_records AS (
    SELECT '' AS usage_date,
           COALESCE(ua.inviter_id, 0)::bigint AS inviter_id,
           CASE WHEN ua.inviter_id IS NULL THEN '' ELSE COALESCE(inviter.email, '') END AS inviter_email,
           CASE WHEN ua.inviter_id IS NULL THEN '' ELSE COALESCE(inviter.username, '') END AS inviter_username,
           u.id AS invitee_id,
           COALESCE(u.email, '') AS invitee_email,
           COALESCE(u.username, '') AS invitee_username,
           COALESCE(ubu.requests, 0)::bigint AS requests,
           COALESCE(ubu.total_tokens, 0)::bigint AS total_tokens,
           COALESCE(ubu.actual_cost, 0)::double precision AS actual_cost,
           COALESCE(ubu.account_cost, 0)::double precision AS account_cost,
           COALESCE(ubu.net_profit, 0)::double precision AS net_profit,
           CASE
               WHEN ua.inviter_id IS NULL THEN 0::double precision
               ELSE %[1]s::double precision
           END AS rebate_rate_percent,
           CASE
               WHEN ua.inviter_id IS NULL THEN 0::double precision
               ELSE (COALESCE(ubu.net_profit, 0) * %[1]s / 100)::double precision
           END AS rebate_amount,
           (ua.inviter_id IS NULL) AS unassigned,
           COALESCE((
               SELECT jsonb_agg(jsonb_build_object(
                   'group_id', detail.group_id,
                   'group_name', detail.group_name,
                   'model', detail.model,
                   'requests', detail.requests,
                   'total_tokens', detail.total_tokens,
                   'actual_cost', detail.actual_cost,
                   'profit_rate_percent', detail.profit_rate_percent,
                   'net_profit', detail.net_profit,
                   'rebate_amount', CASE WHEN ua.inviter_id IS NULL THEN 0::double precision ELSE detail.net_profit * %[1]s / 100 END
               ) ORDER BY detail.net_profit DESC, detail.actual_cost DESC, detail.group_name ASC, detail.model ASC)
               FROM usage_by_user_detail detail
               WHERE detail.user_id = u.id
           ), '[]'::jsonb) AS profit_details
    FROM users u
    LEFT JOIN usage_by_user ubu ON ubu.user_id = u.id
    LEFT JOIN user_affiliates ua ON ua.user_id = u.id
    LEFT JOIN users inviter ON inviter.id = ua.inviter_id
    LEFT JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
    %[4]s
),
subscription_order_rows AS (
    SELECT po.*,
           ROW_NUMBER() OVER (PARTITION BY po.user_id, po.subscription_group_id ORDER BY po.paid_at ASC, po.id ASC) AS subscription_order_number,
           GREATEST(COALESCE(po.subscription_days, 30), 1)::integer AS subscription_days_for_rebate
    FROM payment_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    WHERE %[8]s
),
subscription_effective_orders AS (
    SELECT sor.*,
           sor.paid_at AS effective_start_at,
           sor.paid_at + (sor.subscription_days_for_rebate * INTERVAL '1 day') AS effective_end_at
    FROM subscription_order_rows sor
    WHERE sor.subscription_order_number = 1
    UNION ALL
    SELECT sor.*,
           GREATEST(sor.paid_at, seo.effective_end_at) AS effective_start_at,
           GREATEST(sor.paid_at, seo.effective_end_at) + (sor.subscription_days_for_rebate * INTERVAL '1 day') AS effective_end_at
    FROM subscription_order_rows sor
    JOIN subscription_effective_orders seo ON seo.user_id = sor.user_id
        AND seo.subscription_group_id = sor.subscription_group_id
        AND seo.subscription_order_number + 1 = sor.subscription_order_number
),
recharge_by_user AS (
    SELECT po.user_id,
           COALESCE(SUM(%[5]s), 0)::double precision AS recharge_amount
    FROM payment_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    CROSS JOIN exchange_rate er
    WHERE %[6]s
    GROUP BY po.user_id
),
settlement_by_user AS (
    SELECT uas.user_id,
           COALESCE(SUM(uas.amount), 0)::double precision AS settled_amount
    FROM user_affiliate_settlements uas
    JOIN (SELECT DISTINCT inviter_id FROM usage_user_records WHERE inviter_id > 0) inviter_scope ON inviter_scope.inviter_id = uas.user_id
    WHERE (%[9]s)
    GROUP BY uas.user_id
),
subscription_profit_by_user AS (
    SELECT po.user_id,
           COALESCE(SUM(%[5]s), 0)::double precision AS recharge_amount,
           COALESCE(SUM(%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100), 0)::double precision AS net_profit,
           COALESCE(SUM(CASE WHEN uur.unassigned THEN 0::double precision ELSE (%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100) * uur.rebate_rate_percent / 100 END), 0)::double precision AS rebate_amount,
           COALESCE(jsonb_agg(jsonb_build_object(
               'group_id', COALESCE(po.subscription_group_id, 0),
               'group_name', COALESCE(NULLIF(g.name, ''), CASE WHEN po.subscription_group_id IS NULL THEN '' ELSE '#' || po.subscription_group_id::text END),
               'model', COALESCE(NULLIF(sp.name, ''), 'Subscription'),
               'source', 'subscription',
               'requests', 1,
               'total_tokens', 0,
               'actual_cost', %[5]s,
               'profit_rate_percent', COALESCE(agpr.profit_rate_percent, 0),
               'net_profit', %[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100,
               'rebate_amount', CASE WHEN uur.unassigned THEN 0::double precision ELSE (%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100) * uur.rebate_rate_percent / 100 END
           ) ORDER BY po.effective_start_at DESC, po.paid_at DESC, po.id DESC), '[]'::jsonb) AS profit_details
    FROM subscription_effective_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    CROSS JOIN exchange_rate er
    LEFT JOIN affiliate_group_profit_rates agpr ON agpr.group_id = po.subscription_group_id
    LEFT JOIN groups g ON g.id = po.subscription_group_id
    LEFT JOIN subscription_plans sp ON sp.id = po.plan_id
    WHERE %[10]s
    GROUP BY po.user_id
),
group_profit_detail_rows AS (
    SELECT uur.inviter_id,
           uur.rebate_rate_percent,
           uur.unassigned,
           detail.group_id,
           detail.group_name,
           detail.model,
           COALESCE(SUM(detail.requests), 0)::bigint AS requests,
           COALESCE(SUM(detail.total_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(detail.actual_cost), 0)::double precision AS actual_cost,
           COALESCE(MAX(detail.profit_rate_percent), 0)::double precision AS profit_rate_percent,
           COALESCE(SUM(detail.net_profit), 0)::double precision AS net_profit,
           CASE
               WHEN uur.unassigned THEN 0::double precision
               ELSE (COALESCE(SUM(detail.net_profit), 0) * uur.rebate_rate_percent / 100)::double precision
           END AS rebate_amount
    FROM usage_user_records uur
    JOIN usage_by_user_detail detail ON detail.user_id = uur.invitee_id
    GROUP BY uur.inviter_id,
             uur.rebate_rate_percent,
             uur.unassigned,
             detail.group_id,
             detail.group_name,
             detail.model
),
group_profit_details AS (
    SELECT inviter_id,
           rebate_rate_percent,
           unassigned,
           COALESCE(jsonb_agg(jsonb_build_object(
               'group_id', group_id,
               'group_name', group_name,
               'model', model,
               'requests', requests,
               'total_tokens', total_tokens,
               'actual_cost', actual_cost,
               'profit_rate_percent', profit_rate_percent,
               'net_profit', net_profit,
               'rebate_amount', rebate_amount
           ) ORDER BY net_profit DESC, actual_cost DESC, group_name ASC, model ASC), '[]'::jsonb) AS profit_details
    FROM group_profit_detail_rows
    GROUP BY inviter_id, rebate_rate_percent, unassigned
),
group_subscription_profit_details AS (
    SELECT uur.inviter_id,
           uur.rebate_rate_percent,
           uur.unassigned,
           COALESCE(jsonb_agg(sp_detail.detail ORDER BY (sp_detail.detail->>'net_profit')::double precision DESC), '[]'::jsonb) AS profit_details
    FROM usage_user_records uur
    JOIN subscription_profit_by_user spbu ON spbu.user_id = uur.invitee_id
    JOIN LATERAL jsonb_array_elements(COALESCE(spbu.profit_details, '[]'::jsonb)) AS sp_detail(detail) ON TRUE
    GROUP BY uur.inviter_id, uur.rebate_rate_percent, uur.unassigned
),
records AS (
    SELECT '' AS usage_date,
           uur.inviter_id,
           uur.inviter_email,
           uur.inviter_username,
           0::bigint AS invitee_id,
           '' AS invitee_email,
           '' AS invitee_username,
           COUNT(*)::bigint AS invitee_count,
           COALESCE(SUM(uur.requests), 0)::bigint AS requests,
           COALESCE(SUM(uur.total_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(uur.actual_cost), 0)::double precision AS actual_cost,
           COALESCE(SUM(uur.account_cost), 0)::double precision AS account_cost,
           COALESCE(SUM(uur.net_profit + COALESCE(spbu.net_profit, 0)), 0)::double precision AS net_profit,
           COALESCE(SUM(COALESCE(rb.recharge_amount, 0) + COALESCE(spbu.recharge_amount, 0)), 0)::double precision AS recharge_amount,
           uur.rebate_rate_percent,
           COALESCE(SUM(uur.rebate_amount + COALESCE(spbu.rebate_amount, 0)), 0)::double precision AS rebate_amount,
           COALESCE(sbu.settled_amount, 0)::double precision AS settled_amount,
           GREATEST(COALESCE(SUM(uur.rebate_amount + COALESCE(spbu.rebate_amount, 0)), 0) - COALESCE(sbu.settled_amount, 0), 0)::double precision AS pending_amount,
           uur.unassigned,
           COALESCE(gpd.profit_details, '[]'::jsonb) || COALESCE(gspd.profit_details, '[]'::jsonb) AS profit_details,
           COALESCE(jsonb_agg(jsonb_build_object(
               'date', uur.usage_date,
               'inviter_id', uur.inviter_id,
               'inviter_email', uur.inviter_email,
               'inviter_username', uur.inviter_username,
               'invitee_id', uur.invitee_id,
               'invitee_email', uur.invitee_email,
               'invitee_username', uur.invitee_username,
               'invitee_count', 1,
               'requests', uur.requests,
               'total_tokens', uur.total_tokens,
               'actual_cost', uur.actual_cost,
               'account_cost', uur.account_cost,
               'net_profit', uur.net_profit + COALESCE(spbu.net_profit, 0),
               'recharge_amount', COALESCE(rb.recharge_amount, 0) + COALESCE(spbu.recharge_amount, 0),
               'rebate_rate_percent', uur.rebate_rate_percent,
               'rebate_amount', uur.rebate_amount + COALESCE(spbu.rebate_amount, 0),
               'settled_amount', 0,
               'pending_amount', uur.rebate_amount + COALESCE(spbu.rebate_amount, 0),
               'unassigned', uur.unassigned,
               'profit_details', uur.profit_details || COALESCE(spbu.profit_details, '[]'::jsonb)
            ) ORDER BY uur.actual_cost DESC, (COALESCE(rb.recharge_amount, 0) + COALESCE(spbu.recharge_amount, 0)) DESC, uur.invitee_id ASC), '[]'::jsonb) AS members
    FROM usage_user_records uur
    LEFT JOIN recharge_by_user rb ON rb.user_id = uur.invitee_id
    LEFT JOIN subscription_profit_by_user spbu ON spbu.user_id = uur.invitee_id
    LEFT JOIN settlement_by_user sbu ON sbu.user_id = uur.inviter_id
    LEFT JOIN group_profit_details gpd ON gpd.inviter_id = uur.inviter_id
        AND gpd.rebate_rate_percent = uur.rebate_rate_percent
        AND gpd.unassigned = uur.unassigned
    LEFT JOIN group_subscription_profit_details gspd ON gspd.inviter_id = uur.inviter_id
        AND gspd.rebate_rate_percent = uur.rebate_rate_percent
        AND gspd.unassigned = uur.unassigned
    GROUP BY uur.inviter_id, uur.inviter_email, uur.inviter_username, uur.rebate_rate_percent, uur.unassigned, sbu.settled_amount, gpd.profit_details, gspd.profit_details
)
`, rateExpr, usageWhere, affiliateUsageExchangeRateCTE, userWhere, affiliateUsageRechargeAmountCNYSQL, paymentWhere, groupProfitRatesCTE, subscriptionOrderWhere, buildAffiliateSettlementWhere(startAtArg, endAtArg, filter.Timezone), subscriptionEffectiveWhere), args
	}

	userClauses := []string{"u.deleted_at IS NULL"}
	if filter.InviterID > 0 {
		args = append(args, filter.InviterID)
		userClauses = append(userClauses, fmt.Sprintf("ua.inviter_id = $%d", len(args)))
	}
	if filter.InviteeID > 0 {
		args = append(args, filter.InviteeID)
		userClauses = append(userClauses, fmt.Sprintf("u.id = $%d", len(args)))
	}
	search := strings.TrimSpace(filter.Search)
	if search != "" {
		args = append(args, "%"+search+"%")
		searchArg := len(args)
		userClauses = append(userClauses, fmt.Sprintf(`(
inviter.email ILIKE $%[1]d OR inviter.username ILIKE $%[1]d OR inviter.id::text ILIKE $%[1]d OR
u.email ILIKE $%[1]d OR u.username ILIKE $%[1]d OR u.id::text ILIKE $%[1]d OR
inviter_aff.aff_code ILIKE $%[1]d OR
CASE WHEN ua.inviter_id IS NULL THEN 'unassigned' ELSE '' END ILIKE $%[1]d
)`, searchArg))
	}
	defaultRate := filter.DefaultRebateRatePercent
	if defaultRate < service.AffiliateRebateRateMin {
		defaultRate = service.AffiliateRebateRateMin
	}
	if defaultRate > service.AffiliateRebateRateMax {
		defaultRate = service.AffiliateRebateRateMax
	}
	args = append(args, defaultRate)
	ratePlaceholder := "$" + fmt.Sprint(len(args))
	rateExpr := fmt.Sprintf(affiliatePartnerRateSQL, ratePlaceholder)
	usageWhere := ""
	if len(usageTimeClauses) > 0 {
		usageWhere = "WHERE " + strings.Join(usageTimeClauses, " AND ")
	}
	userWhere := "WHERE " + strings.Join(userClauses, " AND ")

	return fmt.Sprintf(`
WITH RECURSIVE %[7]s,
%[3]s,
usage_by_user_detail AS (
    SELECT ul.user_id,
           COALESCE(ul.group_id, 0)::bigint AS group_id,
           COALESCE(NULLIF(g.name, ''), CASE WHEN ul.group_id IS NULL THEN '' ELSE '#' || ul.group_id::text END) AS group_name,
           COALESCE(NULLIF(BTRIM(ul.requested_model), ''), NULLIF(BTRIM(ul.model), ''), '-') AS model,
           COUNT(*)::bigint AS requests,
           COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(ul.actual_cost), 0)::double precision AS actual_cost,
           COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0)::double precision AS account_cost,
           CASE WHEN ul.subscription_id IS NULL THEN COALESCE(agpr.profit_rate_percent, 0) ELSE 0 END::double precision AS profit_rate_percent,
           COALESCE(SUM(CASE WHEN ul.subscription_id IS NULL THEN GREATEST(ul.actual_cost, 0) * COALESCE(agpr.profit_rate_percent, 0) / 100 ELSE 0 END), 0)::double precision AS net_profit
    FROM usage_logs ul
    LEFT JOIN groups g ON g.id = ul.group_id
    LEFT JOIN affiliate_group_profit_rates agpr ON agpr.group_id = ul.group_id
    %[2]s
    GROUP BY ul.user_id,
             COALESCE(ul.group_id, 0),
             COALESCE(NULLIF(g.name, ''), CASE WHEN ul.group_id IS NULL THEN '' ELSE '#' || ul.group_id::text END),
             COALESCE(NULLIF(BTRIM(ul.requested_model), ''), NULLIF(BTRIM(ul.model), ''), '-'),
             CASE WHEN ul.subscription_id IS NULL THEN COALESCE(agpr.profit_rate_percent, 0) ELSE 0 END
),
usage_by_user AS (
    SELECT user_id,
           COALESCE(SUM(requests), 0)::bigint AS requests,
           COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
           COALESCE(SUM(actual_cost), 0)::double precision AS actual_cost,
           COALESCE(SUM(account_cost), 0)::double precision AS account_cost,
           COALESCE(SUM(net_profit), 0)::double precision AS net_profit
    FROM usage_by_user_detail
    GROUP BY user_id
),
usage_user_records AS (
    SELECT '' AS usage_date,
           COALESCE(ua.inviter_id, 0)::bigint AS inviter_id,
           CASE WHEN ua.inviter_id IS NULL THEN '' ELSE COALESCE(inviter.email, '') END AS inviter_email,
           CASE WHEN ua.inviter_id IS NULL THEN '' ELSE COALESCE(inviter.username, '') END AS inviter_username,
           u.id AS invitee_id,
           COALESCE(u.email, '') AS invitee_email,
           COALESCE(u.username, '') AS invitee_username,
           COALESCE(ubu.requests, 0)::bigint AS requests,
           COALESCE(ubu.total_tokens, 0)::bigint AS total_tokens,
           COALESCE(ubu.actual_cost, 0)::double precision AS actual_cost,
           COALESCE(ubu.account_cost, 0)::double precision AS account_cost,
           COALESCE(ubu.net_profit, 0)::double precision AS net_profit,
           CASE
               WHEN ua.inviter_id IS NULL THEN 0::double precision
               ELSE %[1]s::double precision
           END AS rebate_rate_percent,
           CASE
               WHEN ua.inviter_id IS NULL THEN 0::double precision
               ELSE (COALESCE(ubu.net_profit, 0) * %[1]s / 100)::double precision
           END AS rebate_amount,
           (ua.inviter_id IS NULL) AS unassigned,
           COALESCE((
               SELECT jsonb_agg(jsonb_build_object(
                   'group_id', detail.group_id,
                   'group_name', detail.group_name,
                   'model', detail.model,
                   'requests', detail.requests,
                   'total_tokens', detail.total_tokens,
                   'actual_cost', detail.actual_cost,
                   'profit_rate_percent', detail.profit_rate_percent,
                   'net_profit', detail.net_profit,
                   'rebate_amount', CASE WHEN ua.inviter_id IS NULL THEN 0::double precision ELSE detail.net_profit * %[1]s / 100 END
               ) ORDER BY detail.net_profit DESC, detail.actual_cost DESC, detail.group_name ASC, detail.model ASC)
               FROM usage_by_user_detail detail
               WHERE detail.user_id = u.id
           ), '[]'::jsonb) AS profit_details
    FROM users u
    LEFT JOIN usage_by_user ubu ON ubu.user_id = u.id
    LEFT JOIN user_affiliates ua ON ua.user_id = u.id
    LEFT JOIN users inviter ON inviter.id = ua.inviter_id
    LEFT JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
    %[4]s
),
subscription_order_rows AS (
    SELECT po.*,
           ROW_NUMBER() OVER (PARTITION BY po.user_id, po.subscription_group_id ORDER BY po.paid_at ASC, po.id ASC) AS subscription_order_number,
           GREATEST(COALESCE(po.subscription_days, 30), 1)::integer AS subscription_days_for_rebate
    FROM payment_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    WHERE %[8]s
),
subscription_effective_orders AS (
    SELECT sor.*,
           sor.paid_at AS effective_start_at,
           sor.paid_at + (sor.subscription_days_for_rebate * INTERVAL '1 day') AS effective_end_at
    FROM subscription_order_rows sor
    WHERE sor.subscription_order_number = 1
    UNION ALL
    SELECT sor.*,
           GREATEST(sor.paid_at, seo.effective_end_at) AS effective_start_at,
           GREATEST(sor.paid_at, seo.effective_end_at) + (sor.subscription_days_for_rebate * INTERVAL '1 day') AS effective_end_at
    FROM subscription_order_rows sor
    JOIN subscription_effective_orders seo ON seo.user_id = sor.user_id
        AND seo.subscription_group_id = sor.subscription_group_id
        AND seo.subscription_order_number + 1 = sor.subscription_order_number
),
recharge_by_user AS (
    SELECT po.user_id,
           COALESCE(SUM(%[5]s), 0)::double precision AS recharge_amount
    FROM payment_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    CROSS JOIN exchange_rate er
    WHERE %[6]s
    GROUP BY po.user_id
),
settlement_by_user AS (
    SELECT uas.user_id,
           COALESCE(SUM(uas.amount), 0)::double precision AS settled_amount
    FROM user_affiliate_settlements uas
    JOIN (SELECT DISTINCT inviter_id FROM usage_user_records WHERE inviter_id > 0) inviter_scope ON inviter_scope.inviter_id = uas.user_id
    WHERE (%[9]s)
    GROUP BY uas.user_id
),
subscription_profit_by_user AS (
    SELECT po.user_id,
           COALESCE(SUM(%[5]s), 0)::double precision AS recharge_amount,
           COALESCE(SUM(%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100), 0)::double precision AS net_profit,
           COALESCE(SUM(CASE WHEN uur.unassigned THEN 0::double precision ELSE (%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100) * uur.rebate_rate_percent / 100 END), 0)::double precision AS rebate_amount,
           COALESCE(jsonb_agg(jsonb_build_object(
               'group_id', COALESCE(po.subscription_group_id, 0),
               'group_name', COALESCE(NULLIF(g.name, ''), CASE WHEN po.subscription_group_id IS NULL THEN '' ELSE '#' || po.subscription_group_id::text END),
               'model', COALESCE(NULLIF(sp.name, ''), 'Subscription'),
               'source', 'subscription',
               'requests', 1,
               'total_tokens', 0,
               'actual_cost', %[5]s,
               'profit_rate_percent', COALESCE(agpr.profit_rate_percent, 0),
               'net_profit', %[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100,
               'rebate_amount', CASE WHEN uur.unassigned THEN 0::double precision ELSE (%[5]s * COALESCE(agpr.profit_rate_percent, 0) / 100) * uur.rebate_rate_percent / 100 END
           ) ORDER BY po.effective_start_at DESC, po.paid_at DESC, po.id DESC), '[]'::jsonb) AS profit_details
    FROM subscription_effective_orders po
    JOIN usage_user_records uur ON uur.invitee_id = po.user_id
    CROSS JOIN exchange_rate er
    LEFT JOIN affiliate_group_profit_rates agpr ON agpr.group_id = po.subscription_group_id
    LEFT JOIN groups g ON g.id = po.subscription_group_id
    LEFT JOIN subscription_plans sp ON sp.id = po.plan_id
    WHERE %[10]s
    GROUP BY po.user_id
),
records AS (
    SELECT uur.usage_date,
           uur.inviter_id,
           uur.inviter_email,
           uur.inviter_username,
           uur.invitee_id,
           uur.invitee_email,
           uur.invitee_username,
           1::bigint AS invitee_count,
           uur.requests,
           uur.total_tokens,
           uur.actual_cost,
           uur.account_cost,
           uur.net_profit + COALESCE(spbu.net_profit, 0) AS net_profit,
           (COALESCE(rb.recharge_amount, 0) + COALESCE(spbu.recharge_amount, 0))::double precision AS recharge_amount,
           uur.rebate_rate_percent,
           uur.rebate_amount + COALESCE(spbu.rebate_amount, 0) AS rebate_amount,
           0::double precision AS settled_amount,
           uur.rebate_amount + COALESCE(spbu.rebate_amount, 0) AS pending_amount,
           uur.unassigned,
           uur.profit_details || COALESCE(spbu.profit_details, '[]'::jsonb) AS profit_details,
           '[]'::jsonb AS members
    FROM usage_user_records uur
    LEFT JOIN recharge_by_user rb ON rb.user_id = uur.invitee_id
    LEFT JOIN subscription_profit_by_user spbu ON spbu.user_id = uur.invitee_id
)
`, rateExpr, usageWhere, affiliateUsageExchangeRateCTE, userWhere, affiliateUsageRechargeAmountCNYSQL, paymentWhere, groupProfitRatesCTE, subscriptionOrderWhere, buildAffiliateSettlementWhere(startAtArg, endAtArg, filter.Timezone), subscriptionEffectiveWhere), args
}

func buildAffiliateGroupProfitRatesCTE(args *[]any, rates map[int64]float64) string {
	if len(rates) == 0 {
		return "affiliate_group_profit_rates AS (SELECT NULL::bigint AS group_id, 0::double precision AS profit_rate_percent WHERE false)"
	}
	groupIDs := make([]int64, 0, len(rates))
	for groupID := range rates {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })

	values := make([]string, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		rate := rates[groupID]
		if groupID <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			continue
		}
		if rate > service.AffiliateRebateRateMax {
			rate = service.AffiliateRebateRateMax
		}
		*args = append(*args, groupID, rate)
		values = append(values, fmt.Sprintf("($%d::bigint, $%d::double precision)", len(*args)-1, len(*args)))
	}
	if len(values) == 0 {
		return "affiliate_group_profit_rates AS (SELECT NULL::bigint AS group_id, 0::double precision AS profit_rate_percent WHERE false)"
	}
	return "affiliate_group_profit_rates(group_id, profit_rate_percent) AS (VALUES " + strings.Join(values, ", ") + ")"
}

func buildAffiliateSubscriptionOrderWhere(endAtArg int, userTZ string) string {
	clauses := []string{
		"po.status = 'COMPLETED'",
		"po.order_type = 'subscription'",
		"po.paid_at IS NOT NULL",
		"po.subscription_group_id IS NOT NULL",
	}
	if endAtArg > 0 {
		clauses = append(clauses, fmt.Sprintf("po.paid_at < %s", affiliateUsageMonthEndBoundarySQL(endAtArg, userTZ)))
	}
	return strings.Join(clauses, " AND ")
}

func buildAffiliateSubscriptionEffectiveWhere(startAtArg, endAtArg int, userTZ string) string {
	clauses := []string{"po.effective_start_at IS NOT NULL"}
	tz := strings.ReplaceAll(affiliateUsageTimezone(userTZ), "'", "''")
	if startAtArg > 0 {
		clauses = append(clauses, fmt.Sprintf("date_trunc('month', po.effective_start_at AT TIME ZONE '%s') >= date_trunc('month', $%d AT TIME ZONE '%s')", tz, startAtArg, tz))
	}
	if endAtArg > 0 {
		clauses = append(clauses, fmt.Sprintf("date_trunc('month', po.effective_start_at AT TIME ZONE '%s') < date_trunc('month', (($%d - INTERVAL '1 microsecond') AT TIME ZONE '%s')) + INTERVAL '1 month'", tz, endAtArg, tz))
	}
	return strings.Join(clauses, " AND ")
}

func affiliateUsageMonthEndBoundarySQL(arg int, userTZ string) string {
	tz := strings.ReplaceAll(affiliateUsageTimezone(userTZ), "'", "''")
	return fmt.Sprintf("((date_trunc('month', (($%d - INTERVAL '1 microsecond') AT TIME ZONE '%s')) + INTERVAL '1 month') AT TIME ZONE '%s')", arg, tz, tz)
}

func buildAffiliateSettlementWhere(startAtArg, endAtArg int, userTZ string) string {
	clauses := []string{"uas.amount > 0"}
	tz := strings.ReplaceAll(affiliateUsageTimezone(userTZ), "'", "''")
	if startAtArg > 0 {
		clauses = append(clauses, fmt.Sprintf("uas.settled_on >= ($%d AT TIME ZONE '%s')::date", startAtArg, tz))
	}
	if endAtArg > 0 {
		clauses = append(clauses, fmt.Sprintf("uas.settled_on < ($%d AT TIME ZONE '%s')::date", endAtArg, tz))
	}
	return strings.Join(clauses, " AND ")
}

func affiliateUsageTimezone(tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return "UTC"
	}
	return tz
}

func buildAffiliateUsageOrderBy(filter service.AffiliateUsageFilter) string {
	sortColumns := map[string]string{
		"date":            "usage_date",
		"inviter":         "inviter_email",
		"invitee":         "invitee_email",
		"user":            "invitee_email",
		"invitee_count":   "invitee_count",
		"requests":        "requests",
		"total_tokens":    "total_tokens",
		"actual_cost":     "actual_cost",
		"account_cost":    "account_cost",
		"net_profit":      "net_profit",
		"recharge_amount": "recharge_amount",
		"rebate_rate":     "rebate_rate_percent",
		"rebate_amount":   "rebate_amount",
		"settled_amount":  "settled_amount",
		"pending_amount":  "pending_amount",
	}
	column := sortColumns[filter.SortBy]
	if column == "" {
		column = "actual_cost"
	}
	direction := "DESC"
	if !filter.SortDesc {
		direction = "ASC"
	}
	return "ORDER BY " + column + " " + direction + " NULLS LAST, inviter_id ASC, invitee_id ASC"
}

func queryAffiliateRecordCount(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (int64, error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, rows.Err()
	}
	var total int64
	if err := rows.Scan(&total); err != nil {
		return 0, err
	}
	return total, rows.Err()
}

type affiliateSettlementScanner interface {
	Scan(dest ...any) error
}

func scanAffiliateSettlementRecord(scanner affiliateSettlementScanner) (service.AffiliateSettlementRecord, error) {
	var item service.AffiliateSettlementRecord
	var createdBy sql.NullInt64
	if err := scanner.Scan(
		&item.ID,
		&item.UserID,
		&item.UserEmail,
		&item.Username,
		&item.Amount,
		&item.SettledOn,
		&item.Note,
		&createdBy,
		&item.CreatedByEmail,
		&item.CreatedByUsername,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return item, err
	}
	item.CreatedBy = nullableInt64Ptr(createdBy)
	return item, nil
}

func (r *affiliateRepository) withTx(ctx context.Context, fn func(txCtx context.Context, txClient *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin affiliate transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit affiliate transaction: %w", err)
	}
	return nil
}

func ensureUserAffiliateWithClient(ctx context.Context, client affiliateQueryExecer, userID int64) (*service.AffiliateSummary, error) {
	summary, err := queryAffiliateByUserID(ctx, client, userID)
	if err == nil {
		return summary, nil
	}
	if !errors.Is(err, service.ErrAffiliateProfileNotFound) {
		return nil, err
	}

	for i := 0; i < affiliateCodeMaxAttempts; i++ {
		code, codeErr := generateAffiliateCode()
		if codeErr != nil {
			return nil, codeErr
		}
		_, insertErr := client.ExecContext(ctx, `
INSERT INTO user_affiliates (user_id, aff_code, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING`, userID, code)
		if insertErr == nil {
			break
		}
		if isAffiliateUniqueViolation(insertErr) {
			continue
		}
		return nil, insertErr
	}

	return queryAffiliateByUserID(ctx, client, userID)
}

func queryAffiliateByUserID(ctx context.Context, client affiliateQueryExecer, userID int64) (*service.AffiliateSummary, error) {
	rows, err := client.QueryContext(ctx, `
SELECT user_id,
       aff_code,
       aff_code_custom,
       aff_rebate_rate_percent,
       COALESCE(partner_level, 'none'),
       inviter_id,
       aff_count,
       aff_quota::double precision,
       aff_frozen_quota::double precision,
       aff_history_quota::double precision,
       created_at,
       updated_at
FROM user_affiliates
WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAffiliateProfileNotFound
	}

	var out service.AffiliateSummary
	var inviterID sql.NullInt64
	var rebateRate sql.NullFloat64
	if err := rows.Scan(
		&out.UserID,
		&out.AffCode,
		&out.AffCodeCustom,
		&rebateRate,
		&out.PartnerLevel,
		&inviterID,
		&out.AffCount,
		&out.AffQuota,
		&out.AffFrozenQuota,
		&out.AffHistoryQuota,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if inviterID.Valid {
		out.InviterID = &inviterID.Int64
	}
	if rebateRate.Valid {
		v := rebateRate.Float64
		out.AffRebateRatePercent = &v
	}
	return &out, nil
}

func queryAffiliateByCode(ctx context.Context, client affiliateQueryExecer, code string) (*service.AffiliateSummary, error) {
	rows, err := client.QueryContext(ctx, `
SELECT user_id,
       aff_code,
       aff_code_custom,
       aff_rebate_rate_percent,
       COALESCE(partner_level, 'none'),
       inviter_id,
       aff_count,
       aff_quota::double precision,
       aff_frozen_quota::double precision,
       aff_history_quota::double precision,
       created_at,
       updated_at
FROM user_affiliates
WHERE aff_code = $1
LIMIT 1`, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAffiliateProfileNotFound
	}

	var out service.AffiliateSummary
	var inviterID sql.NullInt64
	var rebateRate sql.NullFloat64
	if err := rows.Scan(
		&out.UserID,
		&out.AffCode,
		&out.AffCodeCustom,
		&rebateRate,
		&out.PartnerLevel,
		&inviterID,
		&out.AffCount,
		&out.AffQuota,
		&out.AffFrozenQuota,
		&out.AffHistoryQuota,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if inviterID.Valid {
		out.InviterID = &inviterID.Int64
	}
	if rebateRate.Valid {
		v := rebateRate.Float64
		out.AffRebateRatePercent = &v
	}
	return &out, nil
}

func queryUserBalance(ctx context.Context, client affiliateQueryExecer, userID int64) (float64, error) {
	rows, err := client.QueryContext(ctx,
		"SELECT balance::double precision FROM users WHERE id = $1 LIMIT 1",
		userID,
	)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, service.ErrUserNotFound
	}
	var balance float64
	if err := rows.Scan(&balance); err != nil {
		return 0, err
	}
	return balance, nil
}

type affiliateTransferSnapshot struct {
	BalanceAfter        float64
	AvailableQuotaAfter float64
	FrozenQuotaAfter    float64
	HistoryQuotaAfter   float64
}

func queryAffiliateTransferSnapshot(ctx context.Context, client affiliateQueryExecer, userID int64) (*affiliateTransferSnapshot, error) {
	rows, err := client.QueryContext(ctx, `
SELECT u.balance::double precision,
       ua.aff_quota::double precision,
       ua.aff_frozen_quota::double precision,
       ua.aff_history_quota::double precision
FROM users u
JOIN user_affiliates ua ON ua.user_id = u.id
WHERE u.id = $1
LIMIT 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query affiliate transfer snapshot: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrUserNotFound
	}

	var snapshot affiliateTransferSnapshot
	if err := rows.Scan(
		&snapshot.BalanceAfter,
		&snapshot.AvailableQuotaAfter,
		&snapshot.FrozenQuotaAfter,
		&snapshot.HistoryQuotaAfter,
	); err != nil {
		return nil, err
	}
	return &snapshot, rows.Err()
}

func nullableFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func nullableInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func generateAffiliateCode() (string, error) {
	buf := make([]byte, affiliateCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate affiliate code: %w", err)
	}
	for i := range buf {
		buf[i] = affiliateCodeCharset[int(buf[i])%len(affiliateCodeCharset)]
	}
	return string(buf), nil
}

func isAffiliateUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code) == "23505"
	}
	return false
}

// UpdateUserAffCode 改写用户的邀请码（自定义专属邀请码）。
// 唯一性冲突返回 ErrAffiliateCodeTaken。
func (r *affiliateRepository) UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error {
	if userID <= 0 {
		return service.ErrUserNotFound
	}
	code := strings.ToUpper(strings.TrimSpace(newCode))
	if code == "" {
		return service.ErrAffiliateCodeInvalid
	}

	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_code = $1,
    aff_code_custom = true,
    updated_at = NOW()
WHERE user_id = $2`, code, userID)
		if err != nil {
			if isAffiliateUniqueViolation(err) {
				return service.ErrAffiliateCodeTaken
			}
			return fmt.Errorf("update aff_code: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return service.ErrUserNotFound
		}
		return nil
	})
}

// ResetUserAffCode 把 aff_code 还原为系统随机码，并清除 aff_code_custom 标记。
func (r *affiliateRepository) ResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	if userID <= 0 {
		return "", service.ErrUserNotFound
	}
	var newCode string
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		for i := 0; i < affiliateCodeMaxAttempts; i++ {
			candidate, codeErr := generateAffiliateCode()
			if codeErr != nil {
				return codeErr
			}
			res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_code = $1,
    aff_code_custom = false,
    updated_at = NOW()
WHERE user_id = $2`, candidate, userID)
			if err != nil {
				if isAffiliateUniqueViolation(err) {
					continue
				}
				return fmt.Errorf("reset aff_code: %w", err)
			}
			affected, _ := res.RowsAffected()
			if affected == 0 {
				return service.ErrUserNotFound
			}
			newCode = candidate
			return nil
		}
		return fmt.Errorf("reset aff_code: exhausted attempts")
	})
	if err != nil {
		return "", err
	}
	return newCode, nil
}

// SetUserRebateRate 设置或清除用户专属返利比例。ratePercent==nil 表示清除（沿用全局）。
func (r *affiliateRepository) SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	if userID <= 0 {
		return service.ErrUserNotFound
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		// nullableArg lets us use a single UPDATE for both "set value" and
		// "clear" cases — database/sql converts nil interface{} to SQL NULL.
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_rebate_rate_percent = $1,
    updated_at = NOW()
WHERE user_id = $2`, nullableArg(ratePercent), userID)
		if err != nil {
			return fmt.Errorf("set aff_rebate_rate_percent: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return service.ErrUserNotFound
		}
		return nil
	})
}

// BatchSetUserRebateRate 批量为多个用户设置专属比例（nil 清除）。
func (r *affiliateRepository) BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	if len(userIDs) == 0 {
		return nil
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		for _, uid := range userIDs {
			if uid <= 0 {
				continue
			}
			if _, err := ensureUserAffiliateWithClient(txCtx, txClient, uid); err != nil {
				return err
			}
		}
		_, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_rebate_rate_percent = $1,
    updated_at = NOW()
WHERE user_id = ANY($2)`, nullableArg(ratePercent), pq.Array(userIDs))
		if err != nil {
			return fmt.Errorf("batch set aff_rebate_rate_percent: %w", err)
		}
		return nil
	})
}

func (r *affiliateRepository) SetUserPartnerLevel(ctx context.Context, userID int64, level string) error {
	if userID <= 0 {
		return service.ErrUserNotFound
	}
	level = service.NormalizeAffiliatePartnerLevel(level)
	if service.AffiliatePartnerLevelRank(level) < 0 {
		return service.ErrAffiliatePartnerLevelInvalid
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET partner_level = $1,
    updated_at = NOW()
WHERE user_id = $2`, level, userID)
		if err != nil {
			return fmt.Errorf("set partner_level: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return service.ErrUserNotFound
		}
		return nil
	})
}

func (r *affiliateRepository) PromotePartnerLevelForInviteCount(ctx context.Context, userID int64, tiers []service.AffiliatePartnerTier) (*service.AffiliatePartnerTier, bool, error) {
	if userID <= 0 {
		return nil, false, service.ErrUserNotFound
	}
	var promotedTier *service.AffiliatePartnerTier
	var changed bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		summary, err := ensureUserAffiliateWithClient(txCtx, txClient, userID)
		if err != nil {
			return err
		}
		nextTier, ok := service.AffiliatePartnerTierByInviteCountFrom(tiers, summary.AffCount)
		if !ok {
			return nil
		}
		currentRank := service.AffiliatePartnerLevelRank(summary.PartnerLevel)
		nextRank := service.AffiliatePartnerLevelRank(nextTier.Level)
		if nextRank <= currentRank {
			return nil
		}
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET partner_level = $1,
    updated_at = NOW()
WHERE user_id = $2`, nextTier.Level, userID)
		if err != nil {
			return fmt.Errorf("promote partner level: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return service.ErrUserNotFound
		}
		changed = true
		promotedTier = &nextTier
		return nil
	})
	return promotedTier, changed, err
}

func (r *affiliateRepository) GetPartnerSummariesByUserIDs(ctx context.Context, userIDs []int64) (map[int64]service.AffiliatePartnerSummary, error) {
	result := make(map[int64]service.AffiliatePartnerSummary, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
SELECT ua.user_id,
       ua.aff_code,
       ua.aff_code_custom,
       ua.aff_rebate_rate_percent,
       COALESCE(ua.partner_level, 'none'),
       ua.aff_count
FROM user_affiliates ua
WHERE ua.user_id = ANY($1)`, pq.Array(userIDs))
	if err != nil {
		return nil, fmt.Errorf("list partner summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item service.AffiliatePartnerSummary
		var rebate sql.NullFloat64
		if err := rows.Scan(
			&item.UserID,
			&item.AffCode,
			&item.AffCodeCustom,
			&rebate,
			&item.PartnerLevel,
			&item.AffCount,
		); err != nil {
			return nil, err
		}
		if rebate.Valid {
			v := rebate.Float64
			item.AffRebateRatePercent = &v
		}
		result[item.UserID] = item
	}
	return result, rows.Err()
}

func (r *affiliateRepository) CreatePartnerApplication(ctx context.Context, userID int64, input service.AffiliatePartnerApplicationInput) (*service.AffiliatePartnerApplication, error) {
	if userID <= 0 {
		return nil, service.ErrUserNotFound
	}
	var applicationID int64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		rows, err := txClient.QueryContext(txCtx, `
INSERT INTO user_affiliate_partner_applications (
    user_id,
    requested_level,
    source,
    strengths,
    portal_url,
    status,
    created_at,
    updated_at
)
VALUES ($1, $2, $3, $4, $5, 'pending', NOW(), NOW())
RETURNING id`, userID, input.RequestedLevel, input.Source, input.Strengths, input.PortalURL)
		if err != nil {
			if isAffiliateUniqueViolation(err) {
				return service.ErrAffiliatePartnerApplicationPending
			}
			return fmt.Errorf("create partner application: %w", err)
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return service.ErrAffiliatePartnerApplicationNotFound
		}
		if err := rows.Scan(&applicationID); err != nil {
			return err
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return r.GetLatestPartnerApplication(ctx, userID)
}

func (r *affiliateRepository) GetLatestPartnerApplication(ctx context.Context, userID int64) (*service.AffiliatePartnerApplication, error) {
	if userID <= 0 {
		return nil, service.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, partnerApplicationSelectSQL()+`
WHERE app.user_id = $1
ORDER BY app.created_at DESC
LIMIT 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("get latest partner application: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAffiliatePartnerApplicationNotFound
	}
	app, err := scanPartnerApplication(rows)
	if err != nil {
		return nil, err
	}
	return app, rows.Err()
}

func (r *affiliateRepository) ListPartnerApplications(ctx context.Context, filter service.AffiliatePartnerApplicationFilter) ([]service.AffiliatePartnerApplication, int64, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	whereParts := []string{"1=1"}
	args := make([]any, 0, 4)
	if filter.Status != "" {
		args = append(args, filter.Status)
		whereParts = append(whereParts, fmt.Sprintf("app.status = $%d", len(args)))
	}
	if strings.TrimSpace(filter.Search) != "" {
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		searchArg := len(args)
		whereParts = append(whereParts, fmt.Sprintf(`(
u.email ILIKE $%[1]d OR u.username ILIKE $%[1]d OR u.id::text ILIKE $%[1]d OR
app.source ILIKE $%[1]d OR app.portal_url ILIKE $%[1]d OR app.requested_level ILIKE $%[1]d
)`, searchArg))
	}
	where := "WHERE " + strings.Join(whereParts, " AND ")
	client := clientFromContext(ctx, r.client)
	total, err := scanInt64(ctx, client, `
SELECT COUNT(*)
FROM user_affiliate_partner_applications app
JOIN users u ON u.id = app.user_id
LEFT JOIN user_affiliates ua ON ua.user_id = app.user_id
`+where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count partner applications: %w", err)
	}
	listArgs := append(args, pageSize, (page-1)*pageSize)
	rows, err := client.QueryContext(ctx, partnerApplicationSelectSQL()+`
`+where+`
ORDER BY app.created_at DESC
LIMIT $`+fmt.Sprint(len(listArgs)-1)+` OFFSET $`+fmt.Sprint(len(listArgs)), listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list partner applications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.AffiliatePartnerApplication, 0)
	for rows.Next() {
		item, err := scanPartnerApplication(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *affiliateRepository) ReviewPartnerApplication(ctx context.Context, applicationID, reviewerID int64, input service.AffiliatePartnerApplicationReviewInput) (*service.AffiliatePartnerApplication, error) {
	if applicationID <= 0 {
		return nil, service.ErrAffiliatePartnerApplicationNotFound
	}
	var userID int64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		rows, err := txClient.QueryContext(txCtx, `
SELECT user_id, status
FROM user_affiliate_partner_applications
WHERE id = $1
FOR UPDATE`, applicationID)
		if err != nil {
			return fmt.Errorf("lock partner application: %w", err)
		}
		var status string
		if rows.Next() {
			if err := rows.Scan(&userID, &status); err != nil {
				_ = rows.Close()
				return err
			}
		} else {
			_ = rows.Close()
			return service.ErrAffiliatePartnerApplicationNotFound
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if status != service.AffiliatePartnerApplicationStatusPending {
			return service.ErrAffiliatePartnerApplicationFinalized
		}

		grantedLevel := sql.NullString{}
		if input.Status == service.AffiliatePartnerApplicationStatusApproved {
			grantedLevel.Valid = true
			grantedLevel.String = input.GrantedLevel
		}
		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliate_partner_applications
SET status = $1,
    granted_level = $2,
    review_note = $3,
    reviewer_id = $4,
    reviewed_at = NOW(),
    updated_at = NOW()
WHERE id = $5`, input.Status, grantedLevel, input.ReviewNote, nullableReviewerID(reviewerID), applicationID); err != nil {
			return fmt.Errorf("review partner application: %w", err)
		}
		if grantedLevel.Valid {
			if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
				return err
			}
			if _, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET partner_level = $1,
    updated_at = NOW()
WHERE user_id = $2`, grantedLevel.String, userID); err != nil {
				return fmt.Errorf("grant partner level: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, partnerApplicationSelectSQL()+`
WHERE app.id = $1
LIMIT 1`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("get reviewed partner application: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAffiliatePartnerApplicationNotFound
	}
	app, err := scanPartnerApplication(rows)
	if err != nil {
		return nil, err
	}
	return app, rows.Err()
}

// nullableArg unwraps a *float64 into an interface{} suitable for SQL parameter
// binding: nil pointer → SQL NULL, non-nil → the float value.
func nullableArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt64Arg(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableReviewerID(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func partnerApplicationSelectSQL() string {
	return `
SELECT app.id,
       app.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       app.requested_level,
       COALESCE(ua.partner_level, 'none') AS current_level,
       app.source,
       app.strengths,
       app.portal_url,
       app.status,
       COALESCE(app.granted_level, ''),
       COALESCE(app.review_note, ''),
       app.reviewer_id,
       app.reviewed_at,
       app.created_at,
       app.updated_at
FROM user_affiliate_partner_applications app
JOIN users u ON u.id = app.user_id
LEFT JOIN user_affiliates ua ON ua.user_id = app.user_id
`
}

func scanPartnerApplication(rows *sql.Rows) (*service.AffiliatePartnerApplication, error) {
	var app service.AffiliatePartnerApplication
	var reviewerID sql.NullInt64
	var reviewedAt sql.NullTime
	if err := rows.Scan(
		&app.ID,
		&app.UserID,
		&app.Email,
		&app.Username,
		&app.RequestedLevel,
		&app.CurrentLevel,
		&app.Source,
		&app.Strengths,
		&app.PortalURL,
		&app.Status,
		&app.GrantedLevel,
		&app.ReviewNote,
		&reviewerID,
		&reviewedAt,
		&app.CreatedAt,
		&app.UpdatedAt,
	); err != nil {
		return nil, err
	}
	app.RequestedLevel = service.NormalizeAffiliatePartnerLevel(app.RequestedLevel)
	app.RequestedTier = affiliatePartnerTierPtr(app.RequestedLevel)
	app.CurrentLevel = service.NormalizeAffiliatePartnerLevel(app.CurrentLevel)
	app.CurrentTier = affiliatePartnerTierPtr(app.CurrentLevel)
	app.GrantedLevel = strings.TrimSpace(app.GrantedLevel)
	if app.GrantedLevel != "" {
		app.GrantedLevel = service.NormalizeAffiliatePartnerLevel(app.GrantedLevel)
		app.GrantedTier = affiliatePartnerTierPtr(app.GrantedLevel)
	}
	if reviewerID.Valid {
		app.ReviewerID = &reviewerID.Int64
	}
	if reviewedAt.Valid {
		app.ReviewedAt = &reviewedAt.Time
	}
	return &app, nil
}

func affiliatePartnerTierPtr(level string) *service.AffiliatePartnerTier {
	tier, ok := service.AffiliatePartnerTierByLevel(level)
	if !ok {
		return nil
	}
	return &tier
}

// ListUsersWithCustomSettings 列出有专属配置（自定义码或专属比例）的用户。
//
// 单一查询同时处理"无搜索"与"按邮箱/用户名模糊搜索"：
// 空 search 时拼接出的 LIKE 模式为 "%%"，匹配所有行；非空时按 ILIKE 子串匹配。
// 这避免了为两种情况维护两份 SQL 模板。
func (r *affiliateRepository) ListUsersWithCustomSettings(ctx context.Context, filter service.AffiliateAdminFilter) ([]service.AffiliateAdminEntry, int64, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	likePattern := "%" + strings.TrimSpace(filter.Search) + "%"

	const baseFrom = `
FROM user_affiliates ua
JOIN users u ON u.id = ua.user_id
WHERE (ua.aff_code_custom = true OR ua.aff_rebate_rate_percent IS NOT NULL OR COALESCE(ua.partner_level, 'none') <> 'none')
  AND (u.email ILIKE $1 OR u.username ILIKE $1)`

	client := clientFromContext(ctx, r.client)

	total, err := scanInt64(ctx, client, "SELECT COUNT(*)"+baseFrom, likePattern)
	if err != nil {
		return nil, 0, fmt.Errorf("count affiliate admin entries: %w", err)
	}

	listQuery := `
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.aff_code,
       ua.aff_code_custom,
       ua.aff_rebate_rate_percent,
       COALESCE(ua.partner_level, 'none'),
       ua.aff_count` + baseFrom + `
ORDER BY ua.updated_at DESC
LIMIT $2 OFFSET $3`

	rows, err := client.QueryContext(ctx, listQuery, likePattern, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list affiliate admin entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]service.AffiliateAdminEntry, 0)
	for rows.Next() {
		var e service.AffiliateAdminEntry
		var rebate sql.NullFloat64
		if err := rows.Scan(&e.UserID, &e.Email, &e.Username, &e.AffCode,
			&e.AffCodeCustom, &rebate, &e.PartnerLevel, &e.AffCount); err != nil {
			return nil, 0, err
		}
		if rebate.Valid {
			v := rebate.Float64
			e.AffRebateRatePercent = &v
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// scanInt64 runs a query expected to return a single int64 column (e.g. COUNT).
func scanInt64(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (int64, error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	var v int64
	if err := rows.Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
