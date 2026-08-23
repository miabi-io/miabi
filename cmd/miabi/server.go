// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/config"
	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/enterprise/license"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/routes"
	"github.com/miabi-io/miabi/internal/runners"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/cluster"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dbupgrade"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/gpu"
	"github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/monitoring"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/notify"
	"github.com/miabi-io/miabi/internal/services/platformbackup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/portforward"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/volumebackup"
	"github.com/miabi-io/miabi/internal/services/wsbackup"
	dbstorage "github.com/miabi-io/miabi/internal/storage"
	"github.com/miabi-io/miabi/internal/storage/logbackfill"
	"github.com/miabi-io/miabi/internal/storage/migration"
	"github.com/miabi-io/miabi/internal/storage/migration/upgrade"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// serverResources holds startup-created dependencies so shutdown can release them cleanly.
type serverResources struct {
	db           *gorm.DB
	redis        *redis.Client
	producer     *worker.Producer
	docker       docker.Client
	worker       *asynq.Server
	cron         *cronpkg.Manager
	forward      *portforward.Service
	stopScraper  context.CancelFunc
	cancelEvents context.CancelFunc
	// stopAnalytics stops the analytics consumer and waits for its final flush.
	stopAnalytics func()
	// entitlements is the license/edition snapshot captured at startup for the OnStarted log.
	entitlements enterprise.Entitlements
}

