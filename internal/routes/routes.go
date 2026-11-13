// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/components"
	"github.com/miabi-io/miabi/internal/config"
	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/healthprobe"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/services/announcement"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/billing"
	"github.com/miabi-io/miabi/internal/services/branding"
	"github.com/miabi-io/miabi/internal/services/certificate"
	"github.com/miabi-io/miabi/internal/services/cluster"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/controlmanager"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/customrole"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dbupgrade"
	"github.com/miabi-io/miabi/internal/services/directory"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/dockerimport"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/miabi-io/miabi/internal/services/environment"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/gitops"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/gpu"
	"github.com/miabi-io/miabi/internal/services/groupsync"
	"github.com/miabi-io/miabi/internal/services/housekeeping"
	"github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/job"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/locationmigration"
	"github.com/miabi-io/miabi/internal/services/logintoken"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/managedcert"
	"github.com/miabi-io/miabi/internal/services/marketplace"
	marketremote "github.com/miabi-io/miabi/internal/services/marketplace/remote"
	"github.com/miabi-io/miabi/internal/services/metadataguard"
	mwservice "github.com/miabi-io/miabi/internal/services/middleware"
	"github.com/miabi-io/miabi/internal/services/monitoring"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/nodestats"
	"github.com/miabi-io/miabi/internal/services/notify"
	"github.com/miabi-io/miabi/internal/services/oauth"
	"github.com/miabi-io/miabi/internal/services/organization"
	"github.com/miabi-io/miabi/internal/services/passkey"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/services/platformbackup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/services/portforward"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/recovery"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/services/registry"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	releasesvc "github.com/miabi-io/miabi/internal/services/release"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/miabi/internal/services/sealing"
	"github.com/miabi-io/miabi/internal/services/search"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/serviceaccount"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/services/storageclass"
	"github.com/miabi-io/miabi/internal/services/telemetry"
	"github.com/miabi-io/miabi/internal/services/transfer"
	"github.com/miabi-io/miabi/internal/services/twofactor"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
	"github.com/miabi-io/miabi/internal/services/usersettings"
	"github.com/miabi-io/miabi/internal/services/versioning"
	"github.com/miabi-io/miabi/internal/services/volumebackup"
	"github.com/miabi-io/miabi/internal/services/webhook"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/services/workspacekeys"
	"github.com/miabi-io/miabi/internal/services/wsbackup"
	"github.com/miabi-io/miabi/internal/siem"
	dbstorage "github.com/miabi-io/miabi/internal/storage"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/web"
	"github.com/miabi-io/miabi/internal/worker"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Router holds the app, config, versioned group, middleware, and handlers.
type Router struct {
	app *okapi.Okapi
	cfg *config.Config
	// Resolved at boot because the IPv6 answer depends on the engine, not only the environment.
	netInfo      handlers.NetworkingInfo
	v1           *okapi.Group
	authenticate okapi.Middleware
	scope        okapi.Middleware
	// scopeMode is the API-key scope enforcement posture applied to every route by register().
	scopeMode middlewares.ScopeMode
	// audit records scope violations so warn mode leaves evidence an operator can act on.
	audit            *audit.Logger
	systemAdmin      okapi.Middleware
	systemAdminRole  okapi.Middleware
	freshElevation   okapi.Middleware
	authRateLimit    okapi.Middleware
	ee               enterprise.EE
	resourcePolicies *repositories.ResourcePolicyRepository
	// Lenient so reconnect storms or nodes behind one NAT are not locked out; the long join token defeats brute force.
	agentRateLimit okapi.Middleware
	// Every node pushes 4 times a minute and a swarm's nodes usually share one egress IP.
	agentStatsRateLimit okapi.Middleware
	h                   routerHandlers
}

type routerHandlers struct {
	health          *handlers.HealthHandler
	auth            *handlers.AuthHandler
	apiKey          *handlers.APIKeyHandler
	serviceAccount  *handlers.ServiceAccountHandler
	workspace       *handlers.WorkspaceHandler
	location        *handlers.LocationHandler
	app             *handlers.ApplicationHandler
	network         *handlers.NetworkHandler
	stack           *handlers.StackHandler
	route           *handlers.RouteHandler
	domain          *handlers.DomainHandler
	dnsProvider     *handlers.DNSProviderHandler
	middleware      *handlers.MiddlewareHandler
	portBinding     *handlers.PortBindingHandler
	security        *handlers.SecurityHandler
	elevation       *handlers.ElevationHandler
	capability      *handlers.CapabilityHandler
	database        *handlers.DatabaseHandler
	job             *handlers.JobHandler
	secret          *handlers.SecretHandler
	config          *handlers.ConfigHandler
	search          *handlers.SearchHandler
	certificate     *handlers.CertificateHandler
	volume          *handlers.VolumeHandler
	backup          *handlers.BackupHandler
	backupSettings  *handlers.WorkspaceBackupSettingsHandler
	workspaceKeys   *handlers.WorkspaceKeysHandler
	volumeBackup    *handlers.VolumeBackupHandler
	workspaceBundle *handlers.WorkspaceBundleHandler
	wsMigration     *handlers.MigrationHandler
	migration       *handlers.LocationMigrationHandler
	monitoring      *handlers.MonitoringHandler
	analytics       *handlers.AnalyticsHandler
	inbox           *handlers.NotificationInboxHandler
	alerts          *handlers.AlertHandler
	marketplace     *handlers.MarketplaceHandler
	registry        *handlers.RegistryHandler
	gitRepo         *handlers.GitRepositoryHandler
	gitInspect      *handlers.GitInspectHandler
	apply           *handlers.ApplyHandler
	gitops          *handlers.GitOpsHandler
	pipeline        *handlers.PipelineHandler
	versioning      *handlers.VersioningHandler
	image           *handlers.ImageHandler
	environment     *handlers.EnvironmentHandler
	release         *handlers.ReleaseHandler
	events          *handlers.EventsHandler
	webhook         *handlers.WebhookHandler
	notification    *handlers.NotificationHandler
	node            *handlers.NodeHandler
	cluster         *handlers.ClusterHandler
	provider        *handlers.ProviderHandler
	runner          *handlers.RunnerHandler
	runnerGateway   *handlers.RunnerGatewayHandler
	usage           *handlers.UsageHandler

	adminUser           *handlers.AdminUserHandler
	adminWorkspace      *handlers.AdminWorkspaceHandler
	adminWorkspaceOps   *handlers.AdminWorkspaceOpsHandler
	adminDomain         *handlers.AdminDomainHandler
	adminRoute          *handlers.AdminRouteHandler
	adminMetrics        *handlers.AdminMetricsHandler
	adminEvent          *handlers.AdminEventHandler
	adminSetting        *handlers.AdminSettingHandler
	adminInfo           *handlers.AdminInfoHandler
	adminBilling        *handlers.AdminBillingHandler
	billing             *handlers.BillingHandler
	adminBranding       *handlers.AdminBrandingHandler
	register            *handlers.RegisterHandler
	update              *handlers.UpdateHandler
	telemetry           *handlers.TelemetryHandler
	adminPlan           *handlers.PlanHandler
	deploymentCfg       *handlers.DeploymentConfigHandler
	adminJob            *handlers.AdminJobHandler
	adminControlManager *handlers.AdminControlManagerHandler
	adminPlatformBackup *handlers.AdminPlatformBackupHandler
	adminRecovery       *handlers.AdminRecoveryHandler
	adminRegistry       *handlers.AdminRegistryHandler
	registryServer      *handlers.RegistryServerHandler
	oauthAdmin          *handlers.OAuthAdminHandler
	oauthPublic         *handlers.OAuthHandler
	license             *handlers.LicenseHandler
	components          *handlers.ComponentHandler
	adminOrganization   *handlers.AdminOrganizationHandler
	ssoAdmin            *handlers.SSOAdminHandler
	ldapAdmin           *handlers.LDAPAdminHandler
	permission          *handlers.PermissionHandler
	customRole          *handlers.CustomRoleHandler
	auditExport         *handlers.AuditExportHandler
	resourcePolicy      *handlers.ResourcePolicyHandler
	siemAdmin           *handlers.SIEMAdminHandler
	adminAnnouncement   *handlers.AdminAnnouncementHandler
	adminDatabaseSize   *handlers.AdminDatabaseSizeHandler
	adminStorageClass   *handlers.AdminStorageClassHandler
	adminRunner         *handlers.AdminRunnerHandler
}

