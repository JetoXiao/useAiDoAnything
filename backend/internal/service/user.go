package service

import (
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID                   int64
	Email                string
	Username             string
	Notes                string
	AvatarURL            string
	AvatarSource         string
	AvatarMIME           string
	AvatarByteSize       int
	AvatarSHA256         string
	PasswordHash         string
	Role                 string
	AdminMenuPermissions []string
	Balance              float64
	Concurrency          int
	Status               string
	AllowedGroups        []int64
	TokenVersion         int64 // Incremented on password change to invalidate existing tokens
	// TokenVersionResolved indicates TokenVersion already contains the fingerprint-derived
	// value expected in JWT claims and refresh-token state.
	TokenVersionResolved bool
	SignupSource         string
	LastLoginAt          *time.Time
	LastActiveAt         *time.Time
	LastUsedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time

	// GroupRates 用户专属分组倍率配置
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64

	// TOTP 双因素认证字段
	TotpSecretEncrypted *string    // AES-256-GCM 加密的 TOTP 密钥
	TotpEnabled         bool       // 是否启用 TOTP
	TotpEnabledAt       *time.Time // TOTP 启用时间

	// 余额不足通知
	BalanceNotifyEnabled             bool
	BalanceNotifyThresholdType       string // "fixed" (default) | "percentage"
	BalanceNotifyThreshold           *float64
	BalanceNotifyExtraEmails         []NotifyEmailEntry
	TotalRecharged                   float64
	AllowBalanceSubscriptionPurchase bool
	HelpCenterKeyPromptDismissed     bool

	// RPMLimit 用户级每分钟请求数上限（0 = 不限制）。仅在所用分组未设置 rpm_limit
	// 且该 (用户, 分组) 无 rpm_override 时作为全局兜底生效，计数键 rpm:u:{userID}:{min}。
	RPMLimit int

	// UserGroupRPMOverride 来自 auth cache snapshot 的 (user, group) RPM 覆盖值。
	// nil = 该 API Key 对应的 (user, group) 无 override；非 nil 时 checkRPM 直接使用，
	// 避免每请求查 DB。字段不持久化到数据库。
	UserGroupRPMOverride *int

	APIKeys       []APIKey
	Subscriptions []UserSubscription
}

func (u *User) IsAdmin() bool {
	return IsAdminRole(u.Role)
}

func (u *User) IsSuperAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsSubAdmin() bool {
	return u.Role == RoleSubAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

func IsAdminRole(role string) bool {
	return role == RoleAdmin || role == RoleSubAdmin
}

func IsValidUserRole(role string) bool {
	switch role {
	case RoleAdmin, RoleSubAdmin, RoleUser:
		return true
	default:
		return false
	}
}

func NormalizeUserRole(role string) string {
	switch role {
	case RoleAdmin, RoleSubAdmin, RoleUser:
		return role
	default:
		return RoleUser
	}
}

func NormalizeAdminMenuPermissions(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalized := strings.TrimSpace(item)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func NormalizeUserMenuPermissions(items []string) []string {
	normalized := NormalizeAdminMenuPermissions(items)
	if len(normalized) == 0 {
		return nil
	}
	out := make([]string, 0, len(normalized))
	for _, item := range normalized {
		if isUserMenuPermissionKey(item) || isCustomUserMenuPermissionKey(item) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func NormalizeSubAdminMenuPermissions(items []string) []string {
	normalized := NormalizeAdminMenuPermissions(items)
	if len(normalized) == 0 {
		return nil
	}
	out := make([]string, 0, len(normalized))
	for _, item := range normalized {
		if isSubAdminMenuPermissionKey(item) || isCustomAdminMenuPermissionKey(item) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func NormalizeMenuPermissionsForRole(role string, items []string) []string {
	switch NormalizeUserRole(role) {
	case RoleSubAdmin:
		return NormalizeSubAdminMenuPermissions(items)
	case RoleUser:
		return NormalizeUserMenuPermissions(items)
	default:
		return nil
	}
}

func isSubAdminMenuPermissionKey(item string) bool {
	switch item {
	case "admin_dashboard",
		"admin_visitor_analytics",
		"admin_download_resources",
		"admin_ops",
		"admin_ttft_analysis",
		"admin_response_cache",
		"admin_requests",
		"admin_users",
		"admin_groups",
		"admin_channel_pricing",
		"admin_channel_monitor",
		"admin_subscriptions",
		"admin_accounts",
		"admin_announcements",
		"admin_proxies",
		"admin_risk_control",
		"admin_redeem",
		"admin_promo_codes",
		"admin_affiliate_usage",
		"admin_affiliate_applications",
		"admin_affiliate_invites",
		"admin_affiliate_rebates",
		"admin_affiliate_transfers",
		"admin_order_dashboard",
		"admin_orders",
		"admin_order_plans",
		"admin_usage",
		"admin_settings":
		return true
	default:
		return false
	}
}

func isUserMenuPermissionKey(item string) bool {
	switch item {
	case "dashboard",
		"api_keys",
		"help_center",
		"image_generation",
		"usage",
		"channel_status",
		"subscriptions",
		"purchase",
		"orders",
		"redeem",
		"affiliate",
		"affiliate_usage",
		"second_level_agency",
		"support_contact",
		"profile":
		return true
	default:
		return false
	}
}

func isCustomUserMenuPermissionKey(item string) bool {
	const prefix = "custom:user:"
	return strings.HasPrefix(item, prefix) && strings.TrimSpace(strings.TrimPrefix(item, prefix)) != ""
}

func isCustomAdminMenuPermissionKey(item string) bool {
	const prefix = "custom:admin:"
	return strings.HasPrefix(item, prefix) && strings.TrimSpace(strings.TrimPrefix(item, prefix)) != ""
}

// CanBindGroup checks whether a user can bind to a given group.
// For standard groups:
// - Public groups (non-exclusive): all users can bind
// - Exclusive groups: only users with the group in AllowedGroups can bind
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	// 公开分组（非专属）：所有用户都可以绑定
	if !isExclusive {
		return true
	}
	// 专属分组：需要在 AllowedGroups 中
	for _, id := range u.AllowedGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