func runServer(cli *okapicli.CLI) {
	cfg := config.New()
	app := cli.Okapi()
	if err := cfg.Initialize(app); err != nil {
		logger.Fatal("failed to initialize Miabi", "error", err)
	}

	// Fallback handlers must be registered before the server starts.
	routes.RegisterFallbacks(app)

	res := &serverResources{}

	if err := cli.RunServer(&okapicli.RunOptions{
		ShutdownTimeout: 30 * time.Second,
		OnStart: func() {
			cfg.InitStorage()
			res.db = cfg.Database.DB
			res.redis = cfg.Redis.Client

			crypto.Init(cfg.EncryptionKey)

			netguard.Configure(cfg.WebhookAllowPrivateTargets)

			if err := migration.Run(res.db); err != nil {
				logger.Fatal("failed to run migrations", "error", err)
			}

			// Wired after migrations so the workspace_keys table exists.
			crypto.SetKeyring(keyring.NewService(repositories.NewWorkspaceKeyRepository(res.db)))

			if err := upgrade.Run(context.Background(), res.db, config.Version, upgrade.Options{
				AllowDowngrade: cfg.AllowDowngrade,
			}); err != nil {
				logger.Fatal("failed to run upgrade steps", "error", err)
			}

			res.producer = worker.NewProducer(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.WorkerMaxRetries)

			dockerClient, err := docker.New()
			if err != nil {
				logger.Fatal("failed to create docker client", "error", err)
			}
			res.docker = dockerClient
			node.SetAppNetwork(cfg.ProxyNetwork)
			serverRepo := repositories.NewServerRepository(res.db)
			nodeService := node.NewService(serverRepo, dockerClient)
			nodeService.Bootstrap(context.Background(), cfg.DockerHost)

			// The local node uses the direct engine client; remote nodes register a tunneled client
			// when their agent connects.
			var localID uint
			if local, err := serverRepo.FindLocal(); err == nil {
				localID = local.ID
			}
			res.producer.SetLocalID(localID)
			nodeClients := nodes.NewClients(localID, dockerClient)
			// Protect the control-plane's own container from being stopped or deleted
			// via the admin containers list (it carries no miabi.* label).
			if selfID := selfcontainer.Detect(); selfID != "" {
				nodeClients.SetLocalSelf(selfID)
				logger.Info("detected control-plane container", "id", selfID)
			}
			nodeManager := nodes.NewManager(nodeClients, nodeService)

			// Auto-detects whether the manager engine is a swarm manager; a no-op on plain Docker.
			// Refreshed once at boot so CapCluster is correct before the first request.
			clusterService := cluster.NewService(nodeClients, nodeService)
			clusterService.Refresh(context.Background())

			imageResolver := platformimage.New(
				settings.NewProvider(repositories.NewSettingRepository(res.db), nil),
				map[string]string{
					platformimage.KeyGoma:  cfg.NodeGatewayImage,
					platformimage.KeyRelay: cfg.ForwardRelayImage,
				},
			)
			nodeGateway := edgegateway.NewService(repositories.NewWorkspaceRepository(res.db), cfg.ControlURL, cfg.NodeGatewayImage, cfg.ProxyNetwork, cfg.AcmeEmail)
			nodeGateway.SetImageResolver(imageResolver)
			// The manager's own gateway reuses the platform Redis for shared cache +
			// distributed rate limiting (remote edge nodes get a per-node Redis).
			nodeGateway.SetRedis(cfg.Redis.Addr, cfg.Redis.Password)
			// Publish request events for Workspace Analytics — only meaningful while
			// this process runs the consumer that reads them.
			if cfg.AnalyticsEnabled {
				nodeGateway.SetAnalytics(cfg.AnalyticsStream)
			}
			// The manager's gateway can only watch route files if handed the volume Miabi writes them
			// to. A stack install mounts one there; a manual install yields nothing and the gateway
			// polls the HTTP provider instead.
			nodeGateway.SetProvidersVolume(
				edgegateway.DetectProvidersVolume(context.Background(), dockerClient, cfg.GomaProviderDir),
			)
			// When set, gateways encrypt sensitive config (middleware rules + TLS) at rest.
			nodeGateway.SetConfigEncryptionKey(cfg.GomaConfigEncryptionKey)
			nodeManager.SetOnConnect(func(ctx context.Context, srv *models.Server, token string, dc docker.Client) {

				clusterService.ReaffirmNode(ctx, srv.ID)
				if srv.Connectivity != models.ConnectivityEdgeGateway {
					return
				}
				// Use the node's recoverable gateway token (not the one-time join
				// token) so the same credential keeps working for on-demand redeploys.
				gwToken, err := nodeService.GatewayToken(srv.ID)
				if err != nil {
					logger.Error("failed to mint node gateway token", "node", srv.ID, "node_name", srv.Name, "error", err)
					return
				}
				// Remote edge nodes run a per-node Redis (the manager reuses the
				// platform Redis); mint/recover its stable password.
				var redisPw string
				if !srv.IsLocal {
					if pw, perr := nodeService.GatewayRedisPassword(srv.ID); perr == nil {
						redisPw = pw
					}
				}
				if err := nodeGateway.Ensure(ctx, dc, srv, gwToken, redisPw); err != nil {
					logger.Error("failed to deploy node gateway", "node", srv.ID, "node_name", srv.Name, "error", err)
					return
				}
				nodeService.MarkGatewayDeployed(srv.ID)
			})
			nodeManager.SetOnRemove(func(ctx context.Context, srv *models.Server, dc docker.Client) {
				if srv.Connectivity != models.ConnectivityEdgeGateway {
					return
				}
				nodeGateway.Teardown(ctx, dc)
			})

			var proxyMgr proxy.Manager
			if cfg.GomaProviderDir != "" {
				proxyMgr = proxy.NewGoma(cfg.GomaProviderDir)
				logger.Info("using Goma file-provider proxy", "dir", cfg.GomaProviderDir)
			} else {
				proxyMgr = proxy.NewMemory()
				logger.Warn("no MIABI_GOMA_PROVIDER_DIR set; using in-memory proxy (dev only)")
			}

			// In-process bus shared by the embedded worker (publishes) and the SSE handlers (subscribe).
			bus := eventbus.New()

			// Shared by the embedded worker (writes) and the SSE/download handlers (read); nil when off.
			logStore := cfg.BuildLogStore()

			// Unrecorded when the store is disabled, so enabling it later still backfills.
			// Idempotent and resumable across boots.
			if err := logbackfill.Run(context.Background(), res.db, logStore, cfg.LogStore.TailBytes, config.Version); err != nil {
				logger.Error("log store: backfill failed (will retry next boot)", "error", err)
			}

			// Embedded asynq worker: processes deploy tasks in this process by
			// default. Run `miabi worker` separately to scale it out.
			appEventRepo := repositories.NewApplicationRepository(res.db)
			eventRepo := repositories.NewAppEventRepository(res.db)
			eventsSvc := events.NewService(eventRepo, bus)
			// Record events fan out to workspace webhooks and channels via background tasks.
			eventsSvc.SetNotifier(notify.NewDispatcher(res.producer))
			// The engine derives deduplicated, auto-resolving alerts from the event stream and fans
			// per-user notifications over the bus. Redis holds the crash-loop/cooldown state.
			alertNamer := alerting.AppNameFunc(func(id uint) string {
				if a, err := appEventRepo.FindByID(id); err == nil && a != nil {
					if a.DisplayName != "" {
						return a.DisplayName
					}
					return a.Name
				}
				return ""
			})
			alertEngine := alerting.NewEngine(
				repositories.NewAlertRepository(res.db),
				repositories.NewNotificationInboxRepository(res.db),
				repositories.NewWorkspaceRepository(res.db),
				alertNamer, bus, alerting.NewRedisCounter(res.redis),
			)
			alertEngine.SetCertLister(repositories.NewCertificateRepository(res.db))
			alertEngine.SetVolumeLister(repositories.NewVolumeRepository(res.db))
			alertEngine.SetSystemAdmins(repositories.NewUserRepository(res.db))
			eventsSvc.SetAlertSink(alertEngine)
			// Platform-scoped node/runner alerts go to the system workspace, workspace runners to their
			// members. The runner manager is wired after InitRoutes returns it.
			palerter := &platformAlerter{
				e:  alertEngine,
				ws: repositories.NewWorkspaceRepository(res.db),
			}
			nodeManager.SetOnStatusChange(palerter.NodeStatus)
			// Runners are scanned, not hooked: their tunnels re-form too often to notify on raw events,
			// so the engine debounces (offline >=2m fires, back >=2m clears). It needs the system
			// workspace to scope shared runners to the super-admins.
			alertEngine.SetSystemWorkspace(palerter.systemWorkspace)
			alertEngine.SetRunnerLister(repositories.NewRunnerRepository(res.db))
			// Quota-scan and backup-outcome alerts are wired below, once their services exist.
			webhookRepo := repositories.NewWebhookRepository(res.db)
			webhookDeliveryRepo := repositories.NewWebhookDeliveryRepository(res.db)
			channelRepo := repositories.NewNotificationChannelRepository(res.db)
			notifyRegistry := notify.NewRegistry()
			fanoutHandler := worker.NewFanoutHandler(webhookRepo, channelRepo, eventRepo, res.producer, res.redis)
			webhookHandler := worker.NewWebhookDeliverHandler(webhookRepo, webhookDeliveryRepo, eventRepo, appEventRepo)
			channelHandler := worker.NewChannelSendHandler(channelRepo, eventRepo, appEventRepo, notifyRegistry)
			secretService := secret.NewService(repositories.NewSecretRepository(res.db))
			// The embedded deploy worker re-syncs Goma, so its route service must apply the same
			// domain-verification gate (and privileged-workspace waiver) as the HTTP service, or a
			// deploy would re-render unverified or banned routes as live.
			deployRouteSvc := route.NewService(repositories.NewRouteRepository(res.db), repositories.NewMiddlewareRepository(res.db), repositories.NewApplicationRepository(res.db), repositories.NewReleaseRepository(res.db), serverRepo, repositories.NewPortBindingRepository(res.db), proxyMgr, cfg.HostPortMin, cfg.HostPortMax)
			deployRouteSvc.SetDomains(repositories.NewDomainRepository(res.db))
			deployRouteSvc.SetWorkspacePolicy(repositories.NewWorkspaceRepository(res.db))
			deployHandler := worker.NewDeployHandler(
				repositories.NewApplicationRepository(res.db),
				repositories.NewDeploymentRepository(res.db),
				repositories.NewReleaseRepository(res.db),
				repositories.NewRegistryRepository(res.db),
				repositories.NewGitRepoRepository(res.db),
				repositories.NewPortBindingRepository(res.db),
				repositories.NewVolumeRepository(res.db),
				repositories.NewStackEnvVarRepository(res.db),
				repositories.NewRouteRepository(res.db),
				nodeClients, bus,
				deployRouteSvc,
				eventsSvc,
				res.producer,
				secretService,
			)
			deployHandler.SetLogStore(logStore)
			deployHandler.SetConfigs(configsvc.NewService(repositories.NewConfigRepository(res.db)))
			dbRepo := repositories.NewDatabaseRepository(res.db)
			dbService := database.NewService(dbRepo, nodeClients, res.producer)
			dbService.SetEventBus(bus)                // same bus as the HTTP service → worker phase changes reach open SSE streams
			dbService.SetImageResolver(imageResolver) // honor admin image overrides on worker-side provisioning
			provisionHandler := worker.NewProvisionDBHandler(dbService)

			// A database version upgrade may run here, so wire the app service (quiesce/restart
			// dependent apps) and the backup service (safety backup + data copy) it needs.
			upgradeAppService := application.NewService(
				repositories.NewApplicationRepository(res.db),
				repositories.NewDeploymentRepository(res.db),
				repositories.NewReleaseRepository(res.db),
				repositories.NewVolumeRepository(res.db),
				repositories.NewRouteRepository(res.db),
				repositories.NewNetworkRepository(res.db),
				repositories.NewStackRepository(res.db),
				repositories.NewAppPortRepository(res.db),
				repositories.NewAppEventRepository(res.db),
				nodeClients,
				res.producer,
				eventsSvc,
			)
			upgradeAppService.SetPortBindings(repositories.NewPortBindingRepository(res.db))
			upgradeBackupService := backup.NewService(repositories.NewBackupRepository(res.db), dbRepo, nodeClients)
			upgradeBackupService.SetDDLRunner(dbService)
			upgradeBackupService.SetImageResolver(imageResolver)
			upgradeBackupService.SetLogStore(logStore)
			dbService.SetAppController(dbupgrade.AppController(upgradeAppService))
			dbService.SetLogicalBackup(dbupgrade.Backup(upgradeBackupService))
			upgradeHandler := worker.NewUpgradeDBHandler(dbService)
			jobHandler := worker.NewJobHandler(
				repositories.NewJobRepository(res.db),
				repositories.NewApplicationRepository(res.db),
				repositories.NewStackEnvVarRepository(res.db),
				repositories.NewRouteRepository(res.db),
				repositories.NewRegistryRepository(res.db),
				nodeClients,
				secretService,
			)
			jobHandler.SetLogStore(logStore)

			// Container security profile: deploy/job containers run as the platform
			// non-root UID when the workspace plan (or the global default) is restricted.
			securityQuota := quota.NewService(
				repositories.NewPlanRepository(res.db),
				repositories.NewWorkspaceQuotaRepository(res.db),
				repositories.NewApplicationRepository(res.db),
				repositories.NewVolumeRepository(res.db),
				dbRepo,
				cfg.PlanEnforcement,
			)
			// The restricted profile is an Enterprise entitlement; without it the resolver
			// clamps every workspace back to the default (image's user).
			edition := enterprise.New(res.db, cfg.LicensePublicKey, cfg.LicenseFile, cfg.DeploymentURL(), installIDOf(res.db))
			securityQuota.SetEdition(edition)
			securityQuota.SetForceNonRoot(cfg.ForceNonRootUser)
			res.entitlements = edition.Entitlements()
			alertEngine.SetQuotaLister(quotaScanner{
				ws:   repositories.NewWorkspaceRepository(res.db),
				q:    securityQuota,
				apps: repositories.NewApplicationRepository(res.db),
				vols: repositories.NewVolumeRepository(res.db),
				dbs:  dbRepo,
			})
			securityResolver := newSecurityResolver(cfg, securityQuota)
			deployHandler.SetSecurity(securityResolver, cfg.SecurityInitImage)
			deployHandler.SetBuilderPolicy(securityQuota)
			gpuScheduler := gpu.NewService(
				repositories.NewGPUDeviceRepository(res.db),
				repositories.NewServerRepository(res.db),
				repositories.NewApplicationRepository(res.db),
				nodeClients,
				gpu.Config{Enabled: cfg.GPUEnabled, NvidiaRuntime: cfg.NvidiaRuntime, ProbeImage: cfg.GPUProbeImage},
			)
			gpuScheduler.SetQuota(securityQuota)
			deployHandler.SetGPU(gpuScheduler)
			jobHandler.SetSecurity(securityResolver, cfg.SecurityInitImage)
			// Managed-subnet allocator: overlay networks + remote-node network
			// recreate draw from the Miabi pool, not Docker's default address pool.
			subnetAllocator := newSubnetAllocator(cfg, res.db)
			deployHandler.SetAllocator(subnetAllocator)
			jobHandler.SetAllocator(subnetAllocator)
			// Cluster mode: a routed app also joins the shared ingress overlay, so the
			// central gateway reaches it on any node without a published host port.
			deployHandler.SetCluster(clusterService)
			jobHandler.SetCluster(clusterService)
			// Git builds run on runners: the resolver supplies the admin-controlled builder image and
			// the image catalog records build provenance.
			deployHandler.SetBuildProvenance(
				imageResolver,
				image.NewService(repositories.NewImageRepository(res.db), repositories.NewReleaseRepository(res.db)),
			)
			// Distribute Git-built images via the internal registry (no-op unless enabled with a
			// platform token) for multi-node pulls; it also resolves the host for runner pushes.
			registryDistributor := registryserver.NewService(
				repositories.NewRegistrySettingsRepository(res.db), imageResolver,
				settings.NewProvider(repositories.NewSettingRepository(res.db), nil),
				nil, repositories.NewWorkspaceRepository(res.db), nil, cfg.ProxyNetwork, cfg.ControlURL, cfg.Registry,
			)

			registryDistributor.SetEntitlements(edition)
			deployHandler.SetDistributor(registryDistributor)
			volumeBackupSvc := volumebackup.NewService(repositories.NewVolumeBackupRepository(res.db), repositories.NewVolumeRepository(res.db), nodeClients)
			volumeBackupSvc.SetImageResolver(imageResolver) // honor admin override for the volume-bkup image
			volumeBackupSvc.SetS3Provider(backupsettings.NewService(repositories.NewWorkspaceBackupSettingsRepository(res.db)))
			volumeBackupSvc.SetLogStore(logStore)
			volumeBackupHandler := worker.NewVolumeBackupHandler(volumeBackupSvc)

			pbHost, pbPort, pbName, pbUser, pbPass, pbSSL := cfg.Database.PostgresConn()
			platformBackupSvc := platformbackup.NewService(
				repositories.NewPlatformBackupRepository(res.db),
				repositories.NewPlatformBackupSetRepository(res.db),
				repositories.NewPlatformBackupSettingsRepository(res.db),
				nodeClients,
				platformbackup.DBConn{Host: pbHost, Port: pbPort, Name: pbName, User: pbUser, Password: pbPass, SSLMode: pbSSL},
				cfg.ProxyNetwork,
			)
			platformBackupSvc.SetImageResolver(imageResolver)
			platformBackupSvc.SetLogStore(logStore)
			// The embedded worker runs backup ITEMS, so it resolves destination and passphrase itself.
			// Without the environment overlay it reads only the stored settings row — empty on a
			// MIABI_PLATFORM_BACKUP_*-only deployment, failing every run with "no S3 target".
			platformBackupSvc.SetEnv(cfg.PlatformBackup)
			// Tenant artifacts are enqueued by the API but RUN here, so this process needs the tenant
			// source too. upgradeBackupService is reused because its DDL runner is what lets a tenant
			// restore recreate a database before loading a dump into it.
			platformBackupSvc.EnableTenantCapture(res.db, upgradeBackupService)
			platformBackupHandler := worker.NewPlatformBackupHandler(platformBackupSvc)

			pipelineHandler := worker.NewPipelineHandler(
				repositories.NewPipelineRepository(res.db),
				repositories.NewApplicationRepository(res.db),
				repositories.NewDeploymentRepository(res.db),
				repositories.NewGitRepoRepository(res.db),
				image.NewService(repositories.NewImageRepository(res.db), repositories.NewReleaseRepository(res.db)),
				nodeClients, bus, res.producer,
			)
			pipelineHandler.SetLogStore(logStore)
			// Resolve a Git credential that references a workspace Secret instead of
			// storing its own copy of the token.
			pipelineHandler.SetSecrets(secretService)
			// Translate Docker daemon events into application events (start/crash/oom/health): one
			// subscriber for the local node, plus one per remote node as agents connect and drop.
			eventCtx, cancelEvents := context.WithCancel(context.Background())
			res.cancelEvents = cancelEvents
			go events.NewSubscriber(dockerClient, appEventRepo, repositories.NewReleaseRepository(res.db), eventsSvc).Run(eventCtx)
			nodeManager.SetSubscriber(func(ctx context.Context, nodeID uint, dc docker.Client) {
				logger.Info("starting node event subscriber", "node", nodeID)
				events.NewSubscriber(dc, appEventRepo, repositories.NewReleaseRepository(res.db), eventsSvc).Run(ctx)
			})

			// Direct-access nodes (socket/api) have no inbound tunnel: build
			// their Docker clients up front and health-poll them on interval.
			go nodeManager.LoadDirect(eventCtx, 30*time.Second)

			// Periodically re-detect swarm state so the manager adapts when an admin
			// enables/disables cluster mode or swarm membership changes out of band.
			go clusterService.RefreshLoop(eventCtx, 30*time.Second)

			// Periodic, self-contained checks (TLS cert expiry/issuance) that aren't event-driven.
			// Fires and auto-resolves as certs renew.
			go alertEngine.ScanLoop(eventCtx)

			// Roll up Goma's per-request events into minute buckets. A standalone worker joins the
			// same consumer group, so events are rolled up exactly once.
			if cfg.AnalyticsEnabled {
				analyticsConsumer := worker.NewAnalyticsConsumer(
					res.redis,
					repositories.NewRouteRepository(res.db),
					repositories.NewAnalyticsRepository(res.db),
					cfg.AnalyticsStream, analyticsConsumerName("server"),
					time.Duration(cfg.AnalyticsFlushSeconds)*time.Second, analyticsRetention(cfg, edition),
					analytics.NewLiveTracker(res.redis, liveWindow(cfg)),
				)

				res.stopAnalytics = analyticsConsumer.Start(eventCtx)
			}

			backupRepo := repositories.NewBackupRepository(res.db)
			backupService := backup.NewService(backupRepo, dbRepo, nodeClients)
			backupService.SetImageResolver(imageResolver)
			backupService.SetLogStore(logStore)
			backupService.SetAlerter(backupAlerter{alertEngine})
			res.cron = cronpkg.NewManager(backupService, dbRepo, backupRepo, backupsettings.NewService(repositories.NewWorkspaceBackupSettingsRepository(res.db)))
			res.cron.Start()

			if logStore.Enabled() {
				_ = res.cron.RegisterTask("logstore", 0, "Log retention sweep", "@daily", func() error {
					n, err := logStore.Sweep(time.Now())
					if err == nil && n > 0 {
						logger.Info("log store: swept expired logs", "removed", n)
					}
					return err
				})
			}

			// Actively probe each connected node's tunnel and tear down any that stopped responding,
			// so a node that dropped ungracefully stops showing online.
			_ = res.cron.RegisterTask("node-health", 0, "Node health sweep", "@every 1m", func() error {
				nodeManager.ReconcileHealth(context.Background())
				return nil
			})

			var runnerDispatcher *runners.Dispatcher

			var wsBundleSvc *wsbackup.Service
			res.forward, runnerDispatcher, _, wsBundleSvc = routes.InitRoutes(app, res.db, res.redis, cfg, res.producer, dockerClient, nodeService, nodeManager, nodeGateway, clusterService, bus, proxyMgr, res.cron, logStore)

			// This process holds the runner tunnels, so its worker dispatches builds to runners for
			// both pipelines and git-source deploys. A run with none available waits up to
			// RunnerWaitTimeout. Wired before the worker starts.
			pipelineHandler.SetRunnerDispatch(runnerDispatcher, repositories.NewWorkspaceRepository(res.db), cfg.Registry.Host, registryDistributor, cfg.RunnerWaitTimeout)
			deployHandler.SetBuildDispatch(runnerDispatcher, cfg.Registry.Host, cfg.RunnerWaitTimeout)

			// Release leases whose deadline passed but whose release defer never ran (runner died or
			// the process restarted mid-job). A leaked lease counts against the runner's concurrency
			// forever, so a genuinely free runner is never scheduled.
			_ = res.cron.RegisterTask("runner-lease-sweep", 0, "Runner lease sweep", "@every 1m", func() error {
				expired, err := runnerDispatcher.SweepExpiredLeases(time.Now())
				if err != nil {
					return err
				}
				if len(expired) > 0 {
					logger.Info("runner lease sweep released expired leases", "count", len(expired))
				}
				return nil
			})

			// The embedded worker shares a process with the agent + runner tunnels, so it is the only
			// worker that consumes the remote-node queue and dispatches to runners.
			res.worker = worker.NewServer(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.WorkerConcurrency, true)
			if err := res.worker.Start(worker.NewMux(deployHandler, provisionHandler, upgradeHandler, fanoutHandler, webhookHandler, channelHandler, jobHandler, volumeBackupHandler, pipelineHandler, platformBackupHandler, worker.NewWorkspaceBundleHandler(wsBundleSvc))); err != nil {
				logger.Fatal("failed to start embedded worker", "error", err)
			}

			if cfg.MetricsEnabled {
				scrapeCtx, cancel := context.WithCancel(context.Background())
				res.stopScraper = cancel
				// This instance only drives the metrics scraper; the stack/event
				// repos (used by WorkspaceOverview) are not needed here.
				mon := monitoring.NewService(
					repositories.NewApplicationRepository(res.db),
					repositories.NewReleaseRepository(res.db),
					dbRepo,
					nil,
					nil,
					repositories.NewMetricRepository(res.db),
					nodeClients,
				)
				go mon.StartScraper(scrapeCtx,
					time.Duration(cfg.MetricsScrapeSeconds)*time.Second,
					time.Duration(cfg.MetricsRetentionHours)*time.Hour)
			}
		},
		OnStarted: func() {
			logger.Info("Miabi Server started",
				"version", config.Version,
				"commit", config.CommitID,
				"built", config.BuildDate,
				"edition", res.entitlements.Edition,
				"license", res.entitlements.State,
				"port", cfg.Port)
			// Surface an Enterprise license that isn't fully active at boot, so a grace/degraded/
			// binding-mismatch state doesn't hide until a paid feature silently stops working.
			if res.entitlements.Edition == enterprise.EditionEnterprise && res.entitlements.State != string(license.StateValid) {
				logger.Warn("Enterprise license is not fully active — check the license page",
					"state", res.entitlements.State, "binding_error", res.entitlements.BindingError)
			}
		},
		OnShutdown: func() {
			shutdownServer(res)
		},
	}); err != nil {
		logger.Fatal("server error", "error", err)
	}
}