// Returns the port-forward service so the caller can release live sessions on shutdown.
func InitRoutes(app *okapi.Okapi, db *gorm.DB, redisClient *redis.Client, cfg *config.Config, producer *worker.Producer, dockerClient docker.Client, nodeService *node.Service, nodeManager *nodes.Manager, nodeGateway *edgegateway.Service, clusterService *cluster.Service, bus *eventbus.Bus, proxyMgr proxy.Manager, cronManager *cronpkg.Manager, logStore *logstore.Store) (*portforward.Service, *runners.Dispatcher, *runners.Manager, *wsbackup.Service, *locationmigration.Service) {
	metrics.SetBuildInfo(config.Version, config.CommitID)

	userRepo := repositories.NewUserRepository(db)
	sessionRepo := repositories.NewSessionRepository(db)
	apiKeyRepo := repositories.NewAPIKeyRepository(db)
	workspaceRepo := repositories.NewWorkspaceRepository(db)
	auditRepo := repositories.NewAuditLogRepository(db)
	resetRepo := repositories.NewPasswordResetRepository(db)
	appRepo := repositories.NewApplicationRepository(db)
	deploymentRepo := repositories.NewDeploymentRepository(db)
	releaseRepo := repositories.NewReleaseRepository(db)
	volumeRepo := repositories.NewVolumeRepository(db)
	dbRepo := repositories.NewDatabaseRepository(db)
	quotaOverrideRepo := repositories.NewWorkspaceQuotaRepository(db)
	networkRepo := repositories.NewNetworkRepository(db)
	stackRepo := repositories.NewStackRepository(db)
	stackEnvRepo := repositories.NewStackEnvVarRepository(db)
	routeRepo := repositories.NewRouteRepository(db)
	middlewareRepo := repositories.NewMiddlewareRepository(db)
	appPortRepo := repositories.NewAppPortRepository(db)
	portBindingRepo := repositories.NewPortBindingRepository(db)
	appEventRepo := repositories.NewAppEventRepository(db)
	settingRepo := repositories.NewSettingRepository(db)
	legacyScope := ""
	if st, err := settingRepo.Get(middlewares.LegacyScopeModeSetting); err == nil && st != nil {
		legacyScope = st.Value
	}
	scopeMode, scopeWhy := middlewares.ResolveScopeMode(cfg.APIKeyScopeEnforcement, legacyScope)
	if scopeMode != middlewares.ScopeModeEnforce {
		logger.Warn("API key scopes are not enforced: keys can do more than their scopes allow. Review api_key.scope_violation audit entries, then set MIABI_API_KEY_SCOPE_ENFORCEMENT=enforce",
			"mode", scopeMode, "why", scopeWhy)
	}
	oauthRepo := repositories.NewOAuthProviderRepository(db)

	sessionStore := session.NewStore(redisClient)
	recoveryRepo := repositories.NewTwoFactorRecoveryRepository(db)
	authService := auth.NewService(userRepo, resetRepo, recoveryRepo, sessionStore, cfg.JWTSecret)
	settingsProvider := settings.NewProvider(settingRepo, map[string]string{
		settings.KeyRequireEmailVerification: cfg.RequireEmailVerification,
		settings.KeyAllowedSignupDomains:     cfg.AllowedSignupDomains,
		settings.KeyWorkspaceCreation:        cfg.WorkspaceCreation,
		settings.KeyInvitationSignupEnabled:  cfg.InvitationSignupEnabled,
		settings.KeyControlManagerMode:       cfg.ControlManagerMode,
	})
	// Pinned values are written back, so a console mode the env no longer sets would otherwise outlive it uncleared.
	if cfg.WorkspaceCreation != models.WorkspaceCreationConsole {
		if st, err := settingRepo.Get(settings.KeyWorkspaceCreation); err == nil && st.Value == models.WorkspaceCreationConsole {
			if err := settingRepo.BulkUpsert([]models.Setting{{Key: settings.KeyWorkspaceCreation, Value: settings.DefaultWorkspaceCreation, Type: models.SettingTypeString}}); err != nil {
				logger.Warn("failed to reset the workspace creation mode left by MIABI_WORKSPACE_CREATION=console", "error", err)
			}
			settingsProvider.Invalidate()
		}
	}
	workspaceCreationMode := func() string {
		m := settingsProvider.String(settings.KeyWorkspaceCreation, settings.DefaultWorkspaceCreation)
		if m == models.WorkspaceCreationConsole && cfg.WorkspaceCreation != models.WorkspaceCreationConsole {
			return settings.DefaultWorkspaceCreation
		}
		return m
	}
	oauthService := oauth.NewService(oauthRepo, userRepo, repositories.NewUserIdentityRepository(db), redisClient)
	oauthService.SetWorkspaces(workspaceRepo) // auto-join SSO users to a provider's default workspace
	orgRepo := repositories.NewOrganizationRepository(db)
	clusterRepo := repositories.NewClusterRepository(db)
	// One default org always exists, so this is wired in every edition.
	organizationService := organization.NewService(orgRepo)
	samlConfigRepo := repositories.NewSAMLConfigRepository(db)
	scimTokenRepo := repositories.NewSCIMTokenRepository(db)
	ldapRepo := repositories.NewLDAPRepository(db)
	customRoleRepo := repositories.NewCustomRoleRepository(db)
	customRoleService := customrole.NewService(customRoleRepo)
	resourcePolicyRepo := repositories.NewResourcePolicyRepository(db)
	siemConfigRepo := repositories.NewSIEMConfigRepository(db)
	// A license may be bound to this Install ID.
	installID, err := dbstorage.EnsureInstallID(db)
	if err != nil {
		logger.Warn("failed to ensure install id", "error", err)
	}
	// In Community builds this is the deny-all stub with no verification code linked.
	ee := enterprise.New(db, cfg.LicenseFile, cfg.DeploymentURL(), installID)
	// Dormant unless the siem_stream entitlement is present.
	siemStreamer := siem.NewStreamer(auditRepo, siemConfigRepo, ee)
	// Keeps auth/session out of the enterprise package; no-op in Community.
	ee.InitSSO(enterprise.SSODeps{
		DB:      db,
		BaseURL: cfg.ApiBaseURL,
		Decrypt: crypto.Decrypt, // read the LDAP bind password stored at rest
		Login: func(ctx context.Context, ident enterprise.SSOIdentity) (string, error) {
			// SAML identities carry no handle, so the username is derived from the email.
			user, err := oauthService.ProvisionSSOUser(ctx, ident.Email, ident.Name, "")
			if err != nil {
				return "", err
			}
			token, jti, err := authService.IssueToken(user)
			if err != nil {
				return "", err
			}
			_ = sessionRepo.Create(&models.Session{UserID: user.ID, JTI: jti, ExpiresAt: time.Now().Add(auth.TokenTTL)})
			now := time.Now()
			user.LastLoginAt = &now
			_ = userRepo.Update(user)
			return strings.TrimRight(cfg.AppWebURL, "/") + "/oauth/callback?token=" + token, nil
		},
	})
	// No-op in Community.
	directoryService := directory.NewService(ee, userRepo, workspaceRepo, ldapRepo)
	serverRepo := repositories.NewServerRepository(db)
	licenseNodeCount := func() int64 {
		n, err := serverRepo.CountLicensed()
		if err != nil {
			return 0
		}
		return n
	}
	// Enforcement is off by default (cfg.PlanEnforcement); when off, all checks pass.
	planRepo := repositories.NewPlanRepository(db)
	quotaService := quota.NewService(planRepo, quotaOverrideRepo, appRepo, volumeRepo, dbRepo, cfg.PlanEnforcement)
	quotaService.SetEdition(ee)                        // clamps Enterprise-only shell/security policies in CE
	quotaService.SetForceNonRoot(cfg.ForceNonRootUser) // server-level non-root mandate, on top of the plan
	quotaService.SetCPUOvercommit(cfg.QuotaCPUOvercommit)
	runnerService := runner.NewService(repositories.NewRunnerRepository(db))
	runnerService.SetQuota(quotaService)
	runnerService.SetImage(cfg.RunnerImage) // enrollment command shows the configured image (MIABI_RUNNER_IMAGE)
	runnerManager := runners.NewManager(runnerService)
	runnerService.SetScheduling(runnerManager, repositories.NewRunnerLeaseRepository(db))
	apiKeyService := auth.NewAPIKeyService(apiKeyRepo)
	billingService := billing.NewService(db, userRepo, apiKeyService, planRepo, settingRepo,
		billing.EnrolmentInfo{MiabiURL: cfg.DeploymentURL(), InstallID: installID, Version: config.Version})
	billingService.SetLocked(cfg.BillingConnectionLocked)
	billingAPIBase := cfg.ApiBaseURL
	if billingAPIBase == "" {
		billingAPIBase = strings.TrimRight(cfg.DeploymentURL(), "/") + "/api/v1"
	}
	billingService.SetSSO(oauthRepo, oauthService.ResolveEndpoints, billingAPIBase)
	billingService.SetConsoleOnly(func() bool {
		return workspaceCreationMode() == models.WorkspaceCreationConsole
	})
	apiKeyService.SetQuota(quotaService)
	// Use the web URL: --url is the panel root, and MIABI_API_URL already includes /api/v1.
	loginTokenServerURL := strings.TrimRight(cfg.AppWebURL, "/")
	if loginTokenServerURL == "" {
		loginTokenServerURL = strings.TrimSuffix(strings.TrimRight(cfg.ApiBaseURL, "/"), "/api/v1")
	}
	loginTokenService := logintoken.New(
		apiKeyService, redisClient, loginTokenServerURL,
		time.Duration(cfg.LoginTokenTTLHours)*time.Hour,
		time.Duration(cfg.LoginTokenMaxTTLHours)*time.Hour,
	)

	var ipv6Active bool
	subnetAllocator, err := netalloc.NewService(repositories.NewNetworkAllocationRepository(db), cfg.NetworkPoolCIDR, cfg.NetworkSubnetPrefix)
	if err != nil {
		logger.Warn("subnet allocator disabled; falling back to Docker's default address pool", "error", err)
		subnetAllocator = nil
	} else {
		// Best-effort reservation of existing subnets so the allocator never hands out an overlap.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = subnetAllocator.ImportExisting(ctx, dockerClient, 0)
		ipv6Active = ipv6Enabled(ctx, dockerClient, cfg)
		subnetAllocator.SetIPv6(ipv6Active, cfg.NetworkIPv6ULAPrefix)
		cancel()
	}
	networkService := network.NewService(networkRepo, dockerClient)
	networkService.SetQuota(quotaService)
	networkService.SetAllocator(subnetAllocator)
	// In cluster mode workspace networks are swarm overlays; existing bridges convert when cluster networking is enabled.
	networkService.SetCluster(clusterService)
	clusterService.SetNodeJoinedHook(func(ctx context.Context, _ uint, nodeName string, dc docker.Client) {
		networkService.PruneStaleBridges(ctx, dc, nodeName)
	})
	networkService.SetClients(nodeManager.Clients())
	networkService.SetMigrationDeps(serverRepo, repositories.NewDatabaseRepository(db))
	clusterService.SetNetworkMigrator(
		func(ctx context.Context) error { _, err := networkService.Migrate(ctx); return err },
		func(ctx context.Context) error { _, err := networkService.Rollback(ctx); return err },
		func() int { n, _ := networkService.PendingMigration(); return n },
	)
	workspaceService := workspace.NewService(workspaceRepo, userRepo, networkService)
	userSettingsService := usersettings.NewService(userRepo, repositories.NewUserSettingRepository(db), workspaceRepo)
	// A user's first workspace becomes their landing place without them finding a setting.
	workspaceService.SetDefaultAdopter(userSettingsService)

	workspaceService.SetPlans(planRepo)
	workspaceService.SetQuota(quotaService)
	// Per-user override (Enterprise) beats org cap beats platform default; fallback mirrors the seeded default.
	workspaceService.SetLimits(
		func() int {
			return settingsProvider.Int(settings.KeyMaxWorkspacesPerUser, settings.DefaultMaxWorkspacesPerUser)
		},
		func() bool { return ee.Has(enterprise.FlagUserWorkspaceLimit) },
	)
	workspaceService.SetConsoleURL(func() string {
		if conn, err := billingService.Connection(); err == nil {
			return conn.ConsoleURL
		}
		return ""
	})
	workspaceService.SetCreationPolicy(workspaceCreationMode)
	// The per-user override needs the Enterprise user_workspace_membership_limit entitlement.
	workspaceService.SetMembershipLimits(
		func() int {
			return settingsProvider.Int(settings.KeyMaxWorkspaceMembershipsPerUser, settings.DefaultMaxWorkspaceMembershipsPerUser)
		},
		func() bool { return ee.Has(enterprise.FlagUserWorkspaceMembershipLimit) },
	)
	// Over-limit users are just not auto-joined; directory/SCIM sync is exempt since it is authoritative.
	oauthService.SetMembershipGate(workspaceService.CanJoinAnother)
	// Crypto-shred a workspace's encryption keys on delete (reuses the live keyring).
	if sh, ok := crypto.CurrentKeyring().(workspace.KeyShredder); ok {
		workspaceService.SetKeyShredder(sh)
	}
	// Must run before EnsureSystem so the System workspace can be pinned to Unlimited.
	if err := dbstorage.SeedPlans(db); err != nil {
		logger.Warn("failed to seed default plans", "error", err)
	}
	// Seed the single default organization (identity realm). Idempotent.
	if _, err := dbstorage.SeedDefaultOrganization(db); err != nil {
		logger.Warn("failed to seed default organization", "error", err)
	}
	if admin, err := dbstorage.SeedAdmin(db, cfg.AdminEmail, cfg.AdminPassword, cfg.ValidateAdminSeed); err != nil {
		if errors.Is(err, dbstorage.ErrAdminSeedInvalid) {
			logger.Fatal("refusing to start without a platform admin", "error", err)
		}
		logger.Error("failed to seed admin user", "error", err)
	} else if admin != nil {
		if _, err := workspaceService.EnsureSystem(admin.ID); err != nil {
			logger.Error("failed to provision Miabi System workspace", "error", err)
		}
		// The default org predates every user, so its owner is the first platform admin, not the first sign-in.
		if err := dbstorage.AdoptDefaultOrganizationOwner(db, admin.ID); err != nil {
			logger.Warn("failed to set the default organization's owner", "error", err)
		}
	}
	auditLogger := audit.NewLogger(auditRepo, bus)
	groupSyncer := groupsync.New(workspaceRepo, userRepo, workspaceService.CanOwnAnother, func(userID uint, e groupsync.Event) {
		var wsID *uint
		if e.WorkspaceID != 0 {
			wsID = &e.WorkspaceID
		}
		auditLogger.Record(audit.Entry{ActorID: &userID, WorkspaceID: wsID, Action: e.Action, TargetType: "user",
			TargetID: strconv.FormatUint(uint64(userID), 10), Metadata: e.Detail})
	})
	oauthService.SetGroupSync(func(p *models.OAuthProvider, user *models.User, groups []string) {
		if !ee.Has(enterprise.FlagSSOGroupMapping) {
			return
		}
		mappings, err := oauthRepo.ListMappings(p.ID)
		if err != nil {
			logger.Warn("group sync: load mappings", "provider", p.Name, "error", err)
			return
		}
		rules := make([]groupsync.Rule, 0, len(mappings))
		for _, m := range mappings {
			rules = append(rules, groupsync.Rule{Group: m.Group, SystemAdmin: m.SystemAdmin, WorkspaceID: m.WorkspaceID, Role: m.WorkspaceRole})
		}
		groupSyncer.Reconcile(groupsync.Input{User: user, Manager: p.Name, Groups: groups, Rules: rules, Prefix: p.GroupPrefix})
	})
	eventsService := events.NewService(appEventRepo, bus)
	nodeClients := nodeManager.Clients()
	// GPUs are discovered by a probe over the Docker API, so no agent change is needed.
	gpuService := gpu.NewService(repositories.NewGPUDeviceRepository(db), serverRepo, appRepo, nodeClients, gpu.Config{
		Enabled:       cfg.GPUEnabled,
		NvidiaRuntime: cfg.NvidiaRuntime,
		ProbeImage:    cfg.GPUProbeImage,
	})
	gpuService.SetQuota(quotaService)
	appService := application.NewService(appRepo, deploymentRepo, releaseRepo, volumeRepo, routeRepo, networkRepo, stackRepo, appPortRepo, appEventRepo, nodeClients, producer, eventsService)
	// Lets the app service clean a deleted app's bindings.
	appService.SetPortBindings(portBindingRepo)
	storageService := storage.NewService(volumeRepo, appRepo, nodeClients)
	portBindingService := portbinding.NewService(portBindingRepo, appRepo, appPortRepo, workspaceRepo, cfg.HostPortMin, cfg.HostPortMax)
	// Checks ports actually published on the node, including non-Miabi containers.
	portBindingService.SetDocker(nodeClients)
	securityService := secpolicy.NewService(repositories.NewSecurityPolicyRepository(db), workspaceRepo, ee, cfg.SecurityPolicies)
	securityService.SetAudit(auditLogger)
	securityService.SetRedis(redisClient)
	go securityService.Listen(context.Background())
	portBindingService.SetPolicy(securityService)
	portBindingService.SetServers(serverRepo)
	// A pending request nobody is told about waits until somebody thinks to look.
	portBindingService.SetReviewNotifier(alerting.NewAdminNotifier(
		userRepo, repositories.NewNotificationInboxRepository(db), bus))
	elevationService := elevation.NewService(redisClient, securityService, authService, cfg.AdminUnlock)
	elevationService.SetNotifier(alerting.NewAdminNotifier(userRepo, repositories.NewNotificationInboxRepository(db), bus))
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyService, apiKeyRepo, workspaceRepo, auditLogger)
	apiKeyHandler.SetElevation(userRepo, elevationService)
	stackService := stack.NewService(stackRepo, appRepo, stackEnvRepo, appEventRepo, appService, storageService, dockerClient, portBindingService)
	stackService.SetAllocator(subnetAllocator)
	dockerImportService := dockerimport.NewService(nodeClients, appService, stackService, appRepo, releaseRepo, deploymentRepo, volumeRepo, networkRepo, stackRepo, portBindingRepo)
	dockerImportService.SetPortPolicy(securityService)
	// Node housekeeping: reclaim disk + reconcile drift between Docker and the DB.
	housekeepingService := housekeeping.NewService(nodeClients, appRepo, dbRepo, stackRepo, volumeRepo)
	routeService := route.NewService(routeRepo, middlewareRepo, appRepo, releaseRepo, serverRepo, proxyMgr)
	// Only route-exposed apps stay on the proxy network, reconciled live without a redeploy.
	proxyReconciler := worker.NewProxyNetworkReconciler(appRepo, releaseRepo, nodeClients)
	routeService.SetProxyAttacher(proxyReconciler)
	// Re-asserted on every refresh so a gateway recreate cannot leave clustered apps dark, and once now at boot.
	clusterService.SetIngressReconciler(proxyReconciler.ReconcileIngressGateway)
	// A swarm outside the default cluster is served by its own ingress node's gateway.
	routeService.SetCluster(clusterService)
	routeService.SetExternalDomains(clusterService)
	clusterService.SetGatewayListener(routeService.SyncCluster)
	clusterService.SetEndpointModeListener(appService.ApplyServiceEndpointMode)
	proxyReconciler.SetCluster(clusterService)
	go func() { _ = proxyReconciler.ReconcileIngressGateway(context.Background()) }()
	// A fresh install already ships a gateway on the manager (the one the Miabi stack installs).
	go func() {
		if local, err := serverRepo.FindLocal(); err == nil {
			edgegateway.AdoptCentral(context.Background(), dockerClient, nodeService, local)
		}
	}()
	// Imported gateways never receive the global key, so they serve routes they cannot decrypt; name those nodes.
	if list, lerr := serverRepo.List(); lerr == nil {
		edgegateway.WarnMissingConfigKey(cfg.GomaConfigEncryptionKey, list)
	}
	routeService.SetEdgeReloader(newEdgeReloader(nodeService, nodeGateway))
	// Canary traffic changes re-sync the proxy route without a redeploy.
	appService.SetRouteSyncer(routeService)
	// Enforce platform CPU/memory caps on app create/update.
	appService.SetSettings(settingsProvider)
	nodeService.SetNodeLimit(func() int { return ee.Entitlements().NodeLimit() })
	organizationService.SetClusters(clusterRepo)
	organizationService.SetWorkspaces(workspaceRepo)
	workspaceService.SetOrgs(organizationService)
	// Without the license a stored org cap goes inert rather than binding an operator who can no longer edit it.
	workspaceService.SetOrgLimitsEntitled(func() bool { return ee.Has(enterprise.FlagOrganizations) })
	// A cordon binds Miabi's placement; Swarm schedules service tasks itself and has to be told too.
	nodeService.SetSwarmCordon(clusterService.MirrorCordon)
	if cronManager != nil {
		cronManager.SetRecoveryPointGate(func() error { return ee.Require(enterprise.FlagRecoveryPoints) })
	}
	// Refuse new placements on cordoned/unknown nodes.
	appService.SetNodeGuard(nodeService)
	appService.SetServerInfo(nodeService)
	appService.SetNodeNamer(nodeService)       // resolve swarm node ids -> names for cluster placement display
	appService.SetWorkspaceInfo(workspaceRepo) // gate privileged host mounts
	appService.SetGitOpsNamer(repositories.NewGitSourceRepository(db))
	appService.SetQuota(quotaService)
	appService.SetClusterCap(clusterService) // gate "service" runtime apps on cluster mode
	// Names the organization a dedicated cluster belongs to, for the console's badge.
	clusterService.SetOrgLabels(organizationService.Labels)
	// Keeps the org default from naming hardware the org may no longer place in.
	clusterService.SetOrgDefaultAligner(organizationService.AlignDefaultCluster)
	housekeepingService.SetSwarmManagers(clusterService)
	housekeepingService.SetNodes(nodeService) // reach a node's swarm manager, to remove an orphaned service
	// Images named by apps, releases, databases or node gateways are never reclaimable however long unused.
	housekeepingService.SetImageRefs(appRepo, releaseRepo, dbRepo, serverRepo)
	// Redeploy uses the ordinary deploy path, which refuses an app whose data volume is gone.
	housekeepingService.SetRedeployer(appService, appRepo)
	appService.SetGrantsEnabled(cfg.ContainerGrantsEnabled)
	appService.SetNetworkEnsurer(networkService) // apps always join the workspace's default network (self-heals a missing one)
	storageService.SetNodeGuard(nodeService)
	storageService.SetServerInfo(nodeService)
	storageService.SetQuota(quotaService)
	storageService.SetWorkspacePrivilege(workspaceRepo) // gate host-path volumes on the privileged flag
	// The route service owns the per-workspace Goma file, so middlewares publish through it.
	middlewareService := mwservice.NewService(middlewareRepo, routeService)
	// New workspaces are seeded with a default security-policy set (best-effort).
	workspaceService.SetMiddlewareSeeder(middlewareService)
	backupRepo := repositories.NewBackupRepository(db)
	backupSetRepo := repositories.NewDatabaseBackupSetRepository(db)
	backupSettingsRepo := repositories.NewWorkspaceBackupSettingsRepository(db)
	volumeBackupRepo := repositories.NewVolumeBackupRepository(db)
	// Runs on the cron manager so only the leader sweeps; Miabi never restores a backup unattended.
	controlManager := controlmanager.New(nodeClients, clusterService, appRepo, releaseRepo, deploymentRepo, volumeRepo, dbRepo, eventsService, settingsProvider)
	controlManager.SetBackups(volumeBackupRepo, backupSetRepo)
	// Redeploys via the ordinary path so the app never lands on a cordoned node or runs missing data or configs.
	controlManager.SetEnforcement(appService, nodeService, repositories.NewConfigRepository(db), auditLogger)
	// Gateways are watched too
	controlManager.SetGateways(nodeService, nodeGateway.Ensurer(nodeService, nodeClients))
	if err := cronManager.RegisterTask("control-manager", 0, "Control manager sweep", "@every 1m", controlManager.Tick); err != nil {
		logger.Error("failed to register the control manager sweep", "error", err)
	}
	databaseService := database.NewService(dbRepo, nodeClients, producer)
	databaseService.SetEventBus(bus) // live status SSE; shares the bus with the embedded worker
	databaseService.SetEventRecorder(eventsService)
	databaseService.SetNodeGuard(nodeService)
	databaseService.SetServerInfo(nodeService)

	nodeService.SetOrphanHandler(func(serverID uint) {
		databaseService.MarkNodeRemoved(serverID)
		appService.MarkNodeRemoved(serverID)
	})
	databaseService.SetQuota(quotaService)
	databaseSizeRepo := repositories.NewDatabaseSizeRepository(db)
	databaseService.SetSizeCatalog(databaseSizeRepo)

	accountService := account.NewService(userRepo, workspaceRepo, appRepo, dbRepo, stackRepo, appService, databaseService, stackService, storageService)
	accountService.SetDeletionGuard(billingService.GuardWorkspaceDeletion)
	accountService.SetWorkspaceDeletedHook(billingService.WorkspaceDeleted)
	accountService.SetEventBus(bus)                        // live workspace-deletion progress SSE
	accountService.SetWorkspaceFinalizer(workspaceService) // row delete + crypto-shred after teardown
	restrictionService := restriction.NewService(workspaceRepo, accountService)
	restrictedWrite := func(wsID uint) error { return restrictionService.Allowed(wsID, restriction.ActionWrite) }
	restrictedDeploy := func(wsID uint) error { return restrictionService.Allowed(wsID, restriction.ActionDeploy) }
	suspended := func(wsID uint) bool { return restrictionService.Allowed(wsID, restriction.ActionRun) != nil }
	quotaService.SetRestriction(restrictedWrite)
	controlManager.SetSuspended(suspended)
	routeService.SetSuspended(suspended)
	restrictionService.SetOnChange(func(ctx context.Context, wsID uint) {
		if err := routeService.SyncWorkspaceProxy(ctx, wsID); err != nil {
			logger.Warn("re-sync routes after a workspace restriction change", "workspace", wsID, "error", err)
		}
	})
	restrictionService.SetBillingActor(billingService.ServiceUserID)
	if cfg.BillingAPIEnabled {
		for _, task := range []struct {
			name, desc, spec string
			run              func() error
		}{
			{"billing-idempotency-purge", "Delete expired Billing API idempotency keys", "17 * * * *", billingService.PurgeIdempotency},
			{"billing-sweep", "Recompute billing account levels and apply them to workspaces", "*/5 * * * *", func() error { return billingService.Sweep(context.Background()) }},
			{"billing-events", "Deliver billing events to the console", "* * * * *", func() error { return billingService.DeliverEvents(context.Background()) }},
			{"billing-events-purge", "Delete delivered billing events past retention", "41 3 * * *", billingService.PurgeEvents},
		} {
			if err := cronManager.RegisterTask(task.name, 0, task.desc, task.spec, task.run); err != nil {
				logger.Error("failed to register billing job", "job", task.name, "error", err)
			}
		}
	}
	// Daily purge of accounts whose deletion grace period has elapsed.
	if err := cronManager.RegisterTask("account-purge", 0, "Purge accounts past deletion grace", "0 2 * * *", func() error {
		accountService.PurgeDue(context.Background())
		return nil
	}); err != nil {
		logger.Error("failed to register account purge job", "error", err)
	}
	if err := cronManager.RegisterTask("workspace-restriction-expiry", 0, "Lift workspace restrictions past their end date", "*/5 * * * *", func() error {
		return restrictionService.ExpireDue(context.Background())
	}); err != nil {
		logger.Error("failed to register workspace restriction expiry job", "error", err)
	}

	// The minute is arbitrary but not :00 so installs do not all converge on one minute.
	updateService := updatecheck.NewService(db, config.Version, cfg.UpdateCheck)
	agentUpdateService := updatecheck.NewAgentService(db, cfg.UpdateCheck)
	runnerUpdateService := updatecheck.NewComponentService(db, updatecheck.ComponentRunner, cfg.UpdateCheck)
	updateChecker := updatecheck.NewChecker(config.Version, updateService, agentUpdateService, runnerUpdateService,
		updatecheck.NewComponentService(db, updatecheck.ComponentGateway, cfg.UpdateCheck))
	if cronManager != nil {
		registerUpdateCheck(cronManager, updateChecker)
	}
	telemetryService := telemetry.New(telemetry.Options{
		Version:   config.Version,
		InstallID: installID,
		Enabled:   cfg.Telemetry,
		Edition:   func() string { return ee.Entitlements().Edition },
		Platform: func() string {
			if clusterService != nil && clusterService.CapCluster() {
				return telemetry.PlatformSwarm
			}
			return telemetry.PlatformStandalone
		},
		Redis: redisClient,
	})
	registerTelemetry(cronManager, telemetryService)
	databaseService.SetNetworkProvider(networkService) // DBs run on the workspace's default network, alongside its apps
	databaseService.SetSwarmNetworks(clusterService)
	// A stale owner (already deleted) returns false and does not block.
	ownerExists := func(kind string, id, workspaceID uint) bool {
		switch kind {
		case models.OwnerApp:
			_, err := appRepo.FindInWorkspace(workspaceID, id)
			return err == nil
		case models.OwnerStack:
			_, err := stackRepo.FindInWorkspace(workspaceID, id)
			return err == nil
		case models.OwnerDatabase:
			_, err := dbRepo.FindInWorkspace(workspaceID, id)
			return err == nil
		default:
			return false
		}
	}
	storageService.SetOwnerExister(ownerExists)
	databaseService.SetOwnerExister(ownerExists)
	forwardService := portforward.NewService(portforward.Config{
		BindAddr:      cfg.ForwardBindAddr,
		AdvertiseHost: cfg.ForwardAdvertiseHost,
		RelayImage:    cfg.ForwardRelayImage,
		Network:       node.AppNetwork,
		TTL:           time.Duration(cfg.ForwardTTLMinutes) * time.Minute,
	}, nodeClients)
	secretService := secret.NewService(repositories.NewSecretRepository(db))
	sealingService := sealing.NewService(repositories.NewWorkspaceSealingKeyRepository(db))
	secretService.SetConsumers(appService)
	searchService := search.NewService(repositories.NewSearchRepository(db))
	configService := configsvc.NewService(repositories.NewConfigRepository(db))
	configService.SetConsumers(appService)
	appService.SetConfigs(configService)
	appService.SetStackEnv(stackEnvRepo)
	housekeepingService.SetConfigs(configService)
	databaseService.SetSecrets(secretService)

	certificateService := certificate.NewService(repositories.NewCertificateRepository(db))
	certificateService.SetRouteRefs(routeService)
	certificateService.SetQuota(quotaService)
	routeService.SetCertResolver(certificateService)
	// Daily TLS certificate expiry scan (logs warnings for certs within 30 days).
	if cronManager != nil {
		if err := cronManager.RegisterTask("certificate_expiry", 0, "TLS certificate expiry monitor", "0 8 * * *", func() error {
			_, e := certificateService.CheckExpiry()
			return e
		}); err != nil {
			logger.Warn("failed to schedule certificate expiry monitor", "error", err)
		}

		if dbSizeCron := strings.TrimSpace(cfg.DatabaseSizeCron); dbSizeCron != "" {
			if err := cronManager.RegisterTask("database_sizes", 0, "Measure database sizes", dbSizeCron, func() error {

				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
				defer cancel()
				_, err := databaseService.SyncAllSizes(ctx)
				return err
			}); err != nil {

				logger.Warn("failed to schedule the database size sweep — sizes will only refresh lazily",
					"cron", dbSizeCron, "error", err)
			}
		}

		// Decisions also live in the audit log with its own retention; this table only feeds the Security Center views.
		if err := cronManager.RegisterTask("security_events_prune", 0, "Security Center event prune", "15 3 * * *", func() error {
			n, err := securityService.PruneEvents(180 * 24 * time.Hour)
			if err == nil && n > 0 {
				logger.Info("pruned security events", "count", n)
			}
			return err
		}); err != nil {
			logger.Warn("failed to schedule the security event prune", "error", err)
		}
		if err := cronManager.RegisterTask("audit_prune", 0, "Audit log retention prune", "0 3 * * *", func() error {
			days := settingsProvider.Int(settings.KeyAuditLogRetentionDays, 0)
			if days <= 0 {
				return nil
			}
			n, err := auditRepo.DeleteOlderThan(time.Now().AddDate(0, 0, -days))
			if err == nil && n > 0 {
				logger.Info("pruned audit log entries", "count", n, "retention_days", days)
			}
			return err
		}); err != nil {
			logger.Warn("failed to schedule audit log prune", "error", err)
		}
		// A no-op unless the siem_stream entitlement is present.
		if err := cronManager.RegisterTask("siem_stream", 1, "SIEM audit streaming", "@every 30s", func() error {
			return siemStreamer.Tick(context.Background())
		}); err != nil {
			logger.Warn("failed to schedule SIEM streaming", "error", err)
		}
	}
	jobRepo := repositories.NewJobRepository(db)
	jobService := job.NewService(jobRepo, appRepo, releaseRepo, nodeClients, producer, 3600)
	jobService.SetPaused(suspended)
	jobService.SetQuota(quotaService)
	jobService.SetDeployments(deploymentRepo)
	jobService.SetScheduler(cronManager)
	jobService.LoadCronJobs()
	if cronManager != nil {
		if err := cronManager.RegisterTask("cronjob_sync", 0, "CronJob schedule sync", "@every 1m", jobService.SyncSchedules); err != nil {
			logger.Warn("failed to schedule cronjob sync", "error", err)
		}
	}
	backupService := backup.NewService(backupRepo, dbRepo, nodeClients)
	backupService.SetDDLRunner(databaseService) // force restore drops & recreates the DB

	databaseService.SetAppController(dbupgrade.AppController(appService))
	databaseService.SetLogicalBackup(dbupgrade.Backup(backupService))
	// Per-workspace shared S3 backup target (used by database & volume backups).
	backupSettingsService := backupsettings.NewService(backupSettingsRepo)
	// The operator identity on the sign-in page and in the console chrome.
	brandingService := branding.NewService(settingRepo, repositories.NewBrandAssetRepository(db))
	imageResolver := platformimage.New(settingsProvider, map[string]string{
		platformimage.KeyGoma:  cfg.NodeGatewayImage,
		platformimage.KeyRelay: cfg.ForwardRelayImage,
	})
	databaseService.SetImageResolver(imageResolver)
	// Reuses the port-forward relay image rather than adding another one to pull.
	clusterService.SetNetCheckImage(imageResolver, cfg.ForwardRelayImage)
	// The manager verifies each agent's self-reported swarm node id against its membership before trusting the shared token.
	clusterService.SetAgentDeps(
		nodeService,
		cfg.ControlURL,
		imageResolver,
		"miabi/agent:latest",
	)
	backupService.SetImageResolver(imageResolver)
	backupService.SetLogStore(logStore) // externalize backup run logs to the shared store
	backupService.SetEventRecorder(eventsService)
	backupService.SetSetRepository(backupSetRepo) // instance-wide recovery points
	// Volume backup: archives a volume to the workspace S3 target (volume-bkup).
	volumeBackupService := volumebackup.NewService(volumeBackupRepo, volumeRepo, nodeClients, cfg.ProxyNetwork)
	volumeBackupService.SetImageResolver(imageResolver)
	volumeBackupService.SetS3Provider(backupSettingsService)
	volumeBackupService.SetEnqueuer(producer) // run backups on the background worker
	volumeBackupService.SetInternalNetwork(cfg.InternalNetwork)
	volumeBackupService.SetLogStore(logStore)
	backupSettingsService.SetEnvelopeRotator(backupService, volumeBackupService)
	if cronManager != nil {
		cronManager.SetVolumeBackups(volumeBackupService)
	}

	pbHost, pbPort, pbName, pbUser, pbPass, pbSSL := cfg.Database.PostgresConn()
	platformBackupService := platformbackup.NewService(
		repositories.NewPlatformBackupRepository(db),
		repositories.NewPlatformBackupSetRepository(db),
		repositories.NewPlatformBackupSettingsRepository(db),
		nodeClients,
		platformbackup.DBConn{Host: pbHost, Port: pbPort, Name: pbName, User: pbUser, Password: pbPass, SSLMode: pbSSL},
		cfg.ProxyNetwork,
	)
	platformBackupService.SetInternalNetwork(cfg.InternalNetwork)
	platformBackupService.SetImageResolver(imageResolver)
	platformBackupService.SetEnqueuer(producer)
	platformBackupService.SetLogStore(logStore)
	platformBackupService.SetFingerprinter(crypto.DeriveToken)
	platformBackupService.SetKeyFingerprinter(crypto.DeriveTokenFrom)
	platformBackupService.SetEnv(cfg.PlatformBackup)
	// Reuses the per-database backup machinery against the platform bucket rather than duplicating it.
	platformBackupService.EnableTenantCapture(db, backupService)

	platformBackupService.SetAppStopper(platformbackup.NewMountAppStopper(appRepo, appService))
	forwardService.SetImageResolver(imageResolver)
	storageService.SetImageResolver(imageResolver)
	nodeStatsService := nodestats.NewService(nodeClients)
	nodeStatsService.SetImageResolver(imageResolver)
	nodeStatsService.SetServers(serverRepo)
	nodeStatsService.SetHostProc(cfg.HostProcPath)
	metadataGuard := metadataguard.NewService(nodeClients, serverRepo, cfg.MetadataGuard)
	metadataGuard.SetImageResolver(imageResolver)
	nodeManager.SetOnConnect(func(ctx context.Context, srv *models.Server, token string, dc docker.Client) {
		metadataGuard.OnConnect(ctx, srv, token, dc)
		networkService.PruneStaleBridges(ctx, dc, srv.Name)
	})
	if cronManager != nil {
		if err := cronManager.RegisterTask("metadata_guard", 0, "Keep containers off the cloud metadata service", "@every 30m", func() error {
			return metadataGuard.Sweep(context.Background())
		}); err != nil {
			logger.Warn("failed to register metadata guard task", "error", err)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_ = metadataGuard.Sweep(ctx)
		}()
	}
	nodeStatsService.SetAgentStats(cfg.NodeStatsAgent)
	// The built-in "default" class is seeded so every install, and every backfilled volume, has one.
	storageClassService := storageclass.NewService(repositories.NewStorageClassRepository(db), nodeClients)
	storageClassService.SetImageResolver(imageResolver)
	storageClassService.SetServerInfo(nodeService)
	if err := storageClassService.EnsureBuiltin(); err != nil {
		logger.Warn("failed to seed the built-in storage class", "error", err)
	}
	storageService.SetStorageClasses(storageClassService)
	// Database volumes follow the same placement rules as workspace volumes, plan binding included.
	databaseService.SetStorageClasses(storageService)

	go storageService.AuditSharedVolumes()
	go databaseService.AuditSharedNetworkInstances()
	monitoringService := monitoring.NewService(appRepo, releaseRepo, dbRepo, stackRepo, appEventRepo, repositories.NewMetricRepository(db), nodeClients)
	monitoringService.SetSwarmManager(clusterService)
	monitoringService.SetServerInfo(nodeService)
	placementService := placement.NewService(clusterRepo, serverRepo, nodeClients.Connected)
	placementService.SetPolicy(quotaService)

	placementService.SetOrgs(organizationService)
	placer := handlers.NewPlacer(placementService, userRepo)
	marketplaceService := marketplace.NewService(appService, databaseService, storageService, stackService, repositories.NewTemplateInstallRepository(db), repositories.NewTemplateRepository(db))
	marketplaceService.SetRoutes(routeService)
	marketplaceService.SetPlacer(placementService)
	dockerImportService.SetPlacer(placementService)
	marketplaceService.SetConfigs(configService)
	marketplaceService.SetEventBus(bus) // live install-progress SSE
	marketplaceRemote := marketremote.New(cfg.MarketplaceURL, marketremote.NewRedisCache(redisClient))
	marketplaceRemote.SetEntitlements(ee, config.OfficialMarketplaceURL)
	marketplaceService.SetRemote(marketplaceRemote)
	if marketplaceRemote.Enabled() {
		// Serve the cached bundle first so startup never blocks on the network.
		if err := marketplaceRemote.LoadCache(context.Background()); err != nil {
			logger.Warn("marketplace: failed to load cached bundle", "error", err)
		}
		go func() {
			if err := marketplaceRemote.Sync(context.Background()); err != nil {
				logger.Warn("marketplace: initial registry sync failed", "error", err)
			}
		}()
		if cronManager != nil {
			if err := cronManager.RegisterTask("marketplace", 1, "Marketplace registry sync", "@every 15m", func() error {
				return marketplaceRemote.Sync(context.Background())
			}); err != nil {
				logger.Warn("marketplace: failed to schedule registry sync", "error", err)
			}
		}
	}
	registryService := registry.NewService(repositories.NewRegistryRepository(db))
	registryService.SetSecrets(secretService)
	// Built-in Docker registry (distinct from the external-creds registryService).
	registryServerService := registryserver.NewService(
		repositories.NewRegistrySettingsRepository(db),
		imageResolver, clusterService, apiKeyService, workspaceRepo,
		proxyMgr, cfg.ProxyNetwork, cfg.ControlURL, cfg.Registry,
	)
	// S3 is env-only now, so the license is checked where the driver is used (boot, GC), not at an API.
	registryServerService.SetInternalNetwork(cfg.InternalNetwork)
	registryServerService.SetEntitlements(ee)
	registryServerService.SetStorageClasses(storageClassService)
	storageClassService.AddPlatformUser(registryServerService)
	// Holds a pipeline's app-bound registry token to that app's own repository.
	registryServerService.SetApps(appRepo)
	gitRepoRepo := repositories.NewGitRepoRepository(db)
	gitRepoService := gitrepo.NewService(gitRepoRepo)
	gitRepoService.SetSecrets(secretService) // same vault-reference support as registries
	domainRepo := repositories.NewDomainRepository(db)
	domainService := domain.NewService(domainRepo)
	dnsProviderService := dnsprovider.NewService(repositories.NewDNSProviderRepository(db), repositories.NewDNSRecordRepository(db), domainRepo)
	dnsProviderService.SetQuota(quotaService)
	// Automate the ownership TXT (and ledger cleanup) for provider-connected domains.
	domainService.SetDNSAutomator(dnsProviderService)
	// Domains under a console-pushed zone connect to that provider and verify themselves.
	domainService.SetConsoleDNS(dnsProviderService)
	billingService.SetConsoleDNS(dnsProviderService, domainService)
	// Routes go live on verify or offline on drift automatically.
	domainService.SetProxyResyncer(routeService)
	// Keep app A/AAAA/CNAME records in sync with routed hosts.
	routeService.SetDNSAddresser(dnsProviderService)
	renewWithin := time.Duration(cfg.CertRenewDays) * 24 * time.Hour
	if renewWithin <= 0 {
		renewWithin = 30 * 24 * time.Hour
	}
	managedCertService := managedcert.NewService(
		certificateService, dnsProviderService, domainRepo,
		repositories.NewACMEAccountRepository(db),
		cfg.AcmeEmail, cfg.ACMEDirectoryURL, renewWithin,
	)
	managedCertService.SetQuota(quotaService)
	if cronManager != nil {
		if err := cronManager.RegisterTask("cert_renew", 1, "Managed certificate auto-renew", "0 6 * * *", func() error {
			managedCertService.RenewDue()
			return nil
		}); err != nil {
			logger.Warn("failed to register cert renew task", "error", err)
		}
	}
	if cronManager != nil {
		reconcileEvery := cfg.DNSReconcileMinutes
		if reconcileEvery <= 0 {
			reconcileEvery = 30
		}
		if err := cronManager.RegisterTask("dns_reconcile", 1, "DNS managed-record reconcile", fmt.Sprintf("@every %dm", reconcileEvery), func() error {
			return dnsProviderService.Reconcile(context.Background())
		}); err != nil {
			logger.Warn("failed to register dns reconcile task", "error", err)
		}
		if err := cronManager.RegisterTask("domain_reverify", 1, "Domain ownership drift re-verification", fmt.Sprintf("@every %dm", reconcileEvery), func() error {
			return domainService.Reverify(context.Background())
		}); err != nil {
			logger.Warn("failed to register domain reverify task", "error", err)
		}
	}
	// Cached so the UI shows declared-vs-used without a live `docker system df` per read.
	if cfg.StorageUsageEnabled && cronManager != nil {
		usageEvery := cfg.StorageUsageMinutes
		if usageEvery <= 0 {
			usageEvery = 30
		}
		if err := cronManager.RegisterTask("storage_usage", 0, "Measure volume disk usage", fmt.Sprintf("@every %dm", usageEvery), func() error {
			return storageService.MeasureUsage(context.Background())
		}); err != nil {
			logger.Warn("failed to register storage usage task", "error", err)
		}
		// Stored per node so fleet figures read the database instead of fanning out to every node.
		if err := cronManager.RegisterTask("node_capacity", 0, "Measure node capacity and load", fmt.Sprintf("@every %dm", usageEvery), func() error {
			return nodeStatsService.Sweep(context.Background())
		}); err != nil {
			logger.Warn("failed to register node capacity task", "error", err)
		}
		if err := cronManager.RegisterTask("storage_class_capacity", 0, "Measure storage class capacity", fmt.Sprintf("@every %dm", usageEvery), func() error {
			return storageClassService.MeasureCapacity(context.Background())
		}); err != nil {
			logger.Warn("failed to register storage class capacity task", "error", err)
		}
		// Seed after boot so a fresh install shows numbers without waiting a full interval.
		go func() {
			time.Sleep(30 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_ = storageService.MeasureUsage(ctx)
			_ = nodeStatsService.Sweep(ctx)
		}()
	}

	// Only when GPU support is on, so a no-GPU fleet pays nothing.
	if cfg.GPUEnabled && cronManager != nil {
		gpuEvery := cfg.GPUInventoryMinutes
		if gpuEvery <= 0 {
			gpuEvery = 30
		}
		if err := cronManager.RegisterTask("gpu_inventory", 0, "GPU device inventory", fmt.Sprintf("@every %dm", gpuEvery), func() error {
			return gpuService.Inventory(context.Background())
		}); err != nil {
			logger.Warn("failed to register gpu inventory task", "error", err)
		}
		go func() {
			time.Sleep(45 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_ = gpuService.Inventory(ctx)
		}()
	}
	// Generated external-access routes bypass route.Service.Create, so they are exempt.
	routeService.SetDomains(domainRepo)
	// A banned domain is still blocked.
	routeService.SetWorkspacePolicy(workspaceRepo)
	// Same signal, read-only: the domains UI reports which unverified domains serve anyway.
	domainService.SetWorkspacePolicy(workspaceRepo)
	// Gate custom-certificate imports on the workspace's registered domains.
	certificateService.SetDomains(domainRepo)
	// Mirrors the route serve gate: a privileged workspace may serve, so may hold a cert for, an unverified domain.
	certificateService.SetWorkspacePrivilege(workspaceRepo)
	// A cluster may serve its generated URLs from one certificate in the system workspace, usually a wildcard.
	certificateService.SetSystemWorkspace(workspaceRepo)
	clusterService.SetExternalCertificates(certificateService)
	routeService.SetExternalCertificates(clusterService)
	certificateService.SetClusterRefs(func(certID uint) ([]string, error) {
		list, err := clusterRepo.ListByExternalCertificate(certID)
		names := make([]string, 0, len(list))
		for _, c := range list {
			names = append(names, c.Name)
		}
		return names, err
	})
	certificateService.SetOnChange(func(cert *models.Certificate) {
		list, err := clusterRepo.ListByExternalCertificate(cert.ID)
		if err != nil {
			logger.Warn("re-rendering generated URLs after a certificate change failed", "certificate", cert.ID, "error", err)
			return
		}
		for _, c := range list {
			clusterService.ResyncRoutes(context.Background(), c.ID)
		}
	})
	// Declarative apply engine (shared by the one-shot apply API and GitOps).
	applyService := apply.NewService(appService, storageService, databaseService, stackService, secretService, routeService, domainService, registryService)
	applyService.SetUnsealer(sealingService)
	applyService.SetConfigs(configService)
	applyService.SetMiddlewares(middlewareService)
	applyService.SetJobs(jobService)
	applyService.SetCluster(clusterService)
	applyService.SetPlacer(placementService)
	applyService.SetCertificates(certificateService)
	applyService.SetPortExposure(portBindingService)
	backupPolicies := apply.BackupPolicies{Schedules: backupRepo, Sets: backupSetRepo, Target: backupSettingsService, Entitlements: ee}
	if cronManager != nil {
		backupPolicies.Cron = cronManager
	}
	applyService.SetBackupPolicies(backupPolicies)
	// Wired once every route service dependency is, since a pinned domain that changed re-hosts routes right away.
	clusterService.SetExternalAccessListener(func(ctx context.Context, clusterID uint) {
		routeService.RehostCluster(ctx, clusterID)
		if c, err := clusterService.Cluster(clusterID); err == nil && c.IsDefault {
			if err := registryServerService.SyncGateway(ctx); err != nil {
				logger.Warn("registry: re-publishing the gateway route failed", "error", err)
			}
		}
	})
	if err := clusterService.PinExternalAccess(cfg.ExternalBaseDomain, cfg.ExternalBaseProvider); err != nil {
		logger.Error("external access from the environment was not applied", "error", err)
	}
	// GitOps: reconcile a repo of miabi.io/v1 manifests via the apply engine.
	gitopsService := gitops.NewService(repositories.NewGitSourceRepository(db), gitRepoRepo, applyService)
	gitopsService.SetRestriction(restrictedDeploy)
	gitopsService.SetSecrets(secretService) // resolve a vault-backed Git credential when fetching
	applyService.SetGitSources(gitopsService)
	applyService.SetGitRepositories(gitRepoService)
	// CI/CD pipelines: definitions + runs on the internal runner (worker).
	pipelineService := pipeline.NewService(repositories.NewPipelineRepository(db), producer)
	pipelineService.SetRestriction(restrictedDeploy)
	pipelineService.SetGitRepos(gitRepoService)
	pipelineService.SetApps(appRepo)
	// Wired here because the two services are mutually dependent.
	appService.SetPipelines(pipelineService)
	// Built-image catalog: provenance written by pipeline builds; list + GC here.
	imageService := image.NewService(repositories.NewImageRepository(db), releaseRepo)
	// Deleting a tag must not pull an image held by a live deployment or pinned release, the same set GC exempts.
	registryServerService.SetCatalog(imageService)
	// Without the sealed identity a dump on a fresh host is ciphertext under a key that died with the old machine.
	platformBackupService.SetIdentitySource(func() (*dr.Identity, error) {
		id := &dr.Identity{
			InstallID:           installID,
			MiabiVersion:        config.Version,
			DBSchema:            dbstorage.SchemaVersion(db),
			EncryptionKey:       cfg.EncryptionKey,
			JWTSecret:           cfg.JWTSecret,
			Domain:              urlHost(cfg.AppWebURL),
			WebURL:              cfg.AppWebURL,
			ControlURL:          cfg.ControlURL,
			NetworkName:         cfg.ProxyNetwork,
			InternalNetworkName: cfg.InternalNetwork,
			CreatedAt:           time.Now().UTC(),
		}
		if st, err := registryServerService.Get(); err == nil && st != nil {
			id.RegistryHost = registryServerService.HostFor(st)
			id.RegistryStorage = st.StorageType
		}
		return id, nil
	})
	// Same boundary the deploy worker enforces, surfaced as a form validation error instead of a failed deploy.
	appService.SetImageGuard(registryServerService)
	// Versions are reserved before queueing; the digest is tagged only once the run succeeds.
	versionService := versioning.NewService(repositories.NewVersionRepository(db), appRepo, repositories.NewPipelineRepository(db))
	versionService.SetRegistry(registryServerService)
	versionService.SetImages(imageService)
	pipelineService.SetVersions(versionService)
	// Returned so the caller can wire it into the pipeline worker, which holds the tunnels.
	runnerMinter := runners.NewCredentialMinter(apiKeyService, cfg.JobAPITokenEnabled)
	runnerDispatcher := runners.NewDispatcher(
		runnerService, runnerManager, runnerMinter,
		repositories.NewPipelineRepository(db), imageService, bus,
		time.Duration(cfg.BuildTimeoutMinutes)*time.Minute,
	)
	// Full logs are externalized so a finished run's logs outlive the live SSE stream.
	runnerDispatcher.SetLogStore(logStore)
	runnerDispatcher.SetDeployer(worker.NewPipelineDeployer(appRepo, deploymentRepo, producer))
	// Pipeline env may reference workspace secrets; resolved on this side, never by a runner.
	runnerDispatcher.SetSecrets(secretService)
	runnerDispatcher.SetVersions(versionService)
	// Promotion environments + release catalog/approval gates.
	environmentRepo := repositories.NewEnvironmentRepository(db)
	environmentService := environment.NewService(environmentRepo)
	releaseService := releasesvc.NewService(releaseRepo, appRepo, environmentRepo, appService)
	// Drives the platform's own create paths, hence the wide dependency set.
	wsBundleService := wsbackup.NewService(wsbackup.Deps{
		Repo:        repositories.NewWorkspaceBundleRepository(db),
		Apps:        appRepo,
		Users:       userRepo,
		Workspaces:  workspaceRepo,
		Settings:    backupSettingsService,
		Workspace:   workspaceService,
		App:         appService,
		Volume:      storageService,
		Database:    databaseService,
		Secret:      secretService,
		Sealing:     sealingService,
		Config:      configService,
		Route:       routeService,
		Middleware:  middlewareService,
		Domain:      domainService,
		DNSProvider: dnsProviderService,
		Certificate: certificateService,
		Stack:       stackService,
		Network:     networkService,
		Registry:    registryService,
		GitRepo:     gitRepoService,
		GitOps:      gitopsService,
		Pipeline:    pipelineService,
		Versions:    versionService,
		Environment: environmentService,
		Jobs:        jobService,
		Backup:      backupService,
		Clients:     nodeClients,
		Images:      imageResolver,
		InstallID:   installID,
		Version:     config.Version,
		Placer:      placementService,
		Freezer:     restrictionService,
		PublicURL:   cfg.DeploymentURL(),
	})
	wsBundleService.SetEnqueuer(producer) // export/restore runs on the worker
	if cfg.MigrationEnabled && cronManager != nil {
		if err := cronManager.RegisterTask("migration-export-sweep", 0, "Purge migration exports nobody collected in time", "*/30 * * * *", func() error {
			return wsBundleService.SweepMigrationExports(context.Background())
		}); err != nil {
			logger.Error("failed to register migration export sweep", "error", err)
		}
	}

	transferService := transfer.New(nodeClients,
		func() string { return imageResolver.Ref(platformimage.KeySync) },
		func() string { return imageResolver.Ref(platformimage.KeyRelay) })
	migrationService := locationmigration.NewService(locationmigration.Deps{
		Repo:        repositories.NewLocationMigrationRepository(db),
		Apps:        appRepo,
		VolumeRepo:  volumeRepo,
		ClusterRepo: clusterRepo,
		App:         appService,
		Volumes:     storageService,
		Databases:   databaseService,
		Routes:      routeService,
		Placer:      placementService,
		Clusters:    clusterService,
		Transfer:    transferService,
		Clients:     nodeClients,
		Bus:         bus,
	})
	migrationService.SetEnqueuer(producer)
	if cronManager != nil {
		if err := cronManager.RegisterTask("location_migration_sweep", 0, "Location migration sweep", "* * * * *", func() error {
			migrationService.Sweep(context.Background())
			return nil
		}); err != nil {
			logger.Warn("failed to schedule the location migration sweep", "error", err)
		}
	}

	// GitOps auto-sync sweep for sources set to automatic reconciliation.
	if cronManager != nil {
		if err := cronManager.RegisterTask("gitops_sync", 0, "GitOps auto-sync sweep", "*/3 * * * *", func() error {
			return gitopsService.ReconcileAuto(context.Background())
		}); err != nil {
			logger.Warn("failed to schedule GitOps auto-sync sweep", "error", err)
		}
		if err := cronManager.RegisterTask("image_gc", 0, "Image catalog GC", "0 4 * * *", func() error {
			_, err := imageService.GC(context.Background(), image.RetentionPolicy{}, dockerClient)
			return err
		}); err != nil {
			logger.Warn("failed to schedule image catalog GC", "error", err)
		}
		pipelineService.SetScheduler(pipelineCronScheduler{cron: cronManager, svc: pipelineService})
	}
	webhookRepo := repositories.NewWebhookRepository(db)
	webhookService := webhook.NewService(webhookRepo, repositories.NewWebhookDeliveryRepository(db), producer)
	notificationService := notify.NewService(repositories.NewNotificationChannelRepository(db), notify.NewRegistry())
	// A no-op until MIABI_SMTP_* is configured.
	platformMailer := mailer.NewService(cfg.SystemSMTP, cfg.AppName, cfg.AppWebURL)
	// Takes the mailer so it can refuse sign-up that needs a verification email nobody can send.
	registrationService := registration.NewService(cfg.RegistrationEnabled, settingsProvider, platformMailer)

	var keyRotator *keyring.Service
	if kr, ok := crypto.CurrentKeyring().(*keyring.Service); ok {
		kr.Register(secretService)
		kr.Register(configService)
		kr.Register(certificateService)
		kr.Register(dnsProviderService)
		kr.Register(registryService)
		kr.Register(gitRepoService)
		kr.Register(webhookService)
		kr.Register(storageService)
		kr.Register(databaseService)
		kr.Register(notificationService)
		kr.Register(appService)
		kr.Register(stackService)
		kr.Register(backupSettingsService)
		kr.Register(middlewareService)
		keyRotator = kr
		if cronManager != nil && cfg.KeyAutoRotate {
			months := cfg.KeyRotateMonths
			if months <= 0 {
				months = 6
			}
			older := time.Duration(months) * 30 * 24 * time.Hour
			if err := cronManager.RegisterTask("key_rotate", 1, "Per-workspace encryption-key auto-rotate", "0 4 * * *", func() error {
				return kr.AutoRotateDue(context.Background(), older)
			}); err != nil {
				logger.Warn("failed to register key rotate task", "error", err)
			}
		}
	}
	// A nil *keyring.Service must stay a nil interface, or Rotate would run against it.
	var deks workspacekeys.DEKs
	if keyRotator != nil {
		deks = keyRotator
	}
	workspaceKeysService := workspacekeys.NewService(deks, repositories.NewWorkspaceKeyRepository(db), sealingService, repositories.NewSecretRepository(db))

	jwtAuth := middlewares.JWTAuth(cfg, sessionStore, userRepo)

	// Shares the alerting inbox so bell, SSE and notifications page need no announcement-specific handling.
	announcementRepo := repositories.NewAnnouncementRepository(db)
	inboxRepo := repositories.NewNotificationInboxRepository(db)
	announcementService := announcement.NewService(announcementRepo, inboxRepo, bus)
	if cronManager != nil {
		// Expiry is evaluated on every read, so a lapsed notice stops showing on time regardless of this tick.
		if err := cronManager.RegisterTask("announcements", 0, "Publish scheduled announcements", "@every 1m", func() error {
			return announcementService.Tick()
		}); err != nil {
			logger.Warn("failed to schedule announcement publishing", "error", err)
		}
	}

	r := &Router{
		app: app,
		cfg: cfg,
		netInfo: handlers.NetworkingInfo{
			IPv6Enabled:   cfg.NetworkIPv6,
			IPv6Active:    ipv6Active,
			IPv6ULAPrefix: cfg.NetworkIPv6ULAPrefix,
			PoolCIDR:      cfg.NetworkPoolCIDR,
			SubnetPrefix:  cfg.NetworkSubnetPrefix,
			ProxyNetwork:  cfg.ProxyNetwork,
		},
		v1:              app.Group("/api/v1"),
		authenticate:    middlewares.Authenticate(jwtAuth, apiKeyService, userRepo, appRepo),
		scope:           middlewares.WorkspaceScope(workspaceRepo, customRoleRepo, restrictionService, appRepo),
		scopeMode:       scopeMode,
		audit:           auditLogger,
		systemAdmin:     middlewares.RequireSystemAdmin(userRepo, elevationService),
		systemAdminRole: middlewares.RequireSystemAdmin(userRepo, nil),
		freshElevation:  middlewares.RequireFreshElevation(elevationService),
		// Auth falls back to a local limiter if Redis is down; agent tunnels fail open (availability over throttling).
		authRateLimit:       middlewares.RateLimit(redisClient, 10, time.Minute, true),
		agentRateLimit:      middlewares.RateLimit(redisClient, 120, time.Minute, false),
		agentStatsRateLimit: middlewares.RateLimit(redisClient, 2400, time.Minute, false),
		ee:                  ee,
		resourcePolicies:    resourcePolicyRepo,
		h: routerHandlers{
			health:          handlers.NewHealthHandler(db, redisClient, dockerClient),
			auth:            handlers.NewAuthHandler(authService, userRepo, sessionRepo, auditLogger, settingsProvider, cfg.DevMode, cfg.PasswordResetEnabled),
			apiKey:          apiKeyHandler,
			serviceAccount:  handlers.NewServiceAccountHandler(serviceaccount.NewService(userRepo, workspaceRepo, apiKeyService, apiKeyRepo), auditLogger),
			usage:           handlers.NewUsageHandler(quotaService, appRepo, dbRepo, volumeRepo, networkRepo, jobRepo, apiKeyRepo, workspaceRepo, repositories.NewRunnerRepository(db)),
			workspace:       handlers.NewWorkspaceHandler(workspaceService, accountService, auditRepo, userRepo, auditLogger, ee),
			location:        handlers.NewLocationHandler(placer, auditLogger),
			app:             handlers.NewApplicationHandler(appService, bus, auditLogger, ee),
			network:         handlers.NewNetworkHandler(networkService, auditLogger),
			stack:           handlers.NewStackHandler(stackService, auditLogger),
			route:           handlers.NewRouteHandler(routeService, auditLogger),
			domain:          handlers.NewDomainHandler(domainService, routeRepo, auditLogger),
			dnsProvider:     handlers.NewDNSProviderHandler(dnsProviderService, auditLogger),
			middleware:      handlers.NewMiddlewareHandler(middlewareService, auditLogger),
			portBinding:     handlers.NewPortBindingHandler(portBindingService, auditLogger),
			security:        handlers.NewSecurityHandler(securityService, portBindingService, workspaceRepo, userRepo, ee),
			elevation:       handlers.NewElevationHandler(elevationService, userRepo, auditLogger),
			database:        handlers.NewDatabaseHandler(databaseService, appService, forwardService, secretService, userRepo, auditLogger, clusterService),
			job:             handlers.NewJobHandler(jobService, auditLogger),
			secret:          handlers.NewSecretHandler(secretService, auditLogger),
			config:          handlers.NewConfigHandler(configService, auditLogger),
			search:          handlers.NewSearchHandler(searchService),
			certificate:     handlers.NewCertificateHandler(certificateService, auditLogger),
			volume:          handlers.NewVolumeHandler(storageService, userRepo, auditLogger),
			backup:          handlers.NewBackupHandler(backupService, dbRepo, backupRepo, backupSetRepo, backupSettingsService, cronManager, ee, auditLogger, cfg.RestoreMaxMB),
			backupSettings:  handlers.NewWorkspaceBackupSettingsHandler(backupSettingsService, auditLogger),
			workspaceKeys:   handlers.NewWorkspaceKeysHandler(workspaceKeysService, sealingService, auditLogger),
			volumeBackup:    handlers.NewVolumeBackupHandler(volumeBackupService, volumeRepo, volumeBackupRepo, cronManager, ee, auditLogger),
			workspaceBundle: handlers.NewWorkspaceBundleHandler(wsBundleService, auditLogger),
			wsMigration: handlers.NewMigrationHandler(handlers.MigrationHandlerDeps{
				Svc: wsBundleService, Keys: apiKeyService, KeyRepo: apiKeyRepo, Workspaces: workspaceRepo, Users: userRepo,
				Audit: auditLogger, Enabled: cfg.MigrationEnabled, PublicURL: cfg.DeploymentURL(), InstallID: installID, Version: config.Version,
			}),
			migration:     handlers.NewLocationMigrationHandler(migrationService, userRepo, ee, auditLogger),
			monitoring:    handlers.NewMonitoringHandler(monitoringService),
			analytics:     handlers.NewAnalyticsHandler(repositories.NewAnalyticsRepository(db), ee, analytics.NewLiveTracker(redisClient, time.Duration(cfg.AnalyticsLiveWindowSeconds)*time.Second), appRepo),
			inbox:         handlers.NewNotificationInboxHandler(inboxRepo, bus, announcementService),
			alerts:        handlers.NewAlertHandler(repositories.NewAlertRepository(db)),
			marketplace:   handlers.NewMarketplaceHandler(marketplaceService, auditLogger),
			registry:      handlers.NewRegistryHandler(registryService, auditLogger),
			gitRepo:       handlers.NewGitRepositoryHandler(gitRepoService, auditLogger),
			gitInspect:    handlers.NewGitInspectHandler(gitRepoService),
			apply:         handlers.NewApplyHandler(applyService, appService, placer, auditLogger),
			gitops:        handlers.NewGitOpsHandler(gitopsService, auditLogger),
			pipeline:      handlers.NewPipelineHandler(pipelineService, bus, auditLogger),
			versioning:    handlers.NewVersioningHandler(versionService, pipelineService, auditLogger),
			image:         handlers.NewImageHandler(imageService, dockerClient, auditLogger),
			environment:   handlers.NewEnvironmentHandler(environmentService, auditLogger),
			release:       handlers.NewReleaseHandler(releaseService, auditLogger),
			events:        handlers.NewEventsHandler(eventsService, bus, appService, databaseService, monitoringService),
			webhook:       handlers.NewWebhookHandler(webhookService, auditLogger),
			notification:  handlers.NewNotificationHandler(notificationService, auditLogger),
			node:          handlers.NewNodeHandler(nodeService, nodeManager, nodeGateway, dockerImportService, housekeepingService, clusterService, imageResolver, cfg.ControlURL, auditLogger, bus, cfg.HostProcPath),
			cluster:       handlers.NewClusterHandler(clusterService, nodeService, auditLogger),
			provider:      handlers.NewProviderHandler(nodeService, routeService),
			runner:        handlers.NewRunnerHandler(runnerService, auditLogger),
			runnerGateway: handlers.NewRunnerGatewayHandler(runnerService, runnerManager),

			adminUser:           handlers.NewAdminUserHandler(db, userRepo, sessionRepo, workspaceRepo, auditRepo, sessionStore, auditLogger, accountService, cfg.DeletionGraceDays),
			adminWorkspace:      handlers.NewAdminWorkspaceHandler(db, workspaceRepo, auditLogger),
			adminWorkspaceOps:   handlers.NewAdminWorkspaceOpsHandler(workspaceRepo, userRepo, planRepo, workspaceService, accountService, restrictionService, auditLogger),
			adminDomain:         handlers.NewAdminDomainHandler(db, domainRepo, routeRepo, domainService, auditLogger),
			adminRoute:          handlers.NewAdminRouteHandler(db, routeRepo, workspaceRepo, routeService, auditLogger),
			adminMetrics:        handlers.NewAdminMetricsHandler(db, dockerClient, redisClient, time.Now()),
			adminEvent:          handlers.NewAdminEventHandler(auditRepo, userRepo, bus, ee),
			adminSetting:        handlers.NewAdminSettingHandler(settingRepo, settingsProvider, auditLogger),
			adminInfo:           handlers.NewAdminInfoHandler(cfg.BillingAPIEnabled, ee, cfg.MigrationEnabled),
			adminBilling:        handlers.NewAdminBillingHandler(billingService, cfg.BillingAPIEnabled, ee, auditLogger),
			billing:             handlers.NewBillingHandler(billingService, auditLogger),
			adminBranding:       handlers.NewAdminBrandingHandler(brandingService, ee, auditLogger),
			capability:          handlers.NewCapabilityHandler(cfg.ContainerGrantsEnabled, appRepo, workspaceRepo),
			register:            handlers.NewRegisterHandler(registrationService, authService, userRepo, platformMailer, auditLogger),
			update:              handlers.NewUpdateHandler(updateService),
			telemetry:           handlers.NewTelemetryHandler(telemetryService),
			adminPlan:           handlers.NewPlanHandler(planRepo, quotaOverrideRepo, workspaceRepo, databaseSizeRepo, repositories.NewRunnerRepository(db), ee, auditLogger),
			deploymentCfg:       handlers.NewDeploymentConfigHandler(imageResolver, settingRepo, settingsProvider, auditLogger, ee),
			adminJob:            handlers.NewAdminJobHandler(cronManager),
			adminControlManager: handlers.NewAdminControlManagerHandler(controlManager),
			adminPlatformBackup: handlers.NewAdminPlatformBackupHandler(platformBackupService, ee, auditLogger),
			adminRegistry:       handlers.NewAdminRegistryHandler(registryServerService, ee, auditLogger),
			registryServer:      handlers.NewRegistryServerHandler(registryServerService, workspaceRepo),
			components: handlers.NewComponentHandler(components.NewService(components.Probes{
				RegistryEnabled: func() bool {
					st, err := registryServerService.Get()
					return err == nil && st.Enabled && registryServerService.HostFor(st) != ""
				},
				AnalyticsEnabled:   func() bool { return cfg.AnalyticsEnabled },
				HealthProbeBundled: healthprobe.Bundled,
				Catalog: func() components.Catalog {
					n, synced, generated := marketplaceRemote.CatalogInfo()
					c := components.Catalog{Templates: n, GeneratedAt: generated}
					if !synced.IsZero() {
						c.SyncedAt = &synced
					}
					return c
				},
			})),
			oauthAdmin:  handlers.NewOAuthAdminHandler(oauthRepo, oauthService, ee, auditLogger),
			oauthPublic: handlers.NewOAuthHandler(oauthService, oauthRepo, authService, sessionRepo, auditLogger, cfg),
			license: handlers.NewLicenseHandler(ee, licenseNodeCount, func() int64 { n, _ := planRepo.Count(); return n }, func() int64 { n, _ := storageClassService.Count(); return n },
				func() int64 { n, _ := repositories.NewRunnerRepository(db).CountShared(); return n },
				installID, auditLogger),
			ssoAdmin:          handlers.NewSSOAdminHandler(orgRepo, samlConfigRepo, scimTokenRepo, ee, auditLogger),
			adminOrganization: handlers.NewAdminOrganizationHandler(organizationService, userRepo, clusterRepo, workspaceRepo, ee, auditLogger),
			ldapAdmin:         handlers.NewLDAPAdminHandler(ldapRepo, ee, auditLogger),
			permission:        handlers.NewPermissionHandler(),
			customRole:        handlers.NewCustomRoleHandler(customRoleService, workspaceService, ee, auditLogger),
			auditExport:       handlers.NewAuditExportHandler(auditRepo, ee),
			resourcePolicy:    handlers.NewResourcePolicyHandler(resourcePolicyRepo, workspaceRepo, ee, auditLogger),
			siemAdmin:         handlers.NewSIEMAdminHandler(siemConfigRepo, siemStreamer, ee, auditLogger),
			adminAnnouncement: handlers.NewAdminAnnouncementHandler(announcementService, announcementRepo, userRepo, ee, auditLogger),
			adminDatabaseSize: handlers.NewAdminDatabaseSizeHandler(databaseSizeRepo, ee, auditLogger),
			adminStorageClass: handlers.NewAdminStorageClassHandler(storageClassService, ee, auditLogger),
			adminRunner:       handlers.NewAdminRunnerHandler(runnerService, ee, auditLogger),
		},
	}

	r.h.auth.SetUserSettings(userSettingsService)
	r.h.adminSetting.SetAuthAccess(registrationService, cfg.PasswordResetEnabled)
	nodeManager.AddStatusListener(r.h.node.PublishStatus)

	if cfg.AnalyticsEnabled && redisClient != nil {
		r.h.provider.SetAnalyticsIngest(
			analytics.NewIngester(redisClient, routeService, cfg.AnalyticsStream),
			ingestMetrics{},
			func(srv *models.Server) handlers.AnalyticsForwarderConfig {
				// Every other node's traffic is already served by the manager's gateway.
				if srv == nil || srv.IsLocal || srv.Connectivity != models.ConnectivityEdgeGateway {
					return handlers.AnalyticsForwarderConfig{}
				}

				pw, err := nodeService.StoredGatewayRedisPassword(srv.ID)
				if err != nil || pw == "" {
					return handlers.AnalyticsForwarderConfig{}
				}
				return handlers.AnalyticsForwarderConfig{
					Enabled: true, Stream: cfg.AnalyticsStream,
					RedisAddr: edgegateway.RedisContainer + ":6379", RedisPassword: pw,
				}
			},
		)
	}

	// Platform notification emails (password reset, workspace invitation, welcome).
	r.h.auth.SetMailer(platformMailer)
	r.h.workspace.SetMailer(platformMailer)
	r.h.adminUser.SetMailer(platformMailer)
	r.h.adminUser.SetUserGuard(billingService.GuardServiceUser)
	r.h.auth.SetLoginTokens(loginTokenService)
	passkeyRepo := repositories.NewPasskeyRepository(db)
	r.h.auth.SetPasskeys(newPasskeyService(cfg, redisClient, passkeyRepo), passkeyRepo)
	r.h.oauthPublic.SetLoginTokens(loginTokenService)
	r.h.adminUser.SetEnterprise(ee) // gate the per-user workspace-limit override

	// Lets any member, not just admins, be offered the service runtime.
	r.h.usage.SetClusterCap(clusterService)
	r.h.usage.SetPlacer(placer)
	billingService.SetAccountDeps(billing.AccountDeps{Workspaces: workspaceService, EE: ee, Meter: r.h.usage,
		Restrictions: restrictionService, Deleter: accountService, HardAfter: time.Duration(cfg.BillingHardAfterDays) * 24 * time.Hour,
		OwnerCreated: func(u *models.User) {
			raw, owner, err := authService.CreatePasswordReset(u.Email)
			if err != nil || owner == nil {
				return
			}
			platformMailer.SendPasswordReset(owner.Email, owner.Name, raw, int(auth.PasswordResetTTL/time.Hour))
		}})
	r.h.app.SetPlacer(placer)
	r.h.database.SetPlacer(placer)
	r.h.volume.SetPlacer(placer)
	r.h.volume.SetStorageClasses(storageClassService, quotaService)
	r.h.stack.SetPlacer(placer)
	r.h.marketplace.SetPlacer(placer)

	// Nil-safe: falls back to the DB tail when the store is disabled.
	r.h.app.SetLogStore(logStore)
	r.h.job.SetLogStore(logStore)
	r.h.pipeline.SetLogStore(logStore)
	r.h.backup.SetLogStore(logStore)
	r.h.auth.SetBranding(brandingService, ee) // the sign-in page's operator identity
	// Read on every setup, so a later rename or domain change applies to the next enrolment.
	authService.SetTOTPIssuer(func() string {
		name := ""
		if ee.Has(enterprise.FlagWhiteLabel) {
			name = brandingService.Get().Name
		}
		return twofactor.Issuer(name, cfg.DeploymentURL())
	})
	r.h.auth.SetRegistration(registrationService)
	r.h.auth.SetCreationPolicy(workspaceService.CreationPolicy)
	authService.SetEmailVerifications(repositories.NewEmailVerificationRepository(db))
	// Has, not Mutable: license expiry keeps the accent lock.
	userSettingsService.SetBrandAccent(func() (models.Accent, bool) {
		if !ee.Has(enterprise.FlagWhiteLabel) {
			return "", false
		}
		b := brandingService.Get()
		return b.Accent, b.AccentEnforced()
	})
	r.h.volumeBackup.SetLogStore(logStore)
	r.h.adminPlatformBackup.SetLogStore(logStore)

	// Wire ACME managed-certificate issuance into the certificate handler.
	r.h.certificate.SetManaged(managedCertService)

	// Guarded so a typed-nil allocator is not stored as a non-nil interface.
	if subnetAllocator != nil {
		r.h.adminMetrics.SetSubnetAllocator(subnetAllocator)
	}
	// Reads stored sweep results so the dashboard stays one query regardless of node count.
	r.h.adminMetrics.SetNodeCapacity(serverRepo)
	r.h.cluster.SetCapacityStore(serverRepo)
	r.h.cluster.SetOrganizations(organizationService, ee)
	r.h.cluster.SetSystemCertificates(certificateService)
	r.h.adminUser.SetOrganizations(organizationService)
	r.h.adminWorkspace.SetOrganizations(organizationService, organizationService.OwnsClusters)
	r.h.oauthAdmin.SetWorkspaces(workspaceRepo)
	r.h.node.SetNodeStats(nodeStatsService)

	// Blocks cross-site WebSocket hijacking: same-origin plus the configured allowlist only.
	wsOrigins := cfg.AllowedBrowserOrigins()
	r.h.app.SetAllowedOrigins(wsOrigins)
	r.h.node.SetAllowedOrigins(wsOrigins)
	r.h.runnerGateway.SetAllowedOrigins(wsOrigins)

	// Annotate runners' live-tunnel status from the runner connection manager.
	r.h.runner.SetConnRegistry(runnerManager)
	r.h.adminRunner.SetConnRegistry(runnerManager)
	r.h.runner.SetRunnerReleases(runnerUpdateService)
	r.h.adminRunner.SetRunnerReleases(runnerUpdateService)
	r.h.runner.SetEventBus(bus)
	r.h.adminRunner.SetEventBus(bus)
	runnerManager.AddStatusListener(r.h.runner.PublishStatus)

	// The quiesce marker holds off schedules and redeploys until an operator has read the report and moved DNS.
	recoveryService := recovery.New(
		repositories.NewSettingRepository(db),
		serverRepo,
		appRepo,
		repositories.NewNetworkRepository(db),
	)
	recoveryService.SetNetworkEnsurer(func(ctx context.Context, name string) error {
		_, err := dockerClient.EnsureNetwork(ctx, name)
		return err
	})
	recoveryService.SetRedeployer(func(app *models.Application) error {
		_, err := appService.Redeploy(app)
		return err
	})
	recoveryService.SetRouteSyncer(routeService.SyncWorkspaceProxy)
	recoveryService.SetRegistryInfo(func() (string, bool) {
		st, err := registryServerService.Get()
		if err != nil || st == nil {
			return "", false
		}
		return st.StorageType, st.Enabled
	})
	recoveryService.SetDatabaseRecovery(
		dbRepo.ListAllInstances,
		func(ctx context.Context, instanceID uint) error {
			inst, err := dbRepo.FindByID(instanceID)
			if err != nil {
				return err
			}
			// The recovered row names a container on a dead host, so recreate from the stored spec before starting.
			if inst.ContainerID != "" {
				if _, err := dockerClient.InspectContainer(ctx, inst.ContainerID); err == nil {
					return databaseService.Start(ctx, inst)
				}
			}
			return databaseService.RunProvision(ctx, instanceID)
		},
		func(ctx context.Context) (*recovery.TenantRestoreSummary, error) {
			set, err := platformBackupService.LatestRestorableSet()
			if err != nil || set == nil {
				return nil, err
			}
			rep, err := platformBackupService.RestoreTenantData(ctx, set)
			if err != nil || rep == nil {
				return nil, err
			}
			return &recovery.TenantRestoreSummary{
				Ref:               rep.Ref,
				DatabasesRestored: rep.DatabasesRestored,
				VolumesRestored:   rep.VolumesRestored,
				Skipped:           rep.Skipped,
				Failures:          rep.Failures,
			}, nil
		},
	)
	r.h.adminRecovery = handlers.NewAdminRecoveryHandler(recoveryService, auditLogger)
	if recoveryService.Pending() {
		logger.Warn("this platform was restored from a recovery point and is QUIESCED: schedules are suspended until recovery is completed from Admin → Platform Backup")
	}

	// Only when enabled and FlagPlatformBackup is present, so Community incurs no platform-backup work.
	reschedulePlatformBackup := func(st *models.PlatformBackupSettings) {
		cronManager.UnregisterTask("platform-backup", 1)
		if st == nil || !st.ScheduleEnabled || st.ScheduleCron == "" || !r.ee.Has(enterprise.FlagPlatformBackup) {
			return
		}
		// Backing up a half-reconciled restore would overwrite good recovery points.
		if recoveryService.Pending() {
			logger.Warn("platform backup schedule suspended: this platform is recovering from a restore")
			return
		}
		if err := cronManager.RegisterTask("platform-backup", 1, "Platform backup", st.ScheduleCron, func() error {
			return platformBackupService.RunScheduled(context.Background())
		}); err != nil {
			logger.Error("invalid platform backup cron", "cron", st.ScheduleCron, "error", err)
		}
	}
	r.h.adminPlatformBackup.SetReschedule(reschedulePlatformBackup)
	if st, err := platformBackupService.GetSettings(); err == nil {
		reschedulePlatformBackup(st)
	}

	// Ensure checks Enabled, so a disabled registry is a no-op.
	r.h.adminRegistry.SetEnsure(func(ctx context.Context) error {
		return registryServerService.Ensure(ctx, dockerClient)
	})
	r.h.adminRegistry.SetGC(func(ctx context.Context) error {
		return registryServerService.GarbageCollect(ctx, dockerClient)
	})
	// The repository count decides whether a storage change strands images.
	r.h.adminRegistry.SetRuntime(func(ctx context.Context) (*registryserver.Runtime, error) {
		return registryServerService.RuntimeStatus(ctx, dockerClient)
	})
	r.h.adminRegistry.SetRepoCount(registryServerService.RepositoryCount)
	go func() {
		if err := registryServerService.Ensure(context.Background(), dockerClient); err != nil {
			logger.Warn("internal registry ensure on boot failed", "error", err)
		}
	}()
	// No-op unless enabled with deletes on; flips read-only during collection.
	if err := cronManager.RegisterTask("registry_gc", 0, "Registry garbage collection", "0 4 * * *", func() error {
		return registryServerService.GarbageCollect(context.Background(), dockerClient)
	}); err != nil {
		logger.Error("failed to register registry GC cron", "error", err)
	}

	// A platform admin can operate containers but not read another workspace's container logs.
	r.h.node.SetMembership(workspaceRepo)
	r.h.cluster.SetConnectivityApplier(r.h.node.ApplyConnectivity)
	r.h.node.SetPoolLabeler(clusterService.SyncPoolLabel)
	// Break-glass: only an explicit security-enforcement opt-out allows it.
	r.h.node.SetSecurityEnforcement(r.cfg.SecurityEnforcement)
	r.h.node.SetAgentReleases(agentUpdateService)
	// GPU inventory + admin device policy on the node detail page.
	r.h.node.SetGPU(gpuService)

	// A misconfigured IdP must never lock the whole instance out.
	ssoEnforced := func(user *models.User) bool {
		if user.IsAdmin() {
			return false
		}
		org, err := orgRepo.FindDefault()
		return err == nil && org.EnforceSSO
	}
	r.h.auth.SetSSOEnforcement(ssoEnforced)
	r.h.register.SetInvitations(workspaceService, ssoEnforced)
	// No-op in Community: Login returns (nil, nil) when ee.LDAP() is nil.
	r.h.auth.SetDirectoryLogin(directoryService.Login)

	// Maintenance mode (admins and auth endpoints excepted).
	r.v1.Use(middlewares.Maintenance(cfg, settingsProvider))

	if cfg.MetricsEnabled {
		r.app.Get("/metrics", metrics.Handler(), okapi.DocHide())
	}

	r.register(r.healthRoutes()...)
	r.register(r.infoRoute())
	r.register(r.permissionRoute())
	r.register(r.authRoutes()...)
	r.register(r.apiKeyRoutes()...)
	r.register(r.workspaceRoutes()...)
	r.register(r.serviceAccountRoutes()...)
	r.register(r.roleRoutes()...)
	r.register(r.applicationRoutes()...)
	r.register(r.jobRoutes()...)
	r.register(r.secretRoutes()...)
	r.register(r.configRoutes()...)
	r.register(r.searchRoutes()...)
	r.register(r.certificateRoutes()...)
	r.register(r.networkRoutes()...)
	r.register(r.stackRoutes()...)
	r.register(r.routeRoutes()...)
	r.register(r.domainRoutes()...)
	r.register(r.dnsProviderRoutes()...)
	r.register(r.middlewareRoutes()...)
	r.register(r.portBindingRoutes()...)
	r.register(r.capabilityRoutes()...)
	r.register(r.componentRoutes()...)
	r.register(r.databaseRoutes()...)
	r.register(r.volumeRoutes()...)
	r.register(r.volumeBackupRoutes()...)
	r.register(r.securityRoutes()...)
	r.register(r.backupRoutes()...)
	r.register(r.workspaceBackupSettingsRoutes()...)
	r.register(r.workspaceKeysRoutes()...)
	r.register(r.workspaceBundleRoutes()...)
	r.register(r.migrationRoutes()...)
	r.register(r.locationMigrationRoutes()...)
	r.register(r.monitoringRoutes()...)
	r.register(r.marketplaceRoutes()...)
	r.register(r.registryRoutes()...)
	r.register(r.gitRepositoryRoutes()...)
	r.register(r.applyRoutes()...)
	r.register(r.gitOpsRoutes()...)
	r.register(r.pipelineRoutes()...)
	r.register(r.versioningRoutes()...)
	r.register(r.imageRoutes()...)
	r.register(r.environmentRoutes()...)
	r.register(r.releaseRoutes()...)
	r.register(r.webhookRoutes()...)
	r.register(r.notificationRoutes()...)
	r.register(r.inboxRoutes()...)
	r.register(r.alertRoutes()...)
	r.register(r.nodeRoutes()...)
	r.register(r.clusterRoutes()...)
	r.register(r.clustersRoutes()...)
	r.register(r.locationRoutes()...)
	r.register(r.runnerRoutes()...)
	r.register(r.adminRunnerRoutes()...)
	r.register(r.runnerGatewayRoutes()...)
	r.register(r.agentRoutes()...)
	r.register(r.providerRoutes()...)
	r.register(r.adminRoutes()...)
	r.h.billing.SetMigration(r.h.wsMigration)
	r.register(r.billingRoutes()...)
	r.register(r.registryServerRoutes()...)
	r.register(r.oauthPublicRoutes()...)
	r.register(r.ssoAdminRoutes()...)
	r.register(r.samlPublicRoutes()...)
	r.register(r.scimRoutes()...)

	// Registered last so API routes win; Okapi auto-excludes their top-level segments.
	if cfg.WebDir != "" {
		r.app.Web("/", cfg.WebDir, okapi.WebConfig{MaxAge: time.Hour})
	} else {
		r.app.WebFS("/", web.Assets, okapi.WebConfig{Root: "dist", MaxAge: time.Hour})
	}

	// Synchronous before serving so Goma's debounced watcher sees the converged state in one reload.
	if err := routeService.ResyncAllProxy(context.Background()); err != nil {
		logger.Warn("proxy startup resync failed", "err", err)
	}
	// A node converted from port-forward at upgrade moves its routes and DNS to its own gateway.
	if list, err := clusterService.Clusters(); err == nil {
		for i := range list {
			if list[i].LegacyIngress {
				go routeService.SyncCluster(context.Background(), list[i].ID)
			}
		}
	}

	// Returned so the embedded worker runs bundle tasks against this fully wired service graph.
	return forwardService, runnerDispatcher, runnerManager, wsBundleService, migrationService
}

// Must be called before RunServer: Okapi binds these during applyCommon at startup.
func RegisterFallbacks(app *okapi.Okapi) {
	app.NoRoute(func(c *okapi.Context) error { return c.AbortNotFound("route not found") })
	app.NoMethod(func(c *okapi.Context) error { return c.AbortMethodNotAllowed("method not allowed") })
}

func (r *Router) healthRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "/healthz",
			Handler:  r.h.health.Healthz,
			Tags:     []string{"Health"},
			Summary:  "Liveness probe",
			Response: &handlers.HealthResponse{},
		},
		{
			Method:   http.MethodGet,
			Path:     "/readyz",
			Handler:  r.h.health.Readyz,
			Tags:     []string{"Health"},
			Summary:  "Readiness probe",
			Response: &handlers.ReadyResponse{},
		},
	}
}

