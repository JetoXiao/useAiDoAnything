package handler

import (
	"path/filepath"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
)

// ProvideAdminHandlers creates the AdminHandlers struct
func ProvideAdminHandlers(
	dashboardHandler *admin.DashboardHandler,
	userHandler *admin.UserHandler,
	groupHandler *admin.GroupHandler,
	accountHandler *admin.AccountHandler,
	announcementHandler *admin.AnnouncementHandler,
	dataManagementHandler *admin.DataManagementHandler,
	backupHandler *admin.BackupHandler,
	oauthHandler *admin.OAuthHandler,
	openaiOAuthHandler *admin.OpenAIOAuthHandler,
	geminiOAuthHandler *admin.GeminiOAuthHandler,
	antigravityOAuthHandler *admin.AntigravityOAuthHandler,
	proxyHandler *admin.ProxyHandler,
	redeemHandler *admin.RedeemHandler,
	promoHandler *admin.PromoHandler,
	settingHandler *admin.SettingHandler,
	opsHandler *admin.OpsHandler,
	systemHandler *admin.SystemHandler,
	subscriptionHandler *admin.SubscriptionHandler,
	usageHandler *admin.UsageHandler,
	userAttributeHandler *admin.UserAttributeHandler,
	errorPassthroughHandler *admin.ErrorPassthroughHandler,
	tlsFingerprintProfileHandler *admin.TLSFingerprintProfileHandler,
	apiKeyHandler *admin.AdminAPIKeyHandler,
	scheduledTestHandler *admin.ScheduledTestHandler,
	channelHandler *admin.ChannelHandler,
	channelMonitorHandler *admin.ChannelMonitorHandler,
	channelMonitorTemplateHandler *admin.ChannelMonitorRequestTemplateHandler,
	contentModerationHandler *admin.ContentModerationHandler,
	paymentHandler *admin.PaymentHandler,
	affiliateHandler *admin.AffiliateHandler,
	trialCardHandler *admin.TrialCardHandler,
	adminHelpCenterHandler *admin.HelpCenterHandler,
) *AdminHandlers {
	return &AdminHandlers{
		Dashboard:              dashboardHandler,
		User:                   userHandler,
		Group:                  groupHandler,
		Account:                accountHandler,
		Announcement:           announcementHandler,
		DataManagement:         dataManagementHandler,
		Backup:                 backupHandler,
		OAuth:                  oauthHandler,
		OpenAIOAuth:            openaiOAuthHandler,
		GeminiOAuth:            geminiOAuthHandler,
		AntigravityOAuth:       antigravityOAuthHandler,
		Proxy:                  proxyHandler,
		Redeem:                 redeemHandler,
		Promo:                  promoHandler,
		Setting:                settingHandler,
		Ops:                    opsHandler,
		System:                 systemHandler,
		Subscription:           subscriptionHandler,
		Usage:                  usageHandler,
		UserAttribute:          userAttributeHandler,
		ErrorPassthrough:       errorPassthroughHandler,
		TLSFingerprintProfile:  tlsFingerprintProfileHandler,
		APIKey:                 apiKeyHandler,
		ScheduledTest:          scheduledTestHandler,
		Channel:                channelHandler,
		ChannelMonitor:         channelMonitorHandler,
		ChannelMonitorTemplate: channelMonitorTemplateHandler,
		ContentModeration:      contentModerationHandler,
		Payment:                paymentHandler,
		Affiliate:              affiliateHandler,
		TrialCard:              trialCardHandler,
		HelpCenter:             adminHelpCenterHandler,
	}
}

// ProvideSystemHandler creates admin.SystemHandler with UpdateService
func ProvideSystemHandler(updateService *service.UpdateService, lockService *service.SystemOperationLockService) *admin.SystemHandler {
	return admin.NewSystemHandler(updateService, lockService)
}

// ProvideSettingHandler creates SettingHandler with version from BuildInfo
func ProvideSettingHandler(settingService *service.SettingService, buildInfo BuildInfo, notificationEmailService *service.NotificationEmailService) *SettingHandler {
	h := NewSettingHandler(settingService, buildInfo.Version)
	h.SetNotificationEmailService(notificationEmailService)
	return h
}

// ProvideAdminSettingHandler creates admin.SettingHandler with notification template APIs.
func ProvideAdminSettingHandler(settingService *service.SettingService, emailService *service.EmailService, turnstileService *service.TurnstileService, opsService *service.OpsService, paymentConfigService *service.PaymentConfigService, paymentService *service.PaymentService, userAttributeService *service.UserAttributeService, notificationEmailService *service.NotificationEmailService) *admin.SettingHandler {
	h := admin.NewSettingHandler(settingService, emailService, turnstileService, opsService, paymentConfigService, paymentService, userAttributeService)
	h.SetNotificationEmailService(notificationEmailService)
	return h
}

func ProvideAdminOpsHandler(opsService *service.OpsService, cfg *config.Config) *admin.OpsHandler {
	h := admin.NewOpsHandler(opsService)
	h.SetResponseCache(NewResponseCacheForAdmin(cfg))
	return h
}

func ProvideHelpCenterHandler(helpCenterService *service.HelpCenterService, userService *service.UserService, cfg *config.Config) *HelpCenterHandler {
	h := NewHelpCenterHandler(helpCenterService, userService)
	if cfg != nil {
		h.SetAttachmentsDir(helpCenterAttachmentsDir(cfg.Pricing.DataDir))
	}
	h.SetBundledAttachmentsDir(helpCenterBundledAttachmentsDir())
	return h
}

