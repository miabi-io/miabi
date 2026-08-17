// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/proxy"
	"github.com/miabi-io/miabi/internal/services/analytics"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/backupsettings"
	"github.com/miabi-io/miabi/internal/services/cluster"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/dbupgrade"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/events"
	"github.com/miabi-io/miabi/internal/services/gpu"
	"github.com/miabi-io/miabi/internal/services/image"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/notify"
	"github.com/miabi-io/miabi/internal/services/platformbackup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/volumebackup"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/worker"
)

// runWorker starts the standalone asynq worker. Live deploy-log SSE needs the embedded
// worker's in-process bus; this one persists logs but publishes no live events.
func runWorker() error {
	cfg := config.New()
	_ = cfg.InitWorker()
	cfg.InitStorage()
	crypto.Init(cfg.EncryptionKey)
	netguard.Configure(cfg.WebhookAllowPrivateTargets)

	node.SetAppNetwork(cfg.ProxyNetwork)

	db := cfg.Database.DB
	crypto.SetKeyring(keyring.NewService(repositories.NewWorkspaceKeyRepository(db)))
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	dockerClient, err := docker.New()
	if err != nil {
		logger.Fatal("failed to create docker client", "error", err)
	}
	defer func() { _ = dockerClient.Close() }()

	var proxyMgr proxy.Manager = proxy.NewMemory()
	if cfg.GomaProviderDir != "" {
		proxyMgr = proxy.NewGoma(cfg.GomaProviderDir)
	}
	producer := worker.NewProducer(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.WorkerMaxRetries)
	defer func() { _ = producer.Close() }()

	// With the filesystem backend this must be the same MIABI_LOG_DIR the control plane reads
	// (a shared mount) for a standalone worker's logs to be visible; nil when disabled.
	logStore := cfg.BuildLogStore()

	// Only the local node's Docker is reachable here (agent tunnels live in the server process).
	// Tasks placed on a remote node will fail; run the embedded worker for multi-node.
	var localID uint
	if local, err := repositories.NewServerRepository(db).FindLocal(); err == nil {
		localID = local.ID
	}
	producer.SetLocalID(localID)
	nodeClients := nodes.NewClients(localID, dockerClient)

	// Deployment-config image catalog, so worker-side services honor admin image overrides
	// instead of the built-in Docker Hub defaults.
	imageResolver := platformimage.New(settings.NewProvider(repositories.NewSettingRepository(db), nil), map[string]string{
		platformimage.KeyGoma:  cfg.NodeGatewayImage,
		platformimage.KeyRelay: cfg.ForwardRelayImage,
	})

	appRepo := repositories.NewApplicationRepository(db)
	eventRepo := repositories.NewAppEventRepository(db)
	eventsSvc := events.NewService(eventRepo, eventbus.New())
	// Record events fan out to workspace webhooks and channels via background tasks.
	eventsSvc.SetNotifier(notify.NewDispatcher(producer))
	webhookRepo := repositories.NewWebhookRepository(db)
	webhookDeliveryRepo := repositories.NewWebhookDeliveryRepository(db)
	channelRepo := repositories.NewNotificationChannelRepository(db)
	fanoutHandler := worker.NewFanoutHandler(webhookRepo, channelRepo, eventRepo, producer, cfg.Redis.Client)
	webhookHandler := worker.NewWebhookDeliverHandler(webhookRepo, webhookDeliveryRepo, eventRepo, appRepo)
	channelHandler := worker.NewChannelSendHandler(channelRepo, eventRepo, appRepo, notify.NewRegistry())
	secretService := secret.NewService(repositories.NewSecretRepository(db))
	// The worker re-syncs Goma on deploy, so its route service must apply the same
	// domain-verification gate as the API server — otherwise a deploy would re-render
	// unverified or banned routes as live.
	workerRouteSvc := route.NewService(repositories.NewRouteRepository(db), repositories.NewMiddlewareRepository(db), appRepo, repositories.NewReleaseRepository(db), repositories.NewServerRepository(db), repositories.NewPortBindingRepository(db), proxyMgr, cfg.HostPortMin, cfg.HostPortMax)
	workerRouteSvc.SetDomains(repositories.NewDomainRepository(db))
	workerRouteSvc.SetWorkspacePolicy(repositories.NewWorkspaceRepository(db))
	deployHandler := worker.NewDeployHandler(
		appRepo,
		repositories.NewDeploymentRepository(db),
		repositories.NewReleaseRepository(db),
		repositories.NewRegistryRepository(db),
		repositories.NewGitRepoRepository(db),
		repositories.NewPortBindingRepository(db),
		repositories.NewVolumeRepository(db),
		repositories.NewStackEnvVarRepository(db),
		repositories.NewRouteRepository(db),
		nodeClients,
		eventbus.New(),
		workerRouteSvc,
		eventsSvc,
		producer,
		secretService,
	)
	deployHandler.SetLogStore(logStore)
	// Serialize concurrent deploys of the same app across workers.
	deployHandler.SetDeployLock(worker.NewRedisDeployLock(cfg.Redis.Client))

	dbService := database.NewService(repositories.NewDatabaseRepository(db), nodeClients, producer)
	dbService.SetImageResolver(imageResolver) // honor admin image overrides on worker-side provisioning
	provisionHandler := worker.NewProvisionDBHandler(dbService)

	// A queued database version upgrade may run here, so the worker needs the app service
	// (quiesce/restart dependent apps) and the backup service (safety backup + data copy).
	upgradeAppService := application.NewService(
		appRepo,
		repositories.NewDeploymentRepository(db),
		repositories.NewReleaseRepository(db),
		repositories.NewVolumeRepository(db),
		repositories.NewRouteRepository(db),
		repositories.NewNetworkRepository(db),
		repositories.NewStackRepository(db),
		repositories.NewAppPortRepository(db),
		eventRepo,
		nodeClients,
		producer,
		eventsSvc,
	)
	upgradeAppService.SetPortBindings(repositories.NewPortBindingRepository(db))
	upgradeBackupService := backup.NewService(repositories.NewBackupRepository(db), repositories.NewDatabaseRepository(db), nodeClients)
	upgradeBackupService.SetDDLRunner(dbService)
	upgradeBackupService.SetImageResolver(imageResolver)
	upgradeBackupService.SetLogStore(logStore)
	dbService.SetAppController(dbupgrade.AppController(upgradeAppService))
	dbService.SetLogicalBackup(dbupgrade.Backup(upgradeBackupService))
	upgradeHandler := worker.NewUpgradeDBHandler(dbService)

	jobHandler := worker.NewJobHandler(
		repositories.NewJobRepository(db),
		appRepo,
		repositories.NewStackEnvVarRepository(db),
		repositories.NewRouteRepository(db),
		repositories.NewRegistryRepository(db),
		nodeClients,
		secretService,
	)
	jobHandler.SetLogStore(logStore)

	// Container security profile: deploy/job containers run as the platform
	// non-root UID when the workspace's plan (or the global default) is restricted.
	securityQuota := quota.NewService(
		repositories.NewPlanRepository(db),
		repositories.NewWorkspaceQuotaRepository(db),
		appRepo,
		repositories.NewVolumeRepository(db),
		repositories.NewDatabaseRepository(db),
		cfg.PlanEnforcement,
	)
	// The restricted profile is an Enterprise entitlement; without it the resolver clamps every
	// workspace back to the image's user. The same edition governs analytics retention below.
	edition := enterprise.New(db, cfg.LicensePublicKey, cfg.LicenseFile, cfg.DeploymentURL(), installIDOf(db))
	securityQuota.SetEdition(edition)
	securityQuota.SetForceNonRoot(cfg.ForceNonRootUser)
	securityResolver := newSecurityResolver(cfg, securityQuota)
	deployHandler.SetSecurity(securityResolver, cfg.SecurityInitImage)
	configService := configsvc.NewService(repositories.NewConfigRepository(db))
	deployHandler.SetConfigs(configService)
	deployHandler.SetBuilderPolicy(securityQuota)
	gpuScheduler := gpu.NewService(
		repositories.NewGPUDeviceRepository(db),
		repositories.NewServerRepository(db),
		appRepo,
		nodeClients,
		gpu.Config{Enabled: cfg.GPUEnabled, NvidiaRuntime: cfg.NvidiaRuntime, ProbeImage: cfg.GPUProbeImage},
	)
	gpuScheduler.SetQuota(securityQuota)
	deployHandler.SetGPU(gpuScheduler)
	jobHandler.SetSecurity(securityResolver, cfg.SecurityInitImage)
	// Managed-subnet allocator: overlay networks + remote-node network recreate
	// draw from the Miabi pool instead of Docker's default address pool.
	subnetAllocator := newSubnetAllocator(cfg, db)
	deployHandler.SetAllocator(subnetAllocator)
	jobHandler.SetAllocator(subnetAllocator)
	// Cluster mode: a routed app also joins the shared ingress overlay, so the central gateway
	// reaches it on any node without a published host port. This worker detects swarm state
	// itself and re-detects on interval, so enabling cluster mode needs no restart.
	clusterService := cluster.NewService(nodeClients, node.NewService(repositories.NewServerRepository(db), dockerClient))
	clusterService.Refresh(context.Background())
	go clusterService.RefreshLoop(context.Background(), 30*time.Second)
	deployHandler.SetCluster(clusterService)
	jobHandler.SetCluster(clusterService)
	// Git builds run on runners; here the resolver supplies the admin-controlled
	// builder image (passed to the runner) and the image catalog records provenance.
	deployHandler.SetBuildProvenance(
		imageResolver,
		image.NewService(repositories.NewImageRepository(db), repositories.NewReleaseRepository(db)),
	)
	// Distribute Git-built images via the internal registry so other nodes can pull them. The
	// workspace repository is required: resolving an image namespace to a workspace is how the
	// deploy path proves the image belongs to the app, and the check fails closed.
	registryDistributor := registryserver.NewService(
		repositories.NewRegistrySettingsRepository(db), imageResolver,
		settings.NewProvider(repositories.NewSettingRepository(db), nil),
		nil, repositories.NewWorkspaceRepository(db), nil, cfg.ProxyNetwork, cfg.ControlURL, cfg.Registry,
	)

	registryDistributor.SetEntitlements(edition)
	deployHandler.SetDistributor(registryDistributor)

	volumeBackupSvc := volumebackup.NewService(repositories.NewVolumeBackupRepository(db), repositories.NewVolumeRepository(db), nodeClients)
	volumeBackupSvc.SetImageResolver(imageResolver) // honor admin override for the volume-bkup image
	volumeBackupSvc.SetS3Provider(backupsettings.NewService(repositories.NewWorkspaceBackupSettingsRepository(db)))
	volumeBackupSvc.SetLogStore(logStore)
	volumeBackupHandler := worker.NewVolumeBackupHandler(volumeBackupSvc)

	pbHost, pbPort, pbName, pbUser, pbPass, pbSSL := cfg.Database.PostgresConn()
	platformBackupSvc := platformbackup.NewService(
		repositories.NewPlatformBackupRepository(db),
		repositories.NewPlatformBackupSetRepository(db),
		repositories.NewPlatformBackupSettingsRepository(db),
		nodeClients,
		platformbackup.DBConn{Host: pbHost, Port: pbPort, Name: pbName, User: pbUser, Password: pbPass, SSLMode: pbSSL},
		cfg.ProxyNetwork,
	)
	platformBackupSvc.SetImageResolver(imageResolver)
	platformBackupSvc.SetLogStore(logStore)
	// The worker runs backup ITEMS, so it resolves destination and passphrase itself. Without
	// the environment overlay it reads only the stored row — empty on a deployment configured
	// purely through MIABI_PLATFORM_BACKUP_*, failing every run with "no S3 target".
	platformBackupSvc.SetEnv(cfg.PlatformBackup)
	// Tenant artifacts are enqueued by the API server and RUN here, so this process needs the
	// tenant source too, or every tenant database and volume fails with "no tenant source".
	platformBackupSvc.EnableTenantCapture(db, upgradeBackupService)
	platformBackupHandler := worker.NewPlatformBackupHandler(platformBackupSvc)

	pipelineHandler := worker.NewPipelineHandler(
		repositories.NewPipelineRepository(db),
		appRepo,
		repositories.NewDeploymentRepository(db),
		repositories.NewGitRepoRepository(db),
		image.NewService(repositories.NewImageRepository(db), repositories.NewReleaseRepository(db)),
		nodeClients,
		eventbus.New(),
		producer,
	)
	pipelineHandler.SetLogStore(logStore)
	// Resolve a Git credential that references a workspace Secret instead of
	// storing its own copy of the token.
	pipelineHandler.SetSecrets(secretService)

	// A standalone worker has no agent tunnels, so it must not consume the remote-node queue —
	// those are reserved for the server's embedded worker. It still handles all local tasks.
	srv := worker.NewServer(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.WorkerConcurrency, false)

	// A standalone worker joins the same consumer group as the embedded one, so gateway events
	// are rolled up exactly once no matter how many workers run.
	if cfg.AnalyticsEnabled {
		analyticsConsumer := worker.NewAnalyticsConsumer(
			cfg.Redis.Client,
			repositories.NewRouteRepository(db),
			repositories.NewAnalyticsRepository(db),
			cfg.AnalyticsStream, analyticsConsumerName("worker"),
			time.Duration(cfg.AnalyticsFlushSeconds)*time.Second, analyticsRetention(cfg, edition),
			analytics.NewLiveTracker(cfg.Redis.Client, liveWindow(cfg)),
		)

		stopAnalytics := analyticsConsumer.Start(context.Background())
		defer stopAnalytics()
	}

	logger.Info("Miabi worker started", "version", config.Version, "concurrency", cfg.WorkerConcurrency)

	return srv.Run(worker.NewMux(deployHandler, provisionHandler, upgradeHandler, fanoutHandler, webhookHandler, channelHandler, jobHandler, volumeBackupHandler, pipelineHandler, platformBackupHandler, nil))
}