// permissionRoute exposes the permission catalog and built-in role presets for the role picker.
func (r *Router) permissionRoute() okapi.RouteDefinition {
	return okapi.RouteDefinition{
		Method:      http.MethodGet,
		Path:        "/permissions",
		Handler:     r.h.permission.Catalog,
		Group:       r.v1,
		Middlewares: []okapi.Middleware{r.authenticate},
		Tags:        []string{"Permissions"},
		Summary:     "Permission catalog & built-in role presets",
		Response:    &dto.Response[handlers.PermissionCatalog]{},
	}
}

func (r *Router) infoRoute() okapi.RouteDefinition {
	return okapi.RouteDefinition{
		Method:   http.MethodGet,
		Path:     "/info",
		Handler:  handlers.NewInfo(r.cfg.OpenAPIDocs),
		Group:    r.v1,
		Tags:     []string{"Info"},
		Summary:  "Application info",
		Response: &dto.Response[handlers.AppInfo]{},
	}
}

// A recovery point records the panel's domain rather than its full URL.
func urlHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		return strings.TrimSuffix(s, "/")
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

// ingestMetrics adapts the metrics package to the handler's interface.
type ingestMetrics struct{}

func (ingestMetrics) IngestAccepted(node string, n int) { metrics.IngestAccepted(node, n) }
func (ingestMetrics) IngestRejected(node string, n int) { metrics.IngestRejected(node, n) }

