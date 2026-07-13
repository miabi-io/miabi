// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/certificate"
	"github.com/miabi-io/miabi/internal/services/cluster"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/customrole"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dbupgrade"
	"github.com/miabi-io/miabi/internal/services/directory"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/dockerimport"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/environment"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/gitops"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/housekeeping"
	"github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/job"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/managedcert"
	"github.com/miabi-io/miabi/internal/services/marketplace"
	marketremote "github.com/miabi-io/miabi/internal/services/marketplace/remote"
	mwservice "github.com/miabi-io/miabi/internal/services/middleware"
	"github.com/miabi-io/miabi/internal/services/monitoring"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/services/network"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/notify"
	"github.com/miabi-io/miabi/internal/services/oauth"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/services/platformbackup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/services/portforward"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/registry"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	releasesvc "github.com/miabi-io/miabi/internal/services/release"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
	"github.com/miabi-io/miabi/internal/services/volumebackup"
	"github.com/miabi-io/miabi/internal/services/webhook"
	"github.com/miabi-io/miabi/internal/services/workspace"
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
	app              *okapi.Okapi
	cfg              *config.Config
	v1               *okapi.Group
	authenticate     okapi.Middleware
	scope            okapi.Middleware
	systemAdmin      okapi.Middleware
	authRateLimit    okapi.Middleware
	ee               enterprise.EE
	resourcePolicies *repositories.ResourcePolicyRepository
	// agentRateLimit guards the agent tunnel endpoint. It is far more lenient
	// than the login limiter: it's a token-authenticated, long-lived WebSocket,
	// and reconnect storms or several nodes behind one NAT IP must not lock
	// agents out. The long random join token makes brute force a non-issue.
	agentRateLimit okapi.Middleware
	h              routerHandlers
}

type routerHandlers struct {
	health         *handlers.HealthHandler
	auth           *handlers.AuthHandler
	apiKey         *handlers.APIKeyHandler
	workspace      *handlers.WorkspaceHandler
	app            *handlers.ApplicationHandler
	network        *handlers.NetworkHandler
	stack          *handlers.StackHandler
	route          *handlers.RouteHandler
	domain         *handlers.DomainHandler
	dnsProvider    *handlers.DNSProviderHandler
	middleware     *handlers.MiddlewareHandler
	portBinding    *handlers.PortBindingHandler
	database       *handlers.DatabaseHandler
	job            *handlers.JobHandler
	secret         *handlers.SecretHandler
	certificate    *handlers.CertificateHandler
	volume         *handlers.VolumeHandler
	backup         *handlers.BackupHandler
	backupSettings *handlers.WorkspaceBackupSettingsHandler
	volumeBackup   *handlers.VolumeBackupHandler
	monitoring     *handlers.MonitoringHandler
	marketplace    *handlers.MarketplaceHandler
	registry       *handlers.RegistryHandler
	gitRepo        *handlers.GitRepositoryHandler
	apply          *handlers.ApplyHandler
	gitops         *handlers.GitOpsHandler
	pipeline       *handlers.PipelineHandler
	image          *handlers.ImageHandler
	environment    *handlers.EnvironmentHandler
	release        *handlers.ReleaseHandler
	events         *handlers.EventsHandler
	webhook        *handlers.WebhookHandler
	notification   *handlers.NotificationHandler
	node           *handlers.NodeHandler
	cluster        *handlers.ClusterHandler
	provider       *handlers.ProviderHandler
	runner         *handlers.RunnerHandler
	runnerGateway  *handlers.RunnerGatewayHandler
	usage          *handlers.UsageHandler

	adminUser           *handlers.AdminUserHandler
	adminWorkspace      *handlers.AdminWorkspaceHandler
	adminDomain         *handlers.AdminDomainHandler
	adminRoute          *handlers.AdminRouteHandler
	adminMetrics        *handlers.AdminMetricsHandler
	adminEvent          *handlers.AdminEventHandler
	adminSetting        *handlers.AdminSettingHandler
	update              *handlers.UpdateHandler
	adminPlan           *handlers.PlanHandler
	deploymentCfg       *handlers.DeploymentConfigHandler
	adminJob            *handlers.AdminJobHandler
	adminPlatformBackup *handlers.AdminPlatformBackupHandler
	adminRegistry       *handlers.AdminRegistryHandler
	registryServer      *handlers.RegistryServerHandler
	oauthAdmin          *handlers.OAuthAdminHandler
	oauthPublic         *handlers.OAuthHandler
	license             *handlers.LicenseHandler
	ssoAdmin            *handlers.SSOAdminHandler
	ldapAdmin           *handlers.LDAPAdminHandler
	permission          *handlers.PermissionHandler
	customRole          *handlers.CustomRoleHandler
	auditExport         *handlers.AuditExportHandler
	resourcePolicy      *handlers.ResourcePolicyHandler
	siemAdmin           *handlers.SIEMAdminHandler
	adminRunner         *handlers.AdminRunnerHandler
}