// installIDOf returns the deployment's stable Install ID, generating it on first call.
func installIDOf(db *gorm.DB) string {
	id, _ := dbstorage.EnsureInstallID(db)
	return id
}

// analyticsConsumerName builds this process's unique name in the analytics consumer group,
// so pending-message ownership is per-process when several workers share it.
func analyticsConsumerName(role string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "node"
	}
	return role + "-" + host
}

// analyticsRetention resolves the effective retention days: the operator setting bounded by
// the license cap. Evaluated per prune, so a license change applies without a restart.
func analyticsRetention(cfg *config.Config, edition enterprise.EE) func() int {
	return func() int {
		return enterprise.ClampAnalyticsRetention(cfg.AnalyticsRetentionDays, edition.Entitlements().AnalyticsRetentionDays())
	}
}

// liveWindow resolves how long a visitor counts as live, so the consumer and API agree.
func liveWindow(cfg *config.Config) time.Duration {
	return time.Duration(cfg.AnalyticsLiveWindowSeconds) * time.Second
}

func shutdownServer(res *serverResources) {
	if res.stopScraper != nil {
		res.stopScraper()
	}
	if res.cancelEvents != nil {
		res.cancelEvents()
	}
	// Flush first: the open minute buckets exist only in the consumer until it stops.
	if res.stopAnalytics != nil {
		res.stopAnalytics()
	}
	if res.cron != nil {
		res.cron.Stop()
	}
	if res.forward != nil {
		res.forward.Shutdown()
	}
	if res.worker != nil {
		res.worker.Shutdown()
	}
	if res.docker != nil {
		_ = res.docker.Close()
	}
	if res.producer != nil {
		_ = res.producer.Close()
	}
	if res.redis != nil {
		_ = res.redis.Close()
	}
	if res.db != nil {
		if sqlDB, err := res.db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	logger.Info("Miabi Server stopped")
}