// Refuses on engines that cannot auto-assign IPv6, or every network create fails, unless a ULA prefix is pinned.
func ipv6Enabled(ctx context.Context, dc docker.Client, cfg *config.Config) bool {
	if !cfg.NetworkIPv6 {
		return false
	}
	if strings.TrimSpace(cfg.NetworkIPv6ULAPrefix) != "" {
		return true // Miabi pins the subnet; no daemon-side assignment needed
	}
	info, err := dc.Info(ctx)
	if err != nil {
		logger.Warn("could not read the engine version; leaving IPv6 off", "error", err)
		return false
	}
	if engineSupportsAutoIPv6(info.Version) {
		return true
	}
	logger.Error("MIABI_NETWORK_IPV6 is on but this Docker Engine cannot assign an IPv6 range itself; "+
		"upgrade to Engine 26+ or set MIABI_NETWORK_IPV6_ULA_PREFIX. Leaving IPv6 off",
		"engine", info.Version)
	return false
}

// Docker 26+ assigns a ULA /64 itself; older engines fail without an explicit v6 subnet.
func engineSupportsAutoIPv6(version string) bool {
	major := strings.SplitN(strings.TrimSpace(version), ".", 2)[0]
	n, err := strconv.Atoi(major)
	return err == nil && n >= 26
}