// InitRoutes wires repositories, services, handlers, and routes onto the app. It
// returns the port-forward service so the caller can release its live sessions on shutdown.
func InitRoutes(app *okapi.Okapi, db *gorm.DB, redisClient *redis.Client, cfg *config.Config, producer *worker.Producer, dockerClient docker.Client, nodeService *node.Service, nodeManager *nodes.Manager, nodeGateway *edgegateway.Service, clusterService *cluster.Service, bus *eventbus.Bus, proxyMgr proxy.Manager, cronManager *cronpkg.Manager, logStore *logstore.Store) (*portforward.Service, *runners.Dispatcher) {
	metrics.SetBuildInfo(config.Version, config.CommitID)

	// Repositories
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
	oauthRepo := repositories.NewOAuthProviderRepository(db)

	// Services
	sessionStore := session.NewStore(redisClient)
	recoveryRepo := repositories.NewTwoFactorRecoveryRepository(db)
	authService := auth.NewService(userRepo, resetRepo, recoveryRepo, sessionStore, cfg.JWTSecret)
	settingsProvider := settings.NewProvider(settingRepo, map[string]string{
		settings.KeyExternalBaseDomain:   cfg.ExternalBaseDomain,
		settings.KeyExternalBaseProvider: cfg.ExternalBaseProvider,
	})
	oauthService := oauth.NewService(oauthRepo, userRepo, redisClient)
	oauthService.SetWorkspaces(workspaceRepo) // auto-join SSO users to a provider's default workspace
	orgRepo := repositories.NewOrganizationRepository(db)
	samlConfigRepo := repositories.NewSAMLConfigRepository(db)
	scimTokenRepo := repositories.NewSCIMTokenRepository(db)
	ldapRepo := repositories.NewLDAPRepository(db)
	customRoleRepo := repositories.NewCustomRoleRepository(db)
	customRoleService := customrole.NewService(customRoleRepo)
	resourcePolicyRepo := repositories.NewResourcePolicyRepository(db)
	siemConfigRepo := repositories.NewSIEMConfigRepository(db)
	// Stable per-instance Install ID: identifies this deployment to the license/
	// customer portal (shown as "Your Install ID"); a license may be bound to it.
	installID, err := dbstorage.EnsureInstallID(db)
	if err != nil {
		logger.Warn("failed to ensure install id", "error", err)
	}
	// Commercial edition seam: gates licensed features behind a verified license.
	// In Community builds this is the deny-all stub (no verification code linked).
	ee := enterprise.New(db, cfg.LicensePublicKey, cfg.LicenseFile, cfg.DeploymentURL(), installID)
	// Enterprise SIEM streaming: ships the audit log to external sinks at-least-once
	// via a durable cursor. Dormant unless the siem_stream entitlement is present.
	siemStreamer := siem.NewStreamer(auditRepo, siemConfigRepo, ee)
	// Enterprise SSO (SAML): provision the identity to a session and hand back the
	// SPA callback URL. Keeps auth/session out of the enterprise package. No-op in
	// Community (the stub's InitSSO does nothing).
	ee.InitSSO(enterprise.SSODeps{
		DB:      db,
		BaseURL: cfg.ApiBaseURL,
		Decrypt: crypto.Decrypt, // read the LDAP bind password stored at rest
		Login: func(ctx context.Context, ident enterprise.SSOIdentity) (string, error) {
			user, err := oauthService.ProvisionSSOUser(ctx, ident.Email, ident.Name)
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
	// Enterprise LDAP/AD: turns a directory bind (behind ee.LDAP()) into a
	// provisioned user with reconciled group access. No-op in Community.
	directoryService := directory.NewService(ee, userRepo, workspaceRepo, ldapRepo)
	serverRepo := repositories.NewServerRepository(db)
	licenseNodeCount := func() int64 {
		servers, err := serverRepo.List()
		if err != nil {
			return 0
		}
		return int64(len(servers))
	}
	// Plans / quotas: per-workspace resource limits + capability gates. Enforcement
	// is off by default (cfg.PlanEnforcement); when off, all checks pass.
	planRepo := repositories.NewPlanRepository(db)
	quotaService := quota.NewService(planRepo, quotaOverrideRepo, appRepo, volumeRepo, dbRepo, cfg.PlanEnforcement)
	quotaService.SetEdition(ee) // clamps Enterprise-only shell/security policies in CE
	// Runners: dedicated build/pipeline machines (workspace-owned + platform-shared).
	// Quota gates MaxRunners on create and the platform-runners capability on use.
	runnerService := runner.NewService(repositories.NewRunnerRepository(db))
	runnerService.SetQuota(quotaService)
	runnerService.SetImage(cfg.RunnerImage) // enrollment command shows the configured image (MIABI_RUNNER_IMAGE)
	// Runner tunnels: runners dial in outbound (token-authenticated); the manager
	// tracks each runner's live session + online/offline status.
	runnerManager := runners.NewManager(runnerService)
	// Scheduling: the manager is the live-tunnel registry, and the lease store
	// provides per-runner concurrency accounting + dead-lease requeue.
	runnerService.SetScheduling(runnerManager, repositories.NewRunnerLeaseRepository(db))
	apiKeyService := auth.NewAPIKeyService(apiKeyRepo)
	apiKeyService.SetQuota(quotaService)
	// Subnet allocator: hands out pool subnets for every managed Docker network so
	// creation doesn't exhaust Docker's small built-in address pools. Nil-safe —
	// on a config error the services fall back to Docker's default pool.
	subnetAllocator, err := netalloc.NewService(repositories.NewNetworkAllocationRepository(db), cfg.NetworkPoolCIDR, cfg.NetworkSubnetPrefix)
	if err != nil {
		logger.Warn("subnet allocator disabled; falling back to Docker's default address pool", "error", err)
		subnetAllocator = nil
	} else {
		// Reserve any pool subnet already used by a pre-existing network so we never
		// hand out an overlapping one (best-effort; skipped when Docker is offline).
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = subnetAllocator.ImportExisting(ctx, dockerClient, 0)
		cancel()
	}
	networkService := network.NewService(networkRepo, dockerClient)
	networkService.SetQuota(quotaService)
	networkService.SetAllocator(subnetAllocator)
	// Cluster mode: a workspace network is a swarm overlay (spans nodes) instead of
	// a node-local bridge, and existing bridges are converted when the admin enables
	// cluster networking. Off (the single-node default), everything stays a bridge.
	networkService.SetCluster(clusterService)
	networkService.SetClients(nodeManager.Clients())
	networkService.SetMigrationDeps(serverRepo, repositories.NewDatabaseRepository(db))
	clusterService.SetNetworkMigrator(
		func(ctx context.Context) error { _, err := networkService.Migrate(ctx); return err },
		func(ctx context.Context) error { _, err := networkService.Rollback(ctx); return err },
		func() int { n, _ := networkService.PendingMigration(); return n },
	)
	workspaceService := workspace.NewService(workspaceRepo, userRepo, networkService)
	workspaceService.SetPlans(planRepo)
	workspaceService.SetQuota(quotaService)
	// Per-user workspace-count limit: the platform-global max_workspaces_per_user
	// is always enforced (OSS); the per-user override (User.WorkspaceLimit) applies
	// only with the Enterprise user_workspace_limit entitlement.
	workspaceService.SetLimits(
		func() int { return settingsProvider.Int(settings.KeyMaxWorkspacesPerUser, 3) },
		func() bool { return ee.Has(enterprise.FlagUserWorkspaceLimit) },
	)
	// Per-user membership limit (workspaces JOINED as a non-owner member): global
	// max_workspace_memberships_per_user is OSS; the per-user override is gated by
	// the Enterprise user_workspace_membership_limit entitlement.
	workspaceService.SetMembershipLimits(
		func() int { return settingsProvider.Int(settings.KeyMaxWorkspaceMembershipsPerUser, 3) },
		func() bool { return ee.Has(enterprise.FlagUserWorkspaceMembershipLimit) },
	)
	// SSO auto-join respects the same membership limit (over-limit users just
	// aren't auto-joined; directory/SCIM sync is exempt — it is authoritative).
	oauthService.SetMembershipGate(workspaceService.CanJoinAnother)
	// Crypto-shred a workspace's encryption keys on delete (reuses the live keyring).
	if sh, ok := crypto.CurrentKeyring().(workspace.KeyShredder); ok {
		workspaceService.SetKeyShredder(sh)
	}
	// Seed the built-in plan catalog (Free/Pro/Unlimited) first, so EnsureSystem
	// can pin the Miabi System workspace to the Unlimited plan. Idempotent.
	if err := dbstorage.SeedPlans(db); err != nil {
		logger.Warn("failed to seed default plans", "error", err)
	}
	// Seed the single default organization (identity realm). Idempotent.
	if _, err := dbstorage.SeedDefaultOrganization(db); err != nil {
		logger.Warn("failed to seed default organization", "error", err)
	}
	// Seed the platform admin on first boot and ensure it owns the Miabi System
	// workspace. Idempotent; a no-op once any user exists.
	if admin, err := dbstorage.SeedAdmin(db, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		logger.Error("failed to seed admin user", "error", err)
	} else if admin != nil {
		if _, err := workspaceService.EnsureSystem(admin.ID); err != nil {
			logger.Error("failed to provision Miabi System workspace", "error", err)
		}
	}
	auditLogger := audit.NewLogger(auditRepo, bus)
	eventsService := events.NewService(appEventRepo, bus)
	nodeClients := nodeManager.Clients()
	appService := application.NewService(appRepo, deploymentRepo, releaseRepo, volumeRepo, routeRepo, networkRepo, stackRepo, appPortRepo, appEventRepo, nodeClients, producer, eventsService)
	// Lets the app service (re)publish host ports when a port-forward app gains a
	// route, and clean a deleted app's bindings.
	appService.SetPortBindings(portBindingRepo)
	storageService := storage.NewService(volumeRepo, appRepo, nodeClients)
	portBindingService := portbinding.NewService(portBindingRepo, appRepo, appPortRepo, workspaceRepo, cfg.HostPortMin, cfg.HostPortMax)
	// Check host-port conflicts against ports actually published on the node
	// (incl. non-Miabi containers), not just the binding table.
	portBindingService.SetDocker(nodeClients)
	stackService := stack.NewService(stackRepo, appRepo, stackEnvRepo, appEventRepo, appService, storageService, dockerClient, portBindingService)
	stackService.SetAllocator(subnetAllocator)
	dockerImportService := dockerimport.NewService(nodeClients, appService, stackService, appRepo, releaseRepo, deploymentRepo, volumeRepo, networkRepo, stackRepo, portBindingRepo)
	// Node housekeeping: reclaim disk + reconcile drift between Docker and the DB.
	housekeepingService := housekeeping.NewService(nodeClients, appRepo, dbRepo, stackRepo, volumeRepo)
	routeService := route.NewService(routeRepo, middlewareRepo, appRepo, releaseRepo, serverRepo, portBindingRepo, proxyMgr, cfg.HostPortMin, cfg.HostPortMax)
	// Attach/detach an app from the shared proxy network as its routes come and go
	// (only route-exposed apps stay on it), reconciled live without a redeploy.
	proxyReconciler := worker.NewProxyNetworkReconciler(appRepo, releaseRepo, nodeClients)
	routeService.SetProxyAttacher(proxyReconciler)
	// Durability for cluster ingress: re-assert the central gateway's attachment to
	// the shared ingress overlay on every cluster refresh (so a gateway recreate via
	// `docker compose up -d` can't leave clustered apps publicly dark), plus once now
	// so a fresh boot doesn't wait a whole refresh interval.
	clusterService.SetIngressReconciler(proxyReconciler.ReconcileIngressGateway)
	// In cluster mode the gateway reaches a remote app over the ingress overlay by
	// its DNS alias, so no host port is published for it — and canary weights, which
	// the port-forward upstream cannot carry, start working on remote nodes.
	routeService.SetCluster(clusterService)
	go func() { _ = proxyReconciler.ReconcileIngressGateway(context.Background()) }()
	// Auto port-forwarding: when a port-forward app gains a route, redeploy it so
	// the node actually publishes the allocated host port the gateway targets.
	routeService.SetPortPublisher(appService)
	// After a workspace proxy sync, tell affected edge-gateway nodes to pull their
	// config immediately instead of waiting for the HTTP-provider poll interval.
	routeService.SetEdgeReloader(newEdgeReloader(nodeService, nodeGateway))
	// Canary traffic changes re-sync the proxy route without a redeploy.
	appService.SetRouteSyncer(routeService)
	// Enforce platform CPU/memory caps on app create/update.
	appService.SetSettings(settingsProvider)
	// Cap node registrations to the edition limit (Community = 3; lifted by a
	// Enterprise license). Re-read per registration so a license change
	// applies without a restart.
	nodeService.SetNodeLimit(func() int { return ee.Entitlements().NodeLimit() })
	// Refuse new placements on cordoned/unknown nodes.
	appService.SetNodeGuard(nodeService)
	appService.SetServerInfo(nodeService)
	appService.SetNodeNamer(nodeService)       // resolve swarm node ids -> names for cluster placement display
	appService.SetWorkspaceInfo(workspaceRepo) // gate privileged host mounts
	appService.SetQuota(quotaService)
	appService.SetClusterCap(clusterService)     // gate "service" runtime apps on cluster mode
	appService.SetNetworkEnsurer(networkService) // apps always join the workspace's default network (self-heals a missing one)
	storageService.SetNodeGuard(nodeService)
	storageService.SetServerInfo(nodeService)
	storageService.SetQuota(quotaService)
	storageService.SetWorkspacePrivilege(workspaceRepo) // gate host-path volumes on the privileged flag
	// Middlewares are published as part of their workspace's proxy re-render (the
	// route service owns the single per-workspace Goma file), so the middleware
	// service drives the route service rather than the proxy directly.
	middlewareService := mwservice.NewService(middlewareRepo, routeService)
	backupRepo := repositories.NewBackupRepository(db)
	backupSettingsRepo := repositories.NewWorkspaceBackupSettingsRepository(db)
	volumeBackupRepo := repositories.NewVolumeBackupRepository(db)
	databaseService := database.NewService(dbRepo, nodeClients, producer)
	databaseService.SetEventBus(bus) // live status SSE; shares the bus with the embedded worker
	databaseService.SetNodeGuard(nodeService)
	databaseService.SetServerInfo(nodeService)
	databaseService.SetQuota(quotaService)

	// Account teardown: stop a disabled user's workloads, and cascade-delete all
	// of a deleted user's owned-workspace resources.
	accountService := account.NewService(userRepo, workspaceRepo, appRepo, dbRepo, stackRepo, appService, databaseService, stackService, storageService)
	accountService.SetEventBus(bus)                        // live workspace-deletion progress SSE
	accountService.SetWorkspaceFinalizer(workspaceService) // row delete + crypto-shred after teardown
	// Daily purge of accounts whose deletion grace period has elapsed.
	if err := cronManager.RegisterTask("account-purge", 0, "Purge accounts past deletion grace", "0 2 * * *", func() error {
		accountService.PurgeDue(context.Background())
		return nil
	}); err != nil {
		logger.Error("failed to register account purge job", "error", err)
	}

	// Daily release check. The minute is arbitrary but not :00 — every Miabi in
	// the world sharing one cron minute would stampede the GitHub API.
	updateService := updatecheck.NewService(db, config.Version, cfg.UpdateCheck)
	if updateService.Enabled() && cronManager != nil {
		if err := cronManager.RegisterTask("update-check", 0, "Check for a new Miabi release", "37 4 * * *", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Check records its own failures on the status row; returning nil keeps a
			// permanently offline host (air-gapped, egress-blocked) from painting the
			// jobs page red every single day.
			if err := updateService.Check(ctx); err != nil {
				logger.Warn("update check failed", "error", err)
			}
			return nil
		}); err != nil {
			logger.Error("failed to register update check job", "error", err)
		}
		// Seed the cache shortly after boot so a fresh install does not wait a day
		// for its first answer. Off the startup path: never block serving on GitHub.
		go func() {
			time.Sleep(30 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := updateService.Check(ctx); err != nil {
				logger.Warn("initial update check failed", "error", err)
			}
		}()
	}
	databaseService.SetNetworkProvider(networkService) // DBs run on the workspace's default network, alongside its apps
	// Delete guard: refuse to orphan a volume/database whose owning app/database/
	// stack still exists (records its owner in metadata; see models.Owner). A
	// stale owner (already deleted) returns false here and does not block.
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
	secretService.SetConsumers(appService)
	databaseService.SetSecrets(secretService)
	// Certificate store <-> route service: routes resolve custom certs from the
	// store at render time; the store consults routes for its delete guard/usage.
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
		// Audit log retention: prune entries older than the configured window
		// (audit_log_retention_days; 0 = keep forever). Open-source — uses the
		// existing setting; the paid capability is export, not retention.
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
		// Enterprise SIEM streaming: ship new audit events to external sinks. A no-op
		// unless the siem_stream entitlement is present; off the request path.
		if err := cronManager.RegisterTask("siem_stream", 1, "SIEM audit streaming", "@every 30s", func() error {
			return siemStreamer.Tick(context.Background())
		}); err != nil {
			logger.Warn("failed to schedule SIEM streaming", "error", err)
		}
	}
	jobRepo := repositories.NewJobRepository(db)
	jobService := job.NewService(jobRepo, appRepo, releaseRepo, nodeClients, producer, 3600)
	jobService.SetQuota(quotaService)
	jobService.SetScheduler(cronManager)
	jobService.LoadCronJobs()
	backupService := backup.NewService(backupRepo, dbRepo, nodeClients)
	backupService.SetDDLRunner(databaseService) // force restore drops & recreates the DB
	// Version upgrade: quiesce/restart apps via the app service, and dump/restore
	// via the backup service (a copy upgrade reuses the safety backup as its source).
	databaseService.SetAppController(dbupgrade.AppController(appService))
	databaseService.SetLogicalBackup(dbupgrade.Backup(backupService))
	// Per-workspace shared S3 backup target (used by database & volume backups).
	backupSettingsService := backupsettings.NewService(backupSettingsRepo)
	// Deployment-config image catalog: resolver over settings, with env config as
	// the built-in default for the gateway/relay images. Wired into every service
	// that runs a platform image.
	imageResolver := platformimage.New(settingsProvider, map[string]string{
		platformimage.KeyGoma:  cfg.NodeGatewayImage,
		platformimage.KeyRelay: cfg.ForwardRelayImage,
	})
	databaseService.SetImageResolver(imageResolver)
	// The network check runs socat probe containers on each node; it reuses the
	// port-forward relay image rather than introducing another one to pull.
	clusterService.SetNetCheckImage(imageResolver, cfg.ForwardRelayImage)
	// The global agent service: Swarm carries the agent to every worker, each agent
	// registers itself from the swarm node id its own engine reports, and the manager
	// verifies that id against its own membership before trusting the shared token.
	clusterService.SetAgentDeps(
		cluster.NewSettingsTokenStore(repositories.NewSettingRepository(db)),
		nodeService,
		cfg.ControlURL,
		imageResolver,
		"miabi/agent:latest", // fallback only; the image catalog is the source of truth
	)
	backupService.SetImageResolver(imageResolver)
	backupService.SetLogStore(logStore) // externalize backup run logs to the shared store
	// Volume backup: archives a volume to the workspace S3 target (volume-bkup).
	volumeBackupService := volumebackup.NewService(volumeBackupRepo, volumeRepo, nodeClients)
	volumeBackupService.SetImageResolver(imageResolver)
	volumeBackupService.SetS3Provider(backupSettingsService)
	volumeBackupService.SetEnqueuer(producer) // run backups on the background worker
	volumeBackupService.SetLogStore(logStore)
	// Platform (control-plane) backup: Enterprise, admin-only disaster recovery for
	// Miabi's own database and platform volumes. Draws its DB connection from the
	// control-plane config and runs on the manager node.
	pbHost, pbPort, pbName, pbUser, pbPass, pbSSL := cfg.Database.PostgresConn()
	platformBackupService := platformbackup.NewService(
		repositories.NewPlatformBackupRepository(db),
		repositories.NewPlatformBackupSettingsRepository(db),
		nodeClients,
		platformbackup.DBConn{Host: pbHost, Port: pbPort, Name: pbName, User: pbUser, Password: pbPass, SSLMode: pbSSL},
		cfg.ProxyNetwork,
	)
	platformBackupService.SetImageResolver(imageResolver)
	platformBackupService.SetEnqueuer(producer)
	platformBackupService.SetLogStore(logStore)
	forwardService.SetImageResolver(imageResolver)
	storageService.SetImageResolver(imageResolver)
	monitoringService := monitoring.NewService(appRepo, releaseRepo, dbRepo, stackRepo, appEventRepo, repositories.NewMetricRepository(db), nodeClients)
	monitoringService.SetServerInfo(nodeService)
	marketplaceService := marketplace.NewService(appService, databaseService, storageService, stackService, repositories.NewTemplateInstallRepository(db), repositories.NewTemplateRepository(db))
	marketplaceService.SetEventBus(bus) // live install-progress SSE
	// Marketplace registry sync: pull the official+community export bundle from
	// the configured marketplace service into Redis and merge it into the
	// catalog. Empty MIABI_MARKETPLACE_URL ⇒ disabled (embedded-only floor).
	marketplaceRemote := marketremote.New(cfg.MarketplaceURL, marketremote.NewRedisCache(redisClient))
	marketplaceService.SetRemote(marketplaceRemote)
	if marketplaceRemote.Enabled() {
		// Serve any previously-synced bundle immediately, then refresh in the
		// background so startup never blocks on the network.
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
	// Built-in Docker registry (distinct from the external-creds registryService).
	registryServerService := registryserver.NewService(
		repositories.NewRegistrySettingsRepository(db),
		imageResolver, settingsProvider, apiKeyService, workspaceRepo,
		proxyMgr, cfg.ProxyNetwork, cfg.ControlURL, cfg.Registry,
	)
	gitRepoRepo := repositories.NewGitRepoRepository(db)
	gitRepoService := gitrepo.NewService(gitRepoRepo)
	domainRepo := repositories.NewDomainRepository(db)
	domainService := domain.NewService(domainRepo)
	dnsProviderService := dnsprovider.NewService(repositories.NewDNSProviderRepository(db), repositories.NewDNSRecordRepository(db), domainRepo)
	dnsProviderService.SetQuota(quotaService)
	// Automate the ownership TXT (and ledger cleanup) for provider-connected domains.
	domainService.SetDNSAutomator(dnsProviderService)
	// Re-render a workspace's gateway config when a domain's verification changes,
	// so its routes go live (on verify) or offline (on drift) automatically.
	domainService.SetProxyResyncer(routeService)
	// Keep app A/AAAA/CNAME records in sync with routed hosts.
	routeService.SetDNSAddresser(dnsProviderService)
	// Managed certificates: issue + auto-renew TLS certs via ACME DNS-01 using a
	// connected DNS provider, stored in workspace Certificates.
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
		// Drift detection: re-check verified manual domains and un-verify any whose
		// ownership TXT has gone missing, dropping their routes offline.
		if err := cronManager.RegisterTask("domain_reverify", 1, "Domain ownership drift re-verification", fmt.Sprintf("@every %dm", reconcileEvery), func() error {
			return domainService.Reverify(context.Background())
		}); err != nil {
			logger.Warn("failed to register domain reverify task", "error", err)
		}
	}
	// Storage-usage sweep: cache real per-volume disk usage so the UI can show
	// declared-vs-used without a live `docker system df` on every read.
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
		// Seed after boot so a fresh install shows numbers without waiting a full
		// interval. Off the startup path.
		go func() {
			time.Sleep(30 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_ = storageService.MeasureUsage(ctx)
		}()
	}
	// Gate user-created route hostnames on a registered domain (generated
	// external-access routes bypass route.Service.Create, so they're exempt).
	routeService.SetDomains(domainRepo)
	// Privileged workspaces may expose routes under registered-but-unverified
	// domains (a banned domain is still blocked).
	routeService.SetWorkspacePolicy(workspaceRepo)
	// Gate custom-certificate imports on the workspace's registered domains.
	certificateService.SetDomains(domainRepo)
	// Declarative apply engine (shared by the one-shot apply API and GitOps).
	applyService := apply.NewService(appService, storageService, databaseService, stackService, secretService, routeService, domainService)
	// Declarative Application port exposure: externalAccess (reverse-proxy URLs,
	// over the platform base domain) and publish/hostPort (host-port bindings).
	applyService.SetPortExposure(func() route.ExternalConfig {
		return route.ExternalConfig{
			BaseDomain: settingsProvider.String(settings.KeyExternalBaseDomain, ""),
			Provider:   settingsProvider.String(settings.KeyExternalBaseProvider, ""),
		}
	}, portBindingService)
	// GitOps: reconcile a repo of miabi.io/v1 manifests via the apply engine.
	gitopsService := gitops.NewService(repositories.NewGitSourceRepository(db), gitRepoRepo, applyService)
	// CI/CD pipelines: definitions + runs on the internal runner (worker).
	pipelineService := pipeline.NewService(repositories.NewPipelineRepository(db), producer)
	// Built-image catalog: provenance written by pipeline builds; list + GC here.
	imageService := image.NewService(repositories.NewImageRepository(db), releaseRepo)
	// Runner job dispatch: sends a build to a runner over its tunnel, mints the
	// per-job credentials, and streams report frames back onto the run. Returned
	// so the caller can wire it into the pipeline worker (which holds the tunnels).
	runnerMinter := runners.NewCredentialMinter(apiKeyService, cfg.JobAPITokenEnabled)
	runnerDispatcher := runners.NewDispatcher(
		runnerService, runnerManager, runnerMinter,
		repositories.NewPipelineRepository(db), imageService, bus,
		time.Duration(cfg.BuildTimeoutMinutes)*time.Minute,
	)
	// Externalize each pipeline step's full log on terminal (bounded tail in the
	// DB), so a finished run's logs survive past the live SSE stream.
	runnerDispatcher.SetLogStore(logStore)
	// A succeeded runner build with a deploy step rolls out by digest: create a
	// deployment of the pushed image and enqueue it to the app's node.
	runnerDispatcher.SetDeployer(worker.NewPipelineDeployer(appRepo, deploymentRepo, producer))
	// Promotion environments + release catalog/approval gates.
	environmentRepo := repositories.NewEnvironmentRepository(db)
	environmentService := environment.NewService(environmentRepo)
	releaseService := releasesvc.NewService(releaseRepo, appRepo, environmentRepo, appService)
	// GitOps auto-sync sweep for sources set to automatic reconciliation.
	if cronManager != nil {
		if err := cronManager.RegisterTask("gitops_sync", 0, "GitOps auto-sync sweep", "*/3 * * * *", func() error {
			return gitopsService.ReconcileAuto(context.Background())
		}); err != nil {
			logger.Warn("failed to schedule GitOps auto-sync sweep", "error", err)
		}
		// Nightly image-catalog GC: prune orphaned build images, never collecting
		// a digest a live deployment or pinned release references.
		if err := cronManager.RegisterTask("image_gc", 0, "Image catalog GC", "0 4 * * *", func() error {
			_, err := imageService.GC(context.Background(), image.RetentionPolicy{}, dockerClient)
			return err
		}); err != nil {
			logger.Warn("failed to schedule image catalog GC", "error", err)
		}
		// Register `on.schedule` crons for every enabled pipeline and keep them in
		// sync as pipelines are created/updated/deleted.
		pipelineService.SetScheduler(pipelineCronScheduler{cron: cronManager, svc: pipelineService})
	}
	webhookRepo := repositories.NewWebhookRepository(db)
	webhookService := webhook.NewService(webhookRepo, repositories.NewWebhookDeliveryRepository(db), producer)
	notificationService := notify.NewService(repositories.NewNotificationChannelRepository(db), notify.NewRegistry())
	// Platform mailer: sends Miabi's own emails (password resets, workspace
	// invitations, account welcomes) over the system SMTP server. A no-op until
	// MIABI_SMTP_* is configured.
	platformMailer := mailer.NewService(cfg.SystemSMTP, cfg.AppName, cfg.AppWebURL)

	// Per-workspace key rotation: register the secret-owning services
	// as reencryptors with the live keyring, and schedule auto-rotation when on.
	var keyRotator *keyring.Service
	if kr, ok := crypto.CurrentKeyring().(*keyring.Service); ok {
		kr.Register(secretService)
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

	// Middleware
	jwtAuth := middlewares.JWTAuth(cfg, sessionStore)

	r := &Router{
		app:          app,
		cfg:          cfg,
		v1:           app.Group("/api/v1"),
		authenticate: middlewares.Authenticate(jwtAuth, apiKeyService, userRepo),
		scope:        middlewares.WorkspaceScope(workspaceRepo, customRoleRepo),
		systemAdmin:  middlewares.RequireSystemAdmin(userRepo),
		// Auth endpoints fall back to a local limiter if Redis is down (brute-force
		// stays throttled); agent tunnels fail open (availability over throttling).
		authRateLimit:    middlewares.RateLimit(redisClient, 10, time.Minute, true),
		agentRateLimit:   middlewares.RateLimit(redisClient, 120, time.Minute, false),
		ee:               ee,
		resourcePolicies: resourcePolicyRepo,
		h: routerHandlers{
			health:         handlers.NewHealthHandler(db, redisClient, dockerClient),
			auth:           handlers.NewAuthHandler(authService, userRepo, sessionRepo, auditLogger, settingsProvider, cfg.DevMode, cfg.PasswordResetEnabled),
			apiKey:         handlers.NewAPIKeyHandler(apiKeyService, apiKeyRepo, workspaceRepo, auditLogger),
			usage:          handlers.NewUsageHandler(quotaService, appRepo, dbRepo, volumeRepo, networkRepo, jobRepo, apiKeyRepo, workspaceRepo, repositories.NewRunnerRepository(db)),
			workspace:      handlers.NewWorkspaceHandler(workspaceService, accountService, auditRepo, userRepo, auditLogger, ee),
			app:            handlers.NewApplicationHandler(appService, bus, auditLogger),
			network:        handlers.NewNetworkHandler(networkService, auditLogger),
			stack:          handlers.NewStackHandler(stackService, auditLogger),
			route:          handlers.NewRouteHandler(routeService, settingsProvider, auditLogger),
			domain:         handlers.NewDomainHandler(domainService, auditLogger),
			dnsProvider:    handlers.NewDNSProviderHandler(dnsProviderService, auditLogger),
			middleware:     handlers.NewMiddlewareHandler(middlewareService, auditLogger),
			portBinding:    handlers.NewPortBindingHandler(portBindingService, auditLogger),
			database:       handlers.NewDatabaseHandler(databaseService, appService, forwardService, secretService, userRepo, auditLogger, clusterService),
			job:            handlers.NewJobHandler(jobService, auditLogger),
			secret:         handlers.NewSecretHandler(secretService, auditLogger),
			certificate:    handlers.NewCertificateHandler(certificateService, auditLogger),
			volume:         handlers.NewVolumeHandler(storageService, userRepo, auditLogger),
			backup:         handlers.NewBackupHandler(backupService, dbRepo, backupRepo, backupSettingsService, cronManager, auditLogger, cfg.RestoreMaxMB),
			backupSettings: handlers.NewWorkspaceBackupSettingsHandler(backupSettingsService, auditLogger),
			volumeBackup:   handlers.NewVolumeBackupHandler(volumeBackupService, volumeRepo, volumeBackupRepo, auditLogger),
			monitoring:     handlers.NewMonitoringHandler(monitoringService),
			marketplace:    handlers.NewMarketplaceHandler(marketplaceService, auditLogger),
			registry:       handlers.NewRegistryHandler(registryService, auditLogger),
			gitRepo:        handlers.NewGitRepositoryHandler(gitRepoService, auditLogger),
			apply:          handlers.NewApplyHandler(applyService, auditLogger),
			gitops:         handlers.NewGitOpsHandler(gitopsService, auditLogger),
			pipeline:       handlers.NewPipelineHandler(pipelineService, bus, auditLogger),
			image:          handlers.NewImageHandler(imageService, dockerClient, auditLogger),
			environment:    handlers.NewEnvironmentHandler(environmentService, auditLogger),
			release:        handlers.NewReleaseHandler(releaseService, auditLogger),
			events:         handlers.NewEventsHandler(eventsService, bus, appService, monitoringService),
			webhook:        handlers.NewWebhookHandler(webhookService, auditLogger),
			notification:   handlers.NewNotificationHandler(notificationService, auditLogger),
			node:           handlers.NewNodeHandler(nodeService, nodeManager, nodeGateway, dockerImportService, housekeepingService, clusterService, imageResolver, cfg.ControlURL, auditLogger, bus, cfg.HostProcPath),
			cluster:        handlers.NewClusterHandler(clusterService, nodeService, auditLogger),
			provider:       handlers.NewProviderHandler(nodeService, routeService),
			runner:         handlers.NewRunnerHandler(runnerService, auditLogger),
			runnerGateway:  handlers.NewRunnerGatewayHandler(runnerService, runnerManager),

			adminUser:           handlers.NewAdminUserHandler(db, userRepo, sessionRepo, workspaceRepo, auditRepo, sessionStore, auditLogger, accountService, cfg.DeletionGraceDays),
			adminWorkspace:      handlers.NewAdminWorkspaceHandler(db, workspaceRepo, auditLogger),
			adminDomain:         handlers.NewAdminDomainHandler(db, domainRepo, routeRepo, domainService, auditLogger),
			adminRoute:          handlers.NewAdminRouteHandler(db, routeRepo, workspaceRepo, routeService, auditLogger),
			adminMetrics:        handlers.NewAdminMetricsHandler(db, dockerClient, redisClient, time.Now()),
			adminEvent:          handlers.NewAdminEventHandler(auditRepo, bus, ee),
			adminSetting:        handlers.NewAdminSettingHandler(settingRepo, settingsProvider, auditLogger),
			update:              handlers.NewUpdateHandler(updateService),
			adminPlan:           handlers.NewPlanHandler(planRepo, quotaOverrideRepo, workspaceRepo, ee, auditLogger),
			deploymentCfg:       handlers.NewDeploymentConfigHandler(imageResolver, settingRepo, settingsProvider, auditLogger),
			adminJob:            handlers.NewAdminJobHandler(cronManager),
			adminPlatformBackup: handlers.NewAdminPlatformBackupHandler(platformBackupService, ee, auditLogger),
			adminRegistry:       handlers.NewAdminRegistryHandler(registryServerService, ee, auditLogger),
			registryServer:      handlers.NewRegistryServerHandler(registryServerService, workspaceRepo),
			oauthAdmin:          handlers.NewOAuthAdminHandler(oauthRepo, oauthService, ee, auditLogger),
			oauthPublic:         handlers.NewOAuthHandler(oauthService, oauthRepo, authService, sessionRepo, auditLogger, cfg),
			license:             handlers.NewLicenseHandler(ee, licenseNodeCount, func() int64 { n, _ := planRepo.Count(); return n }, installID, auditLogger),
			ssoAdmin:            handlers.NewSSOAdminHandler(orgRepo, samlConfigRepo, scimTokenRepo, ee, auditLogger),
			ldapAdmin:           handlers.NewLDAPAdminHandler(ldapRepo, ee, auditLogger),
			permission:          handlers.NewPermissionHandler(),
			customRole:          handlers.NewCustomRoleHandler(customRoleService, workspaceRepo, ee, auditLogger),
			auditExport:         handlers.NewAuditExportHandler(auditRepo, ee),
			resourcePolicy:      handlers.NewResourcePolicyHandler(resourcePolicyRepo, workspaceRepo, ee, auditLogger),
			siemAdmin:           handlers.NewSIEMAdminHandler(siemConfigRepo, siemStreamer, ee, auditLogger),
			adminRunner:         handlers.NewAdminRunnerHandler(runnerService, ee, auditLogger),
		},
	}

	// Platform notification emails (password reset, workspace invitation, welcome).
	r.h.auth.SetMailer(platformMailer)
	r.h.workspace.SetMailer(platformMailer)
	r.h.adminUser.SetMailer(platformMailer)
	r.h.adminUser.SetEnterprise(ee) // gate the per-user workspace-limit override

	// Shared execution-log store: deployment/pipeline/job log reads replay a
	// finished run's full history from the store and expose a full-log download
	// (nil-safe; falls back to the DB tail when disabled).
	// Surface cluster-mode availability as a workspace capability so any member —
	// not just a platform admin — can be offered the replicated "service" runtime
	// when creating an app (the admin-only cluster status endpoint can't drive this).
	r.h.usage.SetClusterCap(clusterService)
	r.h.app.SetLogStore(logStore)
	r.h.job.SetLogStore(logStore)
	r.h.pipeline.SetLogStore(logStore)
	r.h.backup.SetLogStore(logStore)
	r.h.volumeBackup.SetLogStore(logStore)
	r.h.adminPlatformBackup.SetLogStore(logStore)

	// Wire ACME managed-certificate issuance into the certificate handler.
	r.h.certificate.SetManaged(managedCertService)

	// Surface network subnet-pool utilization on the admin metrics dashboard.
	// Guarded so a typed-nil allocator isn't stored as a non-nil interface.
	if subnetAllocator != nil {
		r.h.adminMetrics.SetSubnetAllocator(subnetAllocator)
	}

	// Restrict browser WebSocket upgrades (exec/log/node/runner tunnels) to
	// same-origin plus the configured allowlist, blocking cross-site hijacking.
	wsOrigins := cfg.AllowedBrowserOrigins()
	r.h.app.SetAllowedOrigins(wsOrigins)
	r.h.node.SetAllowedOrigins(wsOrigins)
	r.h.runnerGateway.SetAllowedOrigins(wsOrigins)

	// Annotate runners' live-tunnel status from the runner connection manager.
	r.h.runner.SetConnRegistry(runnerManager)
	r.h.adminRunner.SetConnRegistry(runnerManager)

	// Wire per-workspace encryption-key rotation into the admin workspace handler.
	if keyRotator != nil {
		r.h.adminWorkspace.SetKeyRotator(keyRotator)
	}

	// Platform backup schedule (Enterprise): (re-)register the single platform
	// backup cron from stored settings. Only registered when the schedule is
	// enabled and the FlagPlatformBackup entitlement is present, so Community
	// incurs no platform-backup work. Surfaced in /admin/jobs like other crons.
	reschedulePlatformBackup := func(st *models.PlatformBackupSettings) {
		cronManager.UnregisterTask("platform-backup", 1)
		if st == nil || !st.ScheduleEnabled || st.ScheduleCron == "" || !r.ee.Has(enterprise.FlagPlatformBackup) {
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

	// Built-in registry: apply settings to the container on demand (after a
	// settings change) and once on boot, best-effort. Gated by Enabled inside
	// Ensure, so a disabled registry is a no-op on single-node installs.
	r.h.adminRegistry.SetEnsure(func(ctx context.Context) error {
		return registryServerService.Ensure(ctx, dockerClient)
	})
	r.h.adminRegistry.SetGC(func(ctx context.Context) error {
		return registryServerService.GarbageCollect(ctx, dockerClient)
	})
	go func() {
		if err := registryServerService.Ensure(context.Background(), dockerClient); err != nil {
			logger.Warn("internal registry ensure on boot failed", "error", err)
		}
	}()
	// Daily registry garbage collection (no-op unless the registry is enabled with
	// deletes on; flips read-only for the collect, then restores read-write).
	if err := cronManager.RegisterTask("registry_gc", 0, "Registry garbage collection", "0 4 * * *", func() error {
		return registryServerService.GarbageCollect(context.Background(), dockerClient)
	}); err != nil {
		logger.Error("failed to register registry GC cron", "error", err)
	}

	// Gate node container-log streaming by workspace membership: a platform admin
	// can list/operate containers, but not read another workspace's container logs.
	r.h.node.SetMembership(workspaceRepo)
	// Block stop/remove of managed containers from the admin node view unless the
	// operator has explicitly disabled security enforcement (break-glass).
	r.h.node.SetSecurityEnforcement(r.cfg.SecurityEnforcement)

	// Platform admins are always exempt — a misconfigured IdP must never lock the
	// whole instance out.
	r.h.auth.SetSSOEnforcement(func(user *models.User) bool {
		if user.IsAdmin() {
			return false
		}
		org, err := orgRepo.FindDefault()
		return err == nil && org.EnforceSSO
	})
	// Enterprise LDAP/AD fall-through on the primary login endpoint. No-op in
	// Community (directoryService.Login returns (nil,nil) when ee.LDAP() is nil).
	r.h.auth.SetDirectoryLogin(directoryService.Login)

	// Maintenance mode (admins and auth endpoints excepted).
	r.v1.Use(middlewares.Maintenance(cfg, settingsProvider))

	if cfg.MetricsEnabled {
		r.app.Get("/metrics", metrics.Handler(), okapi.DocHide())
	}

	r.app.Register(r.healthRoutes()...)
	r.app.Register(r.infoRoute())
	r.app.Register(r.permissionRoute())
	r.app.Register(r.authRoutes()...)
	r.app.Register(r.apiKeyRoutes()...)
	r.app.Register(r.workspaceRoutes()...)
	r.app.Register(r.roleRoutes()...)
	r.app.Register(r.applicationRoutes()...)
	r.app.Register(r.jobRoutes()...)
	r.app.Register(r.secretRoutes()...)
	r.app.Register(r.certificateRoutes()...)
	r.app.Register(r.networkRoutes()...)
	r.app.Register(r.stackRoutes()...)
	r.app.Register(r.routeRoutes()...)
	r.app.Register(r.domainRoutes()...)
	r.app.Register(r.dnsProviderRoutes()...)
	r.app.Register(r.middlewareRoutes()...)
	r.app.Register(r.portBindingRoutes()...)
	r.app.Register(r.databaseRoutes()...)
	r.app.Register(r.volumeRoutes()...)
	r.app.Register(r.volumeBackupRoutes()...)
	r.app.Register(r.backupRoutes()...)
	r.app.Register(r.workspaceBackupSettingsRoutes()...)
	r.app.Register(r.monitoringRoutes()...)
	r.app.Register(r.marketplaceRoutes()...)
	r.app.Register(r.registryRoutes()...)
	r.app.Register(r.gitRepositoryRoutes()...)
	r.app.Register(r.applyRoutes()...)
	r.app.Register(r.gitOpsRoutes()...)
	r.app.Register(r.pipelineRoutes()...)
	r.app.Register(r.imageRoutes()...)
	r.app.Register(r.environmentRoutes()...)
	r.app.Register(r.releaseRoutes()...)
	r.app.Register(r.webhookRoutes()...)
	r.app.Register(r.notificationRoutes()...)
	r.app.Register(r.nodeRoutes()...)
	r.app.Register(r.clusterRoutes()...)
	r.app.Register(r.runnerRoutes()...)
	r.app.Register(r.adminRunnerRoutes()...)
	r.app.Register(r.runnerGatewayRoutes()...)
	r.app.Register(r.placeableNodeRoutes()...)
	r.app.Register(r.agentRoutes()...)
	r.app.Register(r.providerRoutes()...)
	r.app.Register(r.adminRoutes()...)
	r.app.Register(r.registryServerRoutes()...)
	r.app.Register(r.oauthPublicRoutes()...)
	r.app.Register(r.ssoAdminRoutes()...)
	r.app.Register(r.samlPublicRoutes()...)
	r.app.Register(r.scimRoutes()...)

	// Serve the built web UI as an SPA (single-binary deployments). Registered
	// last so API routes win; Okapi auto-excludes their top-level segments
	// (e.g. /api, /healthz) so those keep returning JSON, not index.html.
	//
	// The UI is embedded in the binary (internal/web) and served by default, so a
	// stock `miabi` executable needs no static files. MIABI_WEB_DIR overrides this
	// to serve from disk — handy during frontend development (live rebuilds) or to
	// swap in a customized build without recompiling.
	if cfg.WebDir != "" {
		r.app.Web("/", cfg.WebDir, okapi.WebConfig{MaxAge: time.Hour})
	} else {
		r.app.WebFS("/", web.Assets, okapi.WebConfig{Root: "dist", MaxAge: time.Hour})
	}

	// Publish the current desired proxy state into the per-workspace file layout,
	// synchronously here before the server accepts traffic, so Goma's debounced
	// file watcher sees the converged state in a single reload.
	if err := routeService.ResyncAllProxy(context.Background()); err != nil {
		logger.Warn("proxy startup resync failed", "err", err)
	}

	return forwardService, runnerDispatcher
}

// RegisterFallbacks wires NoRoute/NoMethod handlers so router-level errors use
// the standard {success,data,error} envelope. Must be called before the server
// starts (Okapi binds these during applyCommon at startup), i.e. before RunServer.
func RegisterFallbacks(app *okapi.Okapi) {
	app.NoRoute(func(c *okapi.Context) error { return c.AbortNotFound("route not found") })
	app.NoMethod(func(c *okapi.Context) error { return c.AbortMethodNotAllowed("method not allowed") })
}

// healthRoutes returns the liveness/readiness probe definitions.
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

// permissionRoute exposes the static permission catalog + built-in role presets
// (for the role-picker UI). Authenticated; any member may read it.
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

// infoRoute returns the application info endpoint definition.
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
