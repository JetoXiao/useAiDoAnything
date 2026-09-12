package repository

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func compactSQLForTest(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func TestAffiliateUserOverviewSQLIncludesMaturedFrozenQuota(t *testing.T) {
	query := compactSQLForTest(affiliateUserOverviewSQL)

	require.Contains(t, query, "ua.aff_quota + COALESCE(matured.matured_frozen_quota, 0)")
	require.Contains(t, query, "frozen_until <= NOW()")
}

func TestListAgencyCapabilitiesIncludesAllFirstLevelPartners(t *testing.T) {
	query := compactSQLForTest(listAgencyCapabilitiesSQL)

	require.Contains(t, query, "FROM users u")
	require.Contains(t, query, "JOIN user_affiliates ua ON ua.user_id = u.id")
	require.Contains(t, query, "LEFT JOIN affiliate_agency_capabilities c ON c.root_partner_user_id = u.id")
	require.Contains(t, query, "COALESCE(NULLIF(ua.partner_level, ''), 'none') <> 'none'")
	require.Contains(t, query, "ua.aff_rebate_rate_percent IS NOT NULL")
	require.Contains(t, query, "c.root_partner_user_id IS NOT NULL")
	require.Contains(t, query, "COALESCE(c.enabled, FALSE)")
}

func TestAffiliateRecordQueriesUseLedgerAuditFields(t *testing.T) {
	source, err := os.ReadFile("affiliate_repo.go")
	require.NoError(t, err)
	content := string(source)

	require.Contains(t, content, "JOIN payment_orders po ON po.id = ual.source_order_id")
	require.Contains(t, content, "ual.amount::double precision")
	require.Contains(t, content, "ual.balance_after::double precision")
	require.NotContains(t, content, "parseAffiliateRebateAmount")
	require.NotContains(t, content, `"current_balance": "u.balance"`)
}

func TestAffiliateUsageCTEUsesOneToOneRMBRebateLedgerForProfitAndSettlements(t *testing.T) {
	cnyRechargeAmount := compactSQLForTest(affiliateUsageRechargeAmountCNYSQL)
	views := []string{"groups", "users"}

	for _, view := range views {
		t.Run(view, func(t *testing.T) {
			query, _ := buildAffiliateUsageCTE(service.AffiliateUsageFilter{
				View:                     view,
				DefaultRebateRatePercent: service.AffiliateUsageCommissionRateDefault,
				GroupProfitRates:         map[int64]float64{29: 60},
			})
			query = compactSQLForTest(query)

			require.Contains(t, query, "COALESCE(SUM("+cnyRechargeAmount+" * COALESCE(agpr.profit_rate_percent, 0) / 100), 0)::double precision AS net_profit")
			require.Contains(t, query, "'actual_cost', "+cnyRechargeAmount)
			require.Contains(t, query, "'requests', 1")
			require.Contains(t, query, "FROM payment_orders po JOIN usage_user_records uur ON uur.invitee_id = po.user_id")
			require.Contains(t, query, "WHERE po.status = 'COMPLETED' AND po.order_type = 'subscription'")
			require.Contains(t, query, "subscription_effective_orders AS")
			require.Contains(t, query, "GROUP BY po.user_id")
			require.NotContains(t, query, "po.paid_at + (GREATEST")
			require.Contains(t, query, "COALESCE(ubu.net_profit, 0) * COALESCE(")
			require.Contains(t, query, "detail.net_profit * COALESCE(")
			if view == "groups" {
				require.Contains(t, query, "COALESCE(SUM(detail.net_profit), 0) * uur.rebate_rate_percent / 100")
			}
			require.NotContains(t, query, "net_profit, 0) * er.usd_cny_rate")
			require.NotContains(t, query, "detail.net_profit * er.usd_cny_rate")
			require.NotContains(t, query, "MAX(er.usd_cny_rate) * uur.rebate_rate_percent")
			require.Contains(t, query, "COALESCE(spbu.rebate_amount, 0)")
		})
	}
}

func TestAffiliateUsageCTEAttributesSubscriptionRebatesByEffectiveMonth(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	query, _ := buildAffiliateUsageCTE(service.AffiliateUsageFilter{
		View:                     "groups",
		StartAt:                  &start,
		EndAt:                    &end,
		DefaultRebateRatePercent: service.AffiliateUsageCommissionRateDefault,
		GroupProfitRates:         map[int64]float64{29: 60},
	})
	query = compactSQLForTest(query)

	require.Contains(t, query, "WHERE po.status = 'COMPLETED' AND po.order_type = 'subscription'")
	require.Contains(t, query, "ROW_NUMBER() OVER (PARTITION BY po.user_id, po.subscription_group_id ORDER BY po.paid_at ASC, po.id ASC) AS subscription_order_number")
	require.Contains(t, query, "subscription_effective_orders AS")
	require.Contains(t, query, "GREATEST(sor.paid_at, seo.effective_end_at) AS effective_start_at")
	require.Contains(t, query, "po.paid_at < ((date_trunc('month', (($2 - INTERVAL '1 microsecond') AT TIME ZONE")
	require.Contains(t, query, "date_trunc('month', po.effective_start_at AT TIME ZONE")
	require.Contains(t, query, ">= date_trunc('month', $1 AT TIME ZONE")
	require.Contains(t, query, "< date_trunc('month', (($2 - INTERVAL '1 microsecond') AT TIME ZONE")
	require.Contains(t, query, "ORDER BY po.effective_start_at DESC")
	require.NotContains(t, query, "po.paid_at + (GREATEST")
}