func ProvideAdminHelpCenterHandler(helpCenterService *service.HelpCenterService, cfg *config.Config) *admin.HelpCenterHandler {
	h := admin.NewHelpCenterHandler(helpCenterService)
	if cfg != nil {
		h.SetAttachmentsDir(helpCenterAttachmentsDir(cfg.Pricing.DataDir))
	}
	return h
}

func helpCenterAttachmentsDir(dataDir string) string {
	if dataDir == "" {
		dataDir = "./data"
	}
	return filepath.Join(dataDir, "help-center", "attachments")
}

func helpCenterBundledAttachmentsDir() string {
	return filepath.Join("resources", "help-center", "attachments")
}

func ProvideAdminUserHandler(adminService service.AdminService, concurrencyService *service.ConcurrencyService, affiliateService *service.AffiliateService) *admin.UserHandler {
	return admin.NewUserHandler(adminService, concurrencyService, affiliateService)
}

// ProvideHandlers creates the Handlers struct
func ProvideHandlers(
	authHandler *AuthHandler,
	userHandler *UserHandler,
	apiKeyHandler *APIKeyHandler,
	usageHandler *UsageHandler,
	redeemHandler *RedeemHandler,
	freeQuotaHandler *FreeQuotaHandler,
	subscriptionHandler *SubscriptionHandler,
	announcementHandler *AnnouncementHandler,
	channelMonitorUserHandler *ChannelMonitorUserHandler,
	adminHandlers *AdminHandlers,
	gatewayHandler *GatewayHandler,
	openaiGatewayHandler *OpenAIGatewayHandler,
	settingHandler *SettingHandler,
	totpHandler *TotpHandler,
	paymentHandler *PaymentHandler,
	paymentWebhookHandler *PaymentWebhookHandler,
	availableChannelHandler *AvailableChannelHandler,
	modelPricingHandler *ModelPricingHandler,
	modelMarketplaceHandler *ModelMarketplaceHandler,
	helpCenterHandler *HelpCenterHandler,
	visitorAnalyticsHandler *VisitorAnalyticsHandler,
	downloadResourcesHandler *DownloadResourcesHandler,
	_ *service.IdempotencyCoordinator,
	_ *service.IdempotencyCleanupService,
	secondLevelAgencyHandler *SecondLevelAgencyHandler,
) *Handlers {
	return &Handlers{
		Auth:              authHandler,
		User:              userHandler,
		APIKey:            apiKeyHandler,
		Usage:             usageHandler,
		Redeem:            redeemHandler,
		FreeQuota:         freeQuotaHandler,
		Subscription:      subscriptionHandler,
		Announcement:      announcementHandler,
		ChannelMonitor:    channelMonitorUserHandler,
		Admin:             adminHandlers,
		Gateway:           gatewayHandler,
		OpenAIGateway:     openaiGatewayHandler,
		Setting:           settingHandler,
		Totp:              totpHandler,
		Payment:           paymentHandler,
		PaymentWebhook:    paymentWebhookHandler,
		AvailableChannel:  availableChannelHandler,
		ModelPricing:      modelPricingHandler,
		ModelMarketplace:  modelMarketplaceHandler,
		HelpCenter:        helpCenterHandler,
		VisitorAnalytics:  visitorAnalyticsHandler,
		DownloadResources: downloadResourcesHandler,
		SecondLevelAgency: secondLevelAgencyHandler,
	}
}

// ProviderSet is the Wire provider set for all handlers
var ProviderSet = wire.NewSet(
	// Top-level handlers
	NewAuthHandler,
	NewUserHandler,
	NewAPIKeyHandler,
	NewUsageHandler,
	NewRedeemHandler,
	NewFreeQuotaHandler,
	NewSubscriptionHandler,
	NewAnnouncementHandler,
	NewChannelMonitorUserHandler,
	NewGatewayHandler,
	NewOpenAIGatewayHandler,
	NewTotpHandler,
	ProvideSettingHandler,
	NewPaymentHandler,
	NewPaymentWebhookHandler,
	NewAvailableChannelHandler,
	NewModelPricingHandler,
	NewModelMarketplaceHandler,
	ProvideHelpCenterHandler,
	NewVisitorAnalyticsHandler,
	NewDownloadResourcesHandler,
	NewSecondLevelAgencyHandler,

	// Admin handlers
	admin.NewDashboardHandler,
	ProvideAdminUserHandler,
	admin.NewGroupHandler,
	admin.NewAccountHandler,
	admin.NewAnnouncementHandler,
	admin.NewDataManagementHandler,
	admin.NewBackupHandler,
	admin.NewOAuthHandler,
	admin.NewOpenAIOAuthHandler,
	admin.NewGeminiOAuthHandler,
	admin.NewAntigravityOAuthHandler,
	admin.NewProxyHandler,
	admin.NewRedeemHandler,
	admin.NewPromoHandler,
	ProvideAdminSettingHandler,
	ProvideAdminOpsHandler,
	ProvideSystemHandler,
	admin.NewSubscriptionHandler,
	admin.NewUsageHandler,
	admin.NewUserAttributeHandler,
	admin.NewErrorPassthroughHandler,
	admin.NewTLSFingerprintProfileHandler,
	admin.NewAdminAPIKeyHandler,
	admin.NewScheduledTestHandler,
	admin.NewChannelHandler,
	admin.NewChannelMonitorHandler,
	admin.NewChannelMonitorRequestTemplateHandler,
	admin.NewContentModerationHandler,
	admin.NewPaymentHandler,
	admin.NewAffiliateHandler,
	admin.NewTrialCardHandler,
	ProvideAdminHelpCenterHandler,

	// AdminHandlers and Handlers constructors
	ProvideAdminHandlers,
	ProvideHandlers,
)