func registerTelemetry(cronManager *cronpkg.Manager, svc *telemetry.Service) {
	if !svc.Enabled() {
		logger.Debug("anonymous telemetry is disabled")
		return
	}
	logger.Info("Anonymous telemetry is enabled (version, edition, platform, arch; no personal data). Disable with MIABI_TELEMETRY=false: https://docs.miabi.io/docs/operations/telemetry")
	if cronManager != nil {
		// Minute offset from the update checks so installs don't converge on one minute.
		if err := cronManager.RegisterTask("telemetry", 0, "Send the anonymous daily telemetry event", "51 4 * * *", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := svc.Send(ctx, telemetry.EventDaily); err != nil {
				logger.Debug("telemetry daily event not sent", "error", err)
			}
			return nil
		}); err != nil {
			logger.Error("failed to register telemetry job", "error", err)
		}
	}
	go svc.SendStartup(context.Background())
}

// Seeds the cache shortly after boot so a fresh install does not wait a day.
func registerUpdateCheck(cronManager *cronpkg.Manager, checker *updatecheck.Checker) {
	if !checker.Enabled() {
		return
	}
	check := func(what string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := checker.Check(ctx); err != nil {
			logger.Warn(what+" failed", "error", err)
		}
	}
	if err := cronManager.RegisterTask("update-check", 0, "Check for new Miabi, agent, runner and gateway releases", "37 4 * * *", func() error {
		check("update check")
		return nil
	}); err != nil {
		logger.Error("failed to register update check job", "error", err)
	}
	// Off the startup path: never block serving on the network.
	go func() {
		time.Sleep(30 * time.Second)
		check("initial update check")
	}()
}

// Passkeys are tied to an origin, so without a public URL they stay off.
func newPasskeyService(cfg *config.Config, rdb *redis.Client, repo *repositories.PasskeyRepository) *passkey.Service {
	if !cfg.PasskeysEnabled {
		logger.Info("passkeys disabled: MIABI_PASSKEYS_ENABLED=false")
		return nil
	}
	pc, ok := passkey.ConfigFromWebURL(cfg.DeploymentURL(), cfg.PasskeyRPID, cfg.AppName, cfg.PasskeyOrigins)
	if !ok {
		logger.Info("passkeys disabled: MIABI_WEB_URL is not set")
		return nil
	}
	svc, err := passkey.New(pc, rdb, repo)
	if err != nil {
		logger.Warn("passkeys disabled", "error", err)
		return nil
	}
	return svc
}
