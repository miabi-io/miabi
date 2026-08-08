// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package database provisions database server instances (PostgreSQL, MySQL,
// MariaDB, Redis) as containers and manages the logical databases they host, so
// a single instance can back many apps instead of one container per database.
package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrUnsupportedEngine = errors.New("unsupported database engine")
	ErrSlugTaken         = errors.New("database name already taken in workspace")
	ErrNoLogicalDBs      = errors.New("this engine does not support multiple databases")
	ErrInstanceNotReady  = errors.New("database instance is not running yet")
	ErrNotFound          = errors.New("not found")
	ErrNameTaken         = errors.New("a database with this name already exists on the instance")
	ErrNoContainer       = errors.New("database has no container; re-provision it")
	ErrInstanceInUse     = errors.New("a database on this instance is attached to an application; detach it first")
	ErrInstanceRunning   = errors.New("stop the database before deleting it")
	ErrInstanceOwned     = errors.New("database is owned by another resource")
	// Upgrade errors.
	ErrInvalidVersion        = errors.New("invalid target version")
	ErrAlreadyOnVersion      = errors.New("instance is already on this version")
	ErrDowngrade             = errors.New("downgrade is not supported; data written by a newer version may be unreadable")
	ErrMongoMajorUpgrade     = errors.New("MongoDB major-version upgrades must be applied one major at a time and are not yet automated; create a new instance and migrate, or upgrade within the same major")
	ErrUpgradeInProgress     = errors.New("an upgrade is already in progress for this instance")
	ErrInstanceNotUpgradable = errors.New("instance must be running or stopped to upgrade")
	ErrDefaultNetwork        = errors.New("the workspace default network cannot be detached")
	ErrNoNetworkProvider     = errors.New("network management is not available")
)

type engineSpec struct {
	image          func(version string) string
	defaultVersion string
	port           int
	dataDir        string
	adminUser      string
	adminEnv       func(adminUser, adminPass string) []string
	cmd            func(pass string) []string
}

var specs = map[models.DBEngine]engineSpec{
	models.DBEnginePostgres: {
		image:          func(v string) string { return "postgres:" + v },
		defaultVersion: "17-alpine", port: 5432, dataDir: "/var/lib/postgresql/data",
		adminUser: "postgres",
		adminEnv: func(u, p string) []string {
			return []string{"POSTGRES_USER=" + u, "POSTGRES_PASSWORD=" + p}
		},
	},
	models.DBEngineMySQL: {
		image:          func(v string) string { return "mysql:" + v },
		defaultVersion: "8.4", port: 3306, dataDir: "/var/lib/mysql",
		adminUser: "root",
		adminEnv:  func(u, p string) []string { return []string{"MYSQL_ROOT_PASSWORD=" + p} },
	},
	models.DBEngineMariaDB: {
		image:          func(v string) string { return "mariadb:" + v },
		defaultVersion: "11", port: 3306, dataDir: "/var/lib/mysql",
		adminUser: "root",
		adminEnv:  func(u, p string) []string { return []string{"MARIADB_ROOT_PASSWORD=" + p} },
	},
	models.DBEngineRedis: {
		image:          func(v string) string { return "redis:" + v },
		defaultVersion: "7-alpine", port: 6379, dataDir: "/data",
		adminEnv: func(u, p string) []string { return nil },
		cmd:      func(p string) []string { return []string{"redis-server", "--requirepass", p} },
	},
	models.DBEngineMongoDB: {
		image:          func(v string) string { return "mongo:" + v },
		defaultVersion: "7.0", port: 27017, dataDir: "/data/db",
		adminUser: "admin",
		// The official image enables authentication automatically when the root
		// credentials are set; the user is created in the `admin` database. No cmd
		// override is needed. mongosh (the DDL/query client) ships in mongo:6.0+.
		adminEnv: func(u, p string) []string {
			return []string{"MONGO_INITDB_ROOT_USERNAME=" + u, "MONGO_INITDB_ROOT_PASSWORD=" + p}
		},
	},
	models.DBEngineLibSQL: {
		image:          func(v string) string { return "ghcr.io/tursodatabase/libsql-server:" + v },
		defaultVersion: "latest", port: libsqlHTTPPort, dataDir: libsqlDataDir,
		// libSQL takes no admin user/password; the JWT-auth server env is built in
		// bringUp from the instance's keypair (it needs the public key).
		adminEnv: func(u, p string) []string { return nil },
	},
}

// dataMount is the in-container path the instance's data volume mounts at. PostgreSQL 18+ images store the
// cluster in a major-version subdirectory and reject a volume mounted at the pre-18 ".../data" path as a stray
// mount, so mounting one level up lets the data subdirectory live on the volume. Other engines keep their path.
func dataMount(engine models.DBEngine, version, defaultDir string) string {
	if engine == models.DBEnginePostgres && postgresMajor(version) >= 18 {
		return "/var/lib/postgresql"
	}
	return defaultDir
}

// postgresMajor parses the leading major version from a Postgres image tag such
// as "18", "18.1", "18-alpine", or "17-bookworm". Returns 0 when absent.
func postgresMajor(version string) int {
	n, digits := 0, 0
	for i := 0; i < len(version); i++ {
		c := version[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		digits++
	}
	if digits == 0 {
		return 0
	}
	return n
}

// Enqueuer schedules background provisioning. Implemented by the worker
// producer; an interface here avoids a database->worker import cycle.
type Enqueuer interface {
	EnqueueProvisionDB(instanceID, serverID uint) error
	EnqueueUpgradeDB(instanceID, serverID uint, target, path string, stopApps bool) error
}

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// NodeGuard validates that a node can accept a new placement (exists, not
// cordoned). Implemented by the node service; injected after construction.
type NodeGuard interface {
	Placeable(serverID uint) error
}

// ServerInfo resolves a node's display metadata by id. Implemented by the node
// service; injected after construction (optional — read paths degrade to an
// empty server name when unset, e.g. in the worker).
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

type Service struct {
	repo       *repositories.DatabaseRepository
	clients    NodeDocker
	enqueuer   Enqueuer
	nodeGuard  NodeGuard
	serverInfo ServerInfo
	secrets    SecretWriter
	images     ImageResolver
	networks   NetworkProvider
	quota      *quota.Service
	ownerOf    OwnerExister
	apps       AppController
	backups    LogicalBackup
	// bus fans out live instance status (provisioning, upgrade phases, start/stop)
	// to SSE subscribers. Shared with the embedded worker so worker-driven phase
	// changes reach an open detail-page stream. Nil-safe (no-op when unwired).
	bus         *eventbus.Bus
	sizeSyncing sync.Map
}

// OwnerExister reports whether an owning resource of the given kind/id still exists in the workspace,
// letting Delete refuse to orphan a database that still backs an app or stack. Wired by the
// composition root to avoid depending on the app/stack repositories directly.
type OwnerExister func(kind string, id, workspaceID uint) bool

// SetOwnerExister wires the owner-existence check used by Delete (nil-safe).
func (s *Service) SetOwnerExister(fn OwnerExister) { s.ownerOf = fn }

func NewService(repo *repositories.DatabaseRepository, clients NodeDocker, enqueuer Enqueuer) *Service {
	return &Service{repo: repo, clients: clients, enqueuer: enqueuer}
}

// SetEventBus wires the in-process bus used to stream live instance status over
// SSE. Inject the SAME bus into the HTTP-facing and embedded-worker services so
// worker-driven phase changes reach an open stream (nil-safe).
func (s *Service) SetEventBus(b *eventbus.Bus) { s.bus = b }

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetNodeGuard wires the placement guard consulted when provisioning on a node.
func (s *Service) SetNodeGuard(g NodeGuard) { s.nodeGuard = g }

// SetSecrets wires the Vault writer that auto-provisions a database's connection
// secrets and cascades their deletion. Optional (request-side only).
func (s *Service) SetSecrets(w SecretWriter) { s.secrets = w }

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// SetImageResolver wires the deployment-config resolver used to pin engine images
// at provision time. Optional (request-side; the worker uses the pinned image).
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// NetworkProvider resolves workspace Docker networks so a database joins the same
// networks its applications do — the default network always, plus any custom
// networks the user attaches. Implemented by the network service.
type NetworkProvider interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
	Get(workspaceID, id uint) (*models.Network, error)
}

// SetNetworkProvider wires the resolver for workspace networks, where databases
// run alongside the workspace's applications. Optional (request-side): when
// unset, databases fall back to the shared gateway network.
func (s *Service) SetNetworkProvider(p NetworkProvider) { s.networks = p }

// defaultNetwork resolves the workspace's default network record (where databases
// live alongside the workspace's apps), or nil if no provider is wired or the
// lookup fails — callers fall back to the gateway network.
func (s *Service) defaultNetwork(ctx context.Context, workspaceID uint) *models.Network {
	if s.networks == nil {
		return nil
	}
	n, err := s.networks.EnsureDefault(ctx, workspaceID)
	if err != nil || n == nil || n.DockerName == "" {
		logger.Warn("resolve default network for database; falling back to gateway", "workspace", workspaceID, "error", err)
		return nil
	}
	return n
}

// instanceNetworkNames is the full set of Docker networks the instance's own
// container — and every helper job (DDL, size probe, backup, restore) — joins
// so they share a network with the instance and resolve it by name.
func instanceNetworkNames(inst *models.DatabaseInstance) []string {
	return inst.NetworkNames(node.AppNetwork)
}

// ensureInstanceNetworks makes every network the instance is on exist on the node and returns their names, so
// a helper job attaches to the same full set the instance's container runs on — exactly as bringUp does.
// Returning the primary alone risks a network the instance is not on: "could not translate host name".
func (s *Service) ensureInstanceNetworks(ctx context.Context, dc docker.Client, inst *models.DatabaseInstance) ([]string, error) {
	names := instanceNetworkNames(inst)
	for _, n := range names {
		if _, err := dc.EnsureNetwork(ctx, n); err != nil {
			return nil, fmt.Errorf("ensure network %s: %w", n, err)
		}
	}
	return names, nil
}

func engineImageKey(engine models.DBEngine) string {
	switch engine {
	case models.DBEnginePostgres:
		return platformimage.KeyPostgres
	case models.DBEngineMySQL:
		return platformimage.KeyMySQL
	case models.DBEngineMariaDB:
		return platformimage.KeyMariaDB
	case models.DBEngineRedis:
		return platformimage.KeyRedis
	case models.DBEngineMongoDB:
		return platformimage.KeyMongoDB
	case models.DBEngineLibSQL:
		return platformimage.KeyLibSQL
	}
	return ""
}

// resolveEngineImage builds the full server image (repo:tag) for an engine, taking the repo (and mirror)
// from the deployment-config catalog and the tag from the requested version (falling back to the
// configured default tag). Returns the image ref and the effective tag.
func (s *Service) resolveEngineImage(spec engineSpec, engine models.DBEngine, version string) (image, tag string) {
	ref := spec.image(spec.defaultVersion) // built-in default, e.g. "postgres:17-alpine"
	if s.images != nil {
		if r := s.images.Ref(engineImageKey(engine)); r != "" {
			ref = r
		}
	}
	repo, defTag := platformimage.RepoTag(ref)
	tag = strings.TrimSpace(version)
	if tag == "" {
		tag = defTag
	}
	return repo + ":" + tag, tag
}

// EngineDefault is the resolved default image/version for an engine, used to
// prefill the create form so it reflects the admin's deployment-config tags.
type EngineDefault struct {
	Engine  models.DBEngine `json:"engine"`
	Image   string          `json:"image"`   // effective default image ref
	Version string          `json:"version"` // default tag
}

// EngineDefaults returns the resolved default image+version per engine (from the
// deployment-config catalog), in a stable order.
func (s *Service) EngineDefaults() []EngineDefault {
	order := []models.DBEngine{models.DBEnginePostgres, models.DBEngineMySQL, models.DBEngineMariaDB, models.DBEngineRedis, models.DBEngineMongoDB, models.DBEngineLibSQL}
	out := make([]EngineDefault, 0, len(order))
	for _, engine := range order {
		spec, ok := specs[engine]
		if !ok {
			continue
		}
		image, tag := s.resolveEngineImage(spec, engine, "")
		out = append(out, EngineDefault{Engine: engine, Image: image, Version: tag})
	}
	return out
}

// engineImage returns the image to run for an existing instance: the pinned ref
// if set, else rebuilt from the engine default + stored version (legacy rows).
func (s *Service) engineImage(spec engineSpec, inst *models.DatabaseInstance) string {
	if inst.Image != "" {
		return inst.Image
	}
	img, _ := s.resolveEngineImage(spec, inst.Engine, inst.Version)
	return img
}

// provisionSecrets creates/rotates the owned Vault secrets for a logical
// database (best-effort; logged on failure).
func (s *Service) provisionSecrets(workspaceID uint, inst *models.DatabaseInstance, d *models.Database) {
	if s.secrets == nil {
		return
	}
	conn, err := s.DatabaseConnection(inst, d)
	if err != nil {
		logger.Error("build connection for database secrets", "database", d.ID, "error", err)
		return
	}
	if _, err := s.secrets.UpsertOwned(workspaceID, SecretOwnerDatabase, d.ID, PasswordSecretName(inst, d), conn.Password, "Password for database "+d.Name); err != nil {
		logger.Error("create database password secret", "database", d.ID, "error", err)
	}
	if conn.URI != "" {
		_, _ = s.secrets.UpsertOwned(workspaceID, SecretOwnerDatabase, d.ID, URLSecretName(inst, d), conn.URI, "Connection URL for database "+d.Name)
	}
}

// provisionInstanceSecrets creates the owned Vault secrets for an instance that
// has no logical databases (Redis).
func (s *Service) provisionInstanceSecrets(workspaceID uint, inst *models.DatabaseInstance) {
	if s.secrets == nil {
		return
	}
	conn, err := s.InstanceConnection(inst)
	if err != nil {
		return
	}
	if _, err := s.secrets.UpsertOwned(workspaceID, SecretOwnerInstance, inst.ID, InstancePasswordSecretName(inst), conn.Password, "Password for "+inst.Name); err != nil {
		logger.Error("create instance password secret", "instance", inst.ID, "error", err)
	}
	if conn.URI != "" {
		_, _ = s.secrets.UpsertOwned(workspaceID, SecretOwnerInstance, inst.ID, InstanceURLSecretName(inst), conn.URI, "Connection URL for "+inst.Name)
	}
}

// createLibsqlDatabase creates the single implicit logical database for a libSQL instance, storing the
// client JWT (encrypted) as its "password". It is created in the provisioning state and marked running
// once sqld is up. No DDL runs — libSQL serves the database the moment the container starts.
func (s *Service) createLibsqlDatabase(workspaceID uint, inst *models.DatabaseInstance, clientToken string) error {
	encTok, err := crypto.EncryptWS(workspaceID, clientToken)
	if err != nil {
		return err
	}
	d := &models.Database{
		WorkspaceID: workspaceID, InstanceID: inst.ID, Name: libsqlDatabaseName,
		Username: libsqlUsername, PasswordEnc: encTok, Status: models.DBStatusProvisioning,
	}
	if err := s.repo.CreateDatabase(d); err != nil {
		return err
	}
	s.provisionSecrets(workspaceID, inst, d)
	return nil
}

// markLibsqlDatabaseRunning flips the implicit libSQL database to running once the server actually serves
// HTTP. libSQL has no readiness CLI and no DDL to apply, and a container reports "running" before sqld accepts
// connections — so poll /health first and leave the record provisioning (logged) if it never becomes ready.
func (s *Service) markLibsqlDatabaseRunning(ctx context.Context, inst *models.DatabaseInstance) {
	if err := s.waitLibsqlReady(ctx, inst); err != nil {
		logger.Error("libsql not ready; database left provisioning", "instance", inst.ID, "error", err)
		return
	}
	dbs, err := s.repo.ListDatabases(inst.ID)
	if err != nil {
		logger.Error("list libsql database", "instance", inst.ID, "error", err)
		return
	}
	for i := range dbs {
		if dbs[i].Status == models.DBStatusRunning {
			continue
		}
		dbs[i].Status = models.DBStatusRunning
		if err := s.repo.UpdateDatabase(&dbs[i]); err != nil {
			logger.Error("mark libsql database running", "database", dbs[i].ID, "error", err)
		}
	}
}

// waitLibsqlReady blocks until the libSQL server answers HTTP, polling /health from a tiny helper container
// on the instance's networks — the control-plane process is not necessarily on the workspace network, so it
// cannot reach the alias directly. Bounded; returns the last probe output on timeout.
func (s *Service) waitLibsqlReady(ctx context.Context, inst *models.DatabaseInstance) error {
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	nets, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return err
	}
	image := s.helperImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull helper image: %w", err)
	}
	url := fmt.Sprintf("http://%s:%d/health", inst.Host, inst.Port)
	deadline := time.Now().Add(90 * time.Second)
	var lastOut string
	for {
		exit, out, rerr := dc.RunOneShot(ctx, docker.RunSpec{
			Name:       fmt.Sprintf("mb-dbready-%d-%d", inst.ID, time.Now().UnixNano()%100000),
			Image:      image,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"wget -q -O /dev/null " + url},
			Networks:   nets,
		})
		if rerr == nil && exit == 0 {
			return nil
		}
		lastOut = out
		if time.Now().After(deadline) {
			return fmt.Errorf("libsql not ready after 90s: %s", strings.TrimSpace(lastOut))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// SetServerInfo wires the resolver used to annotate instances with their node's
// display name on read paths.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

// annotateServer fills the transient ServerName from the node record so the UI
// can show where an instance lives. Best-effort: leaves it empty on any error.
func (s *Service) annotateServer(inst *models.DatabaseInstance) {
	if inst == nil || s.serverInfo == nil {
		return
	}
	if srv, err := s.serverInfo.Get(inst.ServerID); err == nil && srv != nil {
		inst.ServerName = srv.Label()
	}
}

func (s *Service) dockerFor(inst *models.DatabaseInstance) (docker.Client, error) {
	return s.clients.For(inst.ServerID)
}

// ConnectionInfo is the (sensitive) connection detail for a database.
type ConnectionInfo struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	URI      string `json:"uri"`
}

// Provision creates the instance record (admin credentials and connection
// details known up front) and enqueues the container bring-up to the worker.
func (s *Service) Provision(ctx context.Context, workspaceID, serverID uint, name string, engine models.DBEngine, version string, volumeSizeBytes int64, meta, annotations models.Metadata) (*models.DatabaseInstance, error) {
	if volumeSizeBytes < 0 {
		volumeSizeBytes = 0
	}
	spec, ok := specs[engine]
	if !ok {
		return nil, ErrUnsupportedEngine
	}
	if s.quota.Enabled() {
		n, _ := s.repo.CountInstancesByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceDatabaseInstances, int(n)); err != nil {
			return nil, err
		}
		if err := s.quota.CheckInstanceSize(workspaceID, volumeSizeBytes); err != nil {
			return nil, err
		}
		if err := s.quota.CheckStorageAdd(workspaceID, volumeSizeBytes); err != nil {
			return nil, err
		}
	}
	if serverID == 0 {
		serverID = s.clients.LocalID()
	}
	if s.nodeGuard != nil {
		if err := s.nodeGuard.Placeable(serverID); err != nil {
			return nil, err
		}
	}
	if _, err := s.clients.For(serverID); err != nil {
		return nil, err // chosen node is offline
	}
	// Resolve the server image from the deployment-config catalog (repo + mirror)
	// with the requested version as the tag; pin it on the instance.
	image, version := s.resolveEngineImage(spec, engine, version)
	instName, err := slug.Unique(name, "db", func(c string) (bool, error) {
		return s.repo.ExistsByName(workspaceID, c)
	})
	if err != nil {
		return nil, err
	}

	adminPass := token(16)
	encPass, err := crypto.EncryptWS(workspaceID, adminPass)
	if err != nil {
		return nil, err
	}

	// libSQL authenticates with a JWT instead of a password: generate its keypair
	// up front so the implicit logical database (created below) can carry a client
	// token and bring-up can start sqld with the matching public key.
	var libsqlPrivEnc, libsqlToken string
	if engine == models.DBEngineLibSQL {
		if libsqlPrivEnc, libsqlToken, err = libsqlNewKeypair(workspaceID); err != nil {
			return nil, err
		}
	}

	inst := &models.DatabaseInstance{
		WorkspaceID: workspaceID, Name: instName, DisplayName: name, Engine: engine, Version: version,
		Status: models.DBStatusProvisioning, Port: spec.port, ServerID: serverID,
		Image: image, AdminUser: spec.adminUser, AdminPasswordEnc: encPass,
		JWTPrivateKeyEnc: libsqlPrivEnc,
		VolumeSizeBytes:  volumeSizeBytes,
		Metadata:         models.DefaultManagedBy(meta, models.ManagedByUser),
		Annotations:      annotations,
	}
	if err := s.repo.Create(inst); err != nil {
		return nil, err
	}
	// In-network host (also the container name + DNS alias): a random token plus
	// the id. Generated once here and persisted, so connection strings injected
	// into apps stay valid for the instance's lifetime.
	inst.Host = fmt.Sprintf("mb-db-%s-%d", slug.Token(8), inst.ID)
	inst.VolumeName = inst.Host + "-data"
	// Pin the network the instance runs on (the workspace's default network) so
	// every consumer — bring-up, DDL, size probes, backups, port-forward — agrees
	// on where to reach it, for the instance's lifetime.
	defNet := s.defaultNetwork(ctx, workspaceID)
	inst.NetworkName = node.AppNetwork
	if defNet != nil {
		inst.NetworkName = defNet.DockerName
	}
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	// Record the default network as an attached network (the user can attach more
	// later); the container joins every attached network at bring-up.
	if defNet != nil {
		if err := s.repo.AddNetwork(inst, defNet); err != nil {
			logger.Warn("attach default network to database", "instance", inst.ID, "error", err)
		}
	}

	// Create the data volume up front on the target node, so creating a database always creates its volume
	// immediately — independent of the async container bring-up (which, for heavier engines, may lag or fail
	// to pull). bringUp re-ensures it idempotently.
	if dc, derr := s.clients.For(serverID); derr == nil {
		if _, err := dc.CreateVolume(ctx, inst.VolumeName, map[string]string{docker.LabelDatabase: fmt.Sprint(inst.ID), docker.LabelWorkspace: fmt.Sprint(inst.WorkspaceID)}, inst.VolumeSizeBytes); err != nil {
			logger.Warn("failed to pre-create database volume", "id", inst.ID, "volume", inst.VolumeName, "error", err)
		}
	}

	switch engine {
	case models.DBEngineRedis:
		// Redis has no logical databases — its connection secrets are owned by the
		// instance and created here (credentials are known up front).
		s.provisionInstanceSecrets(workspaceID, inst)
	case models.DBEngineLibSQL:
		// libSQL hosts exactly one database, with no user-managed logical databases. Create the single implicit
		// logical record now — carrying the client token — so the existing per-database backup and app-injection
		// pipeline applies unchanged. It is marked running once sqld is up (markLibsqlDatabaseRunning).
		if err := s.createLibsqlDatabase(workspaceID, inst, libsqlToken); err != nil {
			logger.Error("create implicit libsql database", "instance", inst.ID, "error", err)
		}
	}

	if err := s.enqueuer.EnqueueProvisionDB(inst.ID, inst.ServerID); err != nil {
		return nil, err
	}
	return inst, nil
}

// SetOwner records the owning resource (app/stack/user) on an existing instance's metadata. Used to
// back-link a template's database to the app or stack it backs once those exist. Built-in keys are
// authoritative, so this overrides any prior owner.
func (s *Service) SetOwner(workspaceID, id uint, kind string, ownerID uint, name string) error {
	inst, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	inst.Metadata = models.SetOwner(inst.Metadata, kind, ownerID, name)
	return s.repo.Update(inst)
}

// RunProvision performs the container bring-up for an instance (worker-invoked).
func (s *Service) RunProvision(ctx context.Context, instanceID uint) error {
	inst, err := s.repo.FindByID(instanceID)
	if err != nil {
		return err
	}
	spec, ok := specs[inst.Engine]
	if !ok {
		return ErrUnsupportedEngine
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		inst.Status = models.DBStatusFailed
		_ = s.repo.Update(inst)
		s.publishStatus(inst)
		return err
	}
	// Apply any logical databases that were reserved before bring-up (e.g. a
	// marketplace install of a dedicated database). libSQL has no DDL/readiness
	// probe: its single implicit database just tracks the container.
	if inst.Engine == models.DBEngineLibSQL {
		s.markLibsqlDatabaseRunning(ctx, inst)
	} else {
		s.applyPendingDatabases(ctx, inst)
	}
	s.publishStatus(inst) // bring-up succeeded → running
	return nil
}

// applyPendingDatabases runs the deferred CREATE DATABASE/USER DDL for logical databases that were
// reserved (via PrepareDatabase) before the instance was running. Best-effort: a failure is logged and the
// database left provisioning rather than failing the bring-up, which is not safely retryable.
func (s *Service) applyPendingDatabases(ctx context.Context, inst *models.DatabaseInstance) {
	if !inst.SupportsLogicalDatabases() {
		return
	}
	dbs, err := s.repo.ListDatabases(inst.ID)
	if err != nil {
		logger.Error("list pending databases", "instance", inst.ID, "error", err)
		return
	}
	hasPending := false
	for i := range dbs {
		if dbs[i].Status == models.DBStatusProvisioning {
			hasPending = true
			break
		}
	}
	if !hasPending {
		return
	}
	// A container reports "running" before the engine accepts connections, so wait until a trivial admin query
	// succeeds. Otherwise the CREATE USER/DATABASE DDL races the engine's own startup, fails, and leaves the
	// logical database stuck provisioning — its role never created, so apps can't authenticate.
	if err := s.waitReady(ctx, inst); err != nil {
		logger.Error("engine not ready; logical databases left pending", "instance", inst.ID, "error", err)
		return
	}
	for i := range dbs {
		d := &dbs[i]
		if d.Status != models.DBStatusProvisioning {
			continue
		}
		pass, err := crypto.Decrypt(d.PasswordEnc)
		if err != nil {
			logger.Error("decrypt pending database password", "database", d.ID, "error", err)
			continue
		}
		if err := s.execDDL(ctx, inst, createDDL(inst.Engine, d.Name, d.Username, pass)); err != nil {
			logger.Error("apply pending database", "database", d.ID, "name", d.Name, "error", err)
			continue
		}
		d.Status = models.DBStatusRunning
		if err := s.repo.UpdateDatabase(d); err != nil {
			logger.Error("mark pending database running", "database", d.ID, "error", err)
		}
	}
}

// waitReady blocks until the engine accepts admin connections (a container reports
// "running" before the database is ready to serve). Bounded; returns the last
// probe error on timeout. Only meaningful for SQL engines.
func (s *Service) waitReady(ctx context.Context, inst *models.DatabaseInstance) error {
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for {
		if _, err := s.execQuery(ctx, inst, readinessProbe(inst.Engine)); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("engine not ready after 90s: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Service) bringUp(ctx context.Context, inst *models.DatabaseInstance, spec engineSpec, adminPass string) error {
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	// The instance runs on its attached workspace networks — shared with the
	// workspace's apps so they reach it by its alias. Ensure each exists on the
	// node (a remote node won't have them until something creates them).
	netNames, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return err
	}
	if _, err := dc.CreateVolume(ctx, inst.VolumeName, map[string]string{docker.LabelDatabase: fmt.Sprint(inst.ID), docker.LabelWorkspace: fmt.Sprint(inst.WorkspaceID)}, inst.VolumeSizeBytes); err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	image := s.engineImage(spec, inst)
	s.publishProgress(inst, "Pulling "+image)
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	var cmd []string
	if spec.cmd != nil {
		cmd = spec.cmd(adminPass)
	}
	// libSQL is JWT-authenticated: its server env is derived from the instance's
	// keypair rather than the admin password.
	env := spec.adminEnv(inst.AdminUser, adminPass)
	if inst.Engine == models.DBEngineLibSQL {
		if env, err = libsqlServerEnv(inst); err != nil {
			return fmt.Errorf("build libsql env: %w", err)
		}
	}
	s.publishProgress(inst, "Starting container")
	// A prior attempt may have created the container but died before recording its
	// ID (asynq retries this task on failure). Remove any leftover with our fixed
	// name first, so the retry doesn't fail permanently with "name already in use".
	_ = dc.RemoveContainer(ctx, inst.Host, true)
	containerID, err := dc.RunContainer(ctx, docker.RunSpec{
		Name:           inst.Host,
		Hostname:       inst.Host, // alias hostname (mb-db-<token>-<id>)
		Image:          image,
		Env:            env,
		Cmd:            cmd,
		Networks:       netNames,
		NetworkAliases: []string{inst.Host},
		Mounts:         map[string]string{inst.VolumeName: dataMount(inst.Engine, inst.Version, spec.dataDir)},
		Labels: map[string]string{
			docker.LabelDatabase:  fmt.Sprint(inst.ID),
			docker.LabelWorkspace: fmt.Sprint(inst.WorkspaceID),
		},
	})
	if err != nil {
		return fmt.Errorf("run container: %w", err)
	}
	s.publishProgress(inst, "Waiting for the database to become ready")
	if err := s.healthGate(ctx, dc, containerID); err != nil {
		_ = dc.RemoveContainer(context.Background(), containerID, true)
		return err
	}
	inst.ContainerID = containerID
	inst.Status = models.DBStatusRunning
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	logger.Info("database instance provisioned", "id", inst.ID, "engine", inst.Engine, "host", inst.Host)
	return nil
}

func (s *Service) healthGate(ctx context.Context, dc docker.Client, containerID string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c, err := dc.InspectContainer(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		switch c.State {
		case "running":
			return nil
		case "exited", "dead":
			return fmt.Errorf("database container exited during startup")
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("database did not become healthy in time")
}

func (s *Service) List(workspaceID uint) ([]models.DatabaseInstance, error) {
	insts, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range insts {
		s.annotateServer(&insts[i])
		annotateStorage(&insts[i])
	}
	return insts, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.DatabaseInstance, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.annotateServer(inst)
	annotateStorage(inst)
	return inst, nil
}

// LiveStatus is a lightweight, pollable view of an instance's state: the stored lifecycle status plus,
// when a container exists, its live Docker state and a stats snapshot. The detail page polls this so a
// user sees provisioning finish — or a crash — without a manual refresh.
type LiveStatus struct {
	Status         string                  `json:"status"` // headline: stored lifecycle, or live-derived when running
	StoredStatus   models.DBStatus         `json:"stored_status"`
	ContainerState string                  `json:"container_state,omitempty"`
	Health         string                  `json:"health,omitempty"`
	Running        bool                    `json:"running"`
	Restarting     bool                    `json:"restarting"`
	RestartCount   int                     `json:"restart_count"`
	ExitCode       int                     `json:"exit_code"`
	StartedAt      string                  `json:"started_at,omitempty"`
	UptimeSeconds  int64                   `json:"uptime_seconds"`
	HasContainer   bool                    `json:"has_container"`
	Upgrade        *models.UpgradeProgress `json:"upgrade,omitempty"`
	Stats          *docker.StatsSample     `json:"stats,omitempty"`
}

// LiveStatus inspects the instance's container and returns its real-time status.
// Falls back to the stored status when there is no container yet (provisioning),
// it was removed, or the node is unreachable — so polling always gets an answer.
func (s *Service) LiveStatus(ctx context.Context, inst *models.DatabaseInstance) LiveStatus {
	ls := LiveStatus{Status: string(inst.Status), StoredStatus: inst.Status, Upgrade: inst.Upgrade}
	if inst.ContainerID == "" {
		return ls // not brought up yet (provisioning) or no container — stored status is authoritative
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return ls // node offline — report the stored status
	}
	c, err := dc.InspectContainer(ctx, inst.ContainerID)
	if err != nil {
		return ls // container recorded but gone — stored status stands
	}
	ls.HasContainer = true
	ls.ContainerState = c.State
	ls.Health = c.Health
	ls.Running = c.State == "running"
	ls.Restarting = c.Restarting || c.State == "restarting"
	ls.RestartCount = c.RestartCount
	ls.ExitCode = c.ExitCode
	ls.StartedAt = c.StartedAt
	ls.Status = deriveDBStatus(inst.Status, c.State, c.Health, ls.Restarting)

	if ls.Running && c.StartedAt != "" {
		if t, perr := time.Parse(time.RFC3339Nano, c.StartedAt); perr == nil {
			ls.UptimeSeconds = int64(time.Since(t).Seconds())
		}
	}
	if ls.Running {
		if st, serr := dc.StatsOnce(ctx, inst.ContainerID); serr == nil {
			ls.Stats = &st
		}
	}
	return ls
}

// deriveDBStatus maps a container's Docker state + health into a headline status for the UI. Platform
// lifecycle states (provisioning/upgrading/failed) are authoritative and never overridden by the
// container; otherwise the live container state wins, distinguishing a user-stopped instance from a crash.
func deriveDBStatus(stored models.DBStatus, state, health string, restarting bool) string {
	switch stored {
	case models.DBStatusProvisioning, models.DBStatusUpgrading, models.DBStatusFailed:
		return string(stored)
	}
	switch state {
	case "restarting":
		return "restarting"
	case "paused", "created":
		return state
	case "exited", "dead":
		if stored == models.DBStatusStopped {
			return "stopped"
		}
		return "exited"
	case "running":
		if restarting {
			return "restarting"
		}
		switch health {
		case "unhealthy":
			return "unhealthy"
		case "starting":
			return "starting"
		}
		return "running"
	}
	return string(stored)
}

// AttachNetwork connects the instance to an additional workspace network and,
// when its container exists, attaches it live (no restart). Idempotent.
func (s *Service) AttachNetwork(ctx context.Context, workspaceID, instanceID, networkID uint) (*models.DatabaseInstance, error) {
	if s.networks == nil {
		return nil, ErrNoNetworkProvider
	}
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	net, err := s.networks.Get(workspaceID, networkID)
	if err != nil || net == nil {
		return nil, ErrNotFound
	}
	if err := s.repo.AddNetwork(inst, net); err != nil {
		return nil, err
	}
	// Live-attach the running (or stopped) container so the change takes effect
	// without recreating it. Best-effort: a stopped/missing container or offline
	// node reconciles on the next bring-up.
	if inst.ContainerID != "" {
		if dc, derr := s.dockerFor(inst); derr == nil {
			if _, eerr := dc.EnsureNetwork(ctx, net.DockerName); eerr == nil {
				if cerr := dc.NetworkConnect(ctx, net.DockerName, inst.ContainerID, []string{inst.Host}); cerr != nil {
					logger.Warn("attach network to running database", "instance", inst.ID, "network", net.DockerName, "error", cerr)
				}
			}
		}
	}
	return s.Get(workspaceID, instanceID)
}

// DetachNetwork disconnects the instance from a network. The workspace default
// network (which carries the alias apps reach the instance by) cannot be removed.
func (s *Service) DetachNetwork(ctx context.Context, workspaceID, instanceID, networkID uint) (*models.DatabaseInstance, error) {
	if s.networks == nil {
		return nil, ErrNoNetworkProvider
	}
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	net, err := s.networks.Get(workspaceID, networkID)
	if err != nil || net == nil {
		return nil, ErrNotFound
	}
	if net.IsDefault {
		return nil, ErrDefaultNetwork
	}
	if err := s.repo.RemoveNetwork(inst, net); err != nil {
		return nil, err
	}
	if inst.ContainerID != "" {
		if dc, derr := s.dockerFor(inst); derr == nil {
			if derr := dc.NetworkDisconnect(ctx, net.DockerName, inst.ContainerID, true); derr != nil {
				logger.Warn("detach network from running database", "instance", inst.ID, "network", net.DockerName, "error", derr)
			}
		}
	}
	return s.Get(workspaceID, instanceID)
}

// annotateStorage fills the transient MountPath from the engine spec's data dir
// (version-aware: PostgreSQL 18+ mounts one level up — see dataMount).
func annotateStorage(inst *models.DatabaseInstance) {
	if spec, ok := specs[inst.Engine]; ok {
		inst.MountPath = dataMount(inst.Engine, inst.Version, spec.dataDir)
	}
}

// StreamLogs follows the instance container's logs, invoking sink for each line. tail bounds the
// initial backlog ("" = a sensible default). When follow is true it then follows live output until the
// context is cancelled; when false it returns after the tail.
func (s *Service) StreamLogs(ctx context.Context, workspaceID, instanceID uint, follow bool, tail string, sink func(docker.LogLine) error) error {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return ErrNotFound
	}
	if inst.ContainerID == "" {
		return ErrNoContainer
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	return dc.StreamLogs(ctx, inst.ContainerID, follow, tail, sink)
}

// Start (re)starts a stopped instance container and marks it running.
func (s *Service) Start(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if inst.ContainerID == "" {
		return ErrNoContainer
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	if err := dc.StartContainer(ctx, inst.ContainerID); err != nil {
		return err
	}
	inst.Status = models.DBStatusRunning
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	// Heal any logical databases left pending from a racy bring-up (no-op when
	// none are pending). Async so the request returns immediately.
	go s.applyPendingDatabases(context.Background(), inst)
	return nil
}

// Stop stops the instance container and marks it stopped. The data volume and
// records are retained, so the instance can be started again later.
func (s *Service) Stop(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if inst.ContainerID == "" {
		return ErrNoContainer
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	if err := dc.StopContainer(ctx, inst.ContainerID, 10); err != nil {
		return err
	}
	inst.Status = models.DBStatusStopped
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	return nil
}

// Restart restarts the instance container and marks it running.
func (s *Service) Restart(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if inst.ContainerID == "" {
		return ErrNoContainer
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	if err := dc.RestartContainer(ctx, inst.ContainerID, 10); err != nil {
		return err
	}
	inst.Status = models.DBStatusRunning
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	// Heal any logical databases left pending from a racy bring-up (no-op when none).
	go s.applyPendingDatabases(context.Background(), inst)
	return nil
}

// Delete stops and removes the instance container, its data volume, and its logical database records
// (their data lives in the dropped container). Refuses while the instance is running, or when any of
// its databases is still attached to an application.
func (s *Service) Delete(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if inst.Status == models.DBStatusRunning {
		return ErrInstanceRunning
	}
	if dbs, err := s.repo.ListDatabases(inst.ID); err == nil {
		for _, d := range dbs {
			if d.ApplicationID != nil {
				return ErrInstanceInUse
			}
		}
	}
	// Refuse when the instance still backs an owning app/stack; delete the owner
	// instead of orphaning the database. Stale owners (already gone) don't block.
	if ref, ok := models.Owner(inst.Metadata); ok && ref.Kind != models.OwnerUser && ref.ID > 0 && s.ownerOf != nil && s.ownerOf(ref.Kind, ref.ID, inst.WorkspaceID) {
		name := ref.Name
		if name == "" {
			name = fmt.Sprintf("#%d", ref.ID)
		}
		return fmt.Errorf("%w: it backs %s %q — delete that instead", ErrInstanceOwned, ref.Kind, name)
	}
	// Collect logical databases before deletion so we can cascade their secrets.
	logicalDBs, _ := s.repo.ListDatabases(inst.ID)
	if dc, derr := s.dockerFor(inst); derr == nil {
		if inst.ContainerID != "" {
			_ = dc.StopContainer(ctx, inst.ContainerID, 10)
			_ = dc.RemoveContainer(ctx, inst.ContainerID, true)
		}
		if inst.VolumeName != "" {
			_ = dc.RemoveVolume(ctx, inst.VolumeName, true)
		}
	}
	if err := s.repo.Delete(inst.ID); err != nil {
		return err
	}
	for i := range logicalDBs {
		s.cascadeDeleteSecrets(inst.WorkspaceID, SecretOwnerDatabase, logicalDBs[i].ID)
	}
	s.cascadeDeleteSecrets(inst.WorkspaceID, SecretOwnerInstance, inst.ID)
	return nil
}

// ListDatabases returns an instance's logical databases.
func (s *Service) ListDatabases(workspaceID, instanceID uint) ([]models.Database, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, instanceID); err != nil {
		return nil, ErrNotFound
	}
	return s.repo.ListDatabases(instanceID)
}

// AppDatabase is a logical database enriched with its instance's engine and
// network address, for display on an application.
type AppDatabase struct {
	models.Database
	InstanceName string          `json:"instance_name"`
	Engine       models.DBEngine `json:"engine"`
	Host         string          `json:"host"`
	Port         int             `json:"port"`
}

// ListByApp returns the logical databases attached to an application, each with
// its instance's engine and address.
func (s *Service) ListByApp(workspaceID, appID uint) ([]AppDatabase, error) {
	dbs, err := s.repo.ListDatabasesByApp(workspaceID, appID)
	if err != nil {
		return nil, err
	}
	insts := map[uint]*models.DatabaseInstance{}
	out := make([]AppDatabase, 0, len(dbs))
	for i := range dbs {
		d := dbs[i]
		inst := insts[d.InstanceID]
		if inst == nil {
			if inst, err = s.repo.FindByID(d.InstanceID); err != nil {
				continue
			}
			insts[d.InstanceID] = inst
		}
		out = append(out, AppDatabase{
			Database: d, InstanceName: inst.Name, Engine: inst.Engine, Host: inst.Host, Port: inst.Port,
		})
	}
	return out, nil
}

// DatabaseConnectionForApp reveals the connection for a logical database that
// belongs to the given application (the app's own scoped credentials — not the
// instance admin credentials).
func (s *Service) DatabaseConnectionForApp(workspaceID, appID, dbID uint) (ConnectionInfo, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, dbID)
	if err != nil || d.ApplicationID == nil || *d.ApplicationID != appID {
		return ConnectionInfo{}, ErrNotFound
	}
	inst, err := s.repo.FindByID(d.InstanceID)
	if err != nil {
		return ConnectionInfo{}, ErrNotFound
	}
	return s.DatabaseConnection(inst, d)
}

// GetDatabase loads a logical database scoped to the workspace.
func (s *Service) GetDatabase(workspaceID, id uint) (*models.Database, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return d, nil
}

// AttachToApp links an existing logical database to an application, recording
// the env prefix used for its injected connection vars. Returns the updated
// database. Node-affinity and cross-app checks are the caller's responsibility.
func (s *Service) AttachToApp(workspaceID, dbID, appID uint, envPrefix string) (*models.Database, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, dbID)
	if err != nil {
		return nil, ErrNotFound
	}
	d.ApplicationID = &appID
	d.EnvPrefix = envPrefix
	if err := s.repo.UpdateDatabase(d); err != nil {
		return nil, err
	}
	return d, nil
}

// DetachFromApp unlinks a logical database from its application, clearing the
// owner and env prefix. Returns the updated database (with its prior prefix so
// the caller can clean up injected env vars before it is cleared on the row).
func (s *Service) DetachFromApp(workspaceID, dbID uint) (*models.Database, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, dbID)
	if err != nil {
		return nil, ErrNotFound
	}
	d.ApplicationID = nil
	d.EnvPrefix = ""
	if err := s.repo.UpdateDatabase(d); err != nil {
		return nil, err
	}
	return d, nil
}

// CreateDatabase provisions a new logical database (with its own scoped user) on
// a running SQL instance by running the engine client as a one-shot container.
func (s *Service) CreateDatabase(ctx context.Context, workspaceID, instanceID uint, name string, appID *uint) (*models.Database, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !inst.SupportsLogicalDatabases() {
		return nil, ErrNoLogicalDBs
	}
	if inst.Status != models.DBStatusRunning {
		return nil, ErrInstanceNotReady
	}
	if s.quota.Enabled() {
		n, _ := s.repo.CountDatabasesByInstance(inst.ID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceDatabasesPerInstance, int(n)); err != nil {
			return nil, err
		}
	}
	dbName := sanitizeIdent(name)
	if dbName == "" {
		dbName = "db_" + token(4)
	}
	if taken, err := s.repo.ExistsDatabaseByName(inst.ID, dbName); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrNameTaken
	}

	user := "u_" + token(4)
	pass := token(16)
	if err := s.execDDL(ctx, inst, createDDL(inst.Engine, dbName, user, pass)); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	encPass, err := crypto.EncryptWS(workspaceID, pass)
	if err != nil {
		return nil, err
	}
	d := &models.Database{
		WorkspaceID: workspaceID, InstanceID: inst.ID, Name: dbName,
		Username: user, PasswordEnc: encPass, Status: models.DBStatusRunning, ApplicationID: appID,
	}
	if err := s.repo.CreateDatabase(d); err != nil {
		return nil, err
	}
	// Auto-provision the database's connection secrets in the Vault.
	s.provisionSecrets(workspaceID, inst, d)
	return d, nil
}

// UniqueDatabaseName derives a sanitized logical-database name from base that is free on the instance,
// appending _2, _3, ... only on collision. This lets callers use a meaningful, readable name — the
// install's name — without ever hitting ErrNameTaken.
func (s *Service) UniqueDatabaseName(instanceID uint, base string) (string, error) {
	name := sanitizeIdent(base)
	if name == "" {
		name = "db_" + token(4)
	}
	candidate := name
	for i := 2; i <= 100; i++ {
		taken, err := s.repo.ExistsDatabaseByName(instanceID, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s_%d", name, i)
	}
	return name + "_" + token(4), nil // pathological fallback
}

// PrepareDatabase reserves a logical database (record + scoped credentials) WITHOUT running DDL, so a
// caller can wire an app's connection env before the instance has finished provisioning. The actual
// CREATE runs later, when the instance comes up. For a running instance use CreateDatabase.
func (s *Service) PrepareDatabase(workspaceID, instanceID uint, name string, appID *uint) (*models.Database, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !inst.SupportsLogicalDatabases() {
		return nil, ErrNoLogicalDBs
	}
	dbName := sanitizeIdent(name)
	if dbName == "" {
		dbName = "db_" + token(4)
	}
	if taken, err := s.repo.ExistsDatabaseByName(inst.ID, dbName); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrNameTaken
	}
	pass := token(16)
	encPass, err := crypto.EncryptWS(workspaceID, pass)
	if err != nil {
		return nil, err
	}
	d := &models.Database{
		WorkspaceID: workspaceID, InstanceID: inst.ID, Name: dbName,
		Username: "u_" + token(4), PasswordEnc: encPass,
		Status: models.DBStatusProvisioning, ApplicationID: appID,
	}
	if err := s.repo.CreateDatabase(d); err != nil {
		return nil, err
	}
	s.provisionSecrets(workspaceID, inst, d)
	return d, nil
}

// RecreateDatabase drops and recreates a logical database (and its user) with
// the same credentials — a clean slate for a force restore. Satisfies the backup
// service's DDLRunner.
func (s *Service) RecreateDatabase(ctx context.Context, inst *models.DatabaseInstance, db *models.Database) error {
	if inst.Engine == models.DBEngineLibSQL {
		// libSQL has no DROP/CREATE DDL; a force restore replaces the data wholesale
		// via the backup tool, so there is nothing to recreate here.
		return nil
	}
	if !inst.SupportsLogicalDatabases() {
		return ErrNoLogicalDBs
	}
	pass, err := crypto.Decrypt(db.PasswordEnc)
	if err != nil {
		return err
	}
	if err := s.execDDL(ctx, inst, dropDDL(inst.Engine, db.Name, db.Username)); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if err := s.execDDL(ctx, inst, createDDL(inst.Engine, db.Name, db.Username, pass)); err != nil {
		return fmt.Errorf("recreate database: %w", err)
	}
	return nil
}

// DeleteDatabase drops a logical database and its user from the instance.
func (s *Service) DeleteDatabase(ctx context.Context, workspaceID, id uint) error {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	inst, err := s.repo.FindByID(d.InstanceID)
	if err != nil {
		return ErrNotFound
	}
	// Best-effort drop on the server; always remove the record.
	if inst.Status == models.DBStatusRunning {
		if err := s.execDDL(ctx, inst, dropDDL(inst.Engine, d.Name, d.Username)); err != nil {
			logger.Error("drop logical database", "id", d.ID, "error", err)
		}
	}
	if err := s.repo.DeleteDatabase(d.ID); err != nil {
		return err
	}
	s.cascadeDeleteSecrets(d.WorkspaceID, SecretOwnerDatabase, d.ID)
	return nil
}

// cascadeDeleteSecrets removes the owned Vault secrets when a database/instance
// is deleted, warning if apps still referenced them.
func (s *Service) cascadeDeleteSecrets(workspaceID uint, ownerKind string, ownerID uint) {
	if s.secrets == nil {
		return
	}
	refs, err := s.secrets.DeleteOwned(workspaceID, ownerKind, ownerID)
	if err != nil {
		logger.Error("delete owned database secrets", "owner", ownerKind, "id", ownerID, "error", err)
		return
	}
	if len(refs) > 0 {
		logger.Warn("deleted database secrets still referenced by apps; their next deploy will fail until the reference is removed",
			"owner", ownerKind, "id", ownerID, "apps", len(refs))
	}
}

// DatabaseConnection returns the decrypted connection info for a logical
// database (sensitive — admin only).
func (s *Service) DatabaseConnection(inst *models.DatabaseInstance, d *models.Database) (ConnectionInfo, error) {
	pass, err := crypto.Decrypt(d.PasswordEnc)
	if err != nil {
		return ConnectionInfo{}, err
	}
	info := ConnectionInfo{Host: inst.Host, Port: inst.Port, Username: d.Username, Password: pass, Database: d.Name}
	switch inst.Engine {
	case models.DBEnginePostgres:
		info.URI = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", d.Username, pass, inst.Host, inst.Port, d.Name)
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		info.URI = fmt.Sprintf("mysql://%s:%s@%s:%d/%s", d.Username, pass, inst.Host, inst.Port, d.Name)
	case models.DBEngineMongoDB:
		// The scoped user is created on its own database, so authSource = the db.
		info.URI = fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s", d.Username, pass, inst.Host, inst.Port, d.Name, d.Name)
	case models.DBEngineLibSQL:
		// libSQL clients connect over HTTP with the JWT carried as authToken; `pass`
		// is the token, not a password.
		info.URI = fmt.Sprintf("libsql://%s:%d?authToken=%s", inst.Host, inst.Port, pass)
	}
	return info, nil
}

// InstanceConnection returns the admin/direct connection for an instance. Used
// for Redis (which has no logical databases) and for diagnostics.
func (s *Service) InstanceConnection(inst *models.DatabaseInstance) (ConnectionInfo, error) {
	// libSQL has no admin user/password; its connection lives on the single implicit
	// logical database (carrying the client token). Delegate to it.
	if inst.Engine == models.DBEngineLibSQL {
		dbs, err := s.repo.ListDatabases(inst.ID)
		if err != nil {
			return ConnectionInfo{}, err
		}
		if len(dbs) == 0 {
			return ConnectionInfo{}, ErrNotFound
		}
		return s.DatabaseConnection(inst, &dbs[0])
	}
	pass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return ConnectionInfo{}, err
	}
	info := ConnectionInfo{Host: inst.Host, Port: inst.Port, Username: inst.AdminUser, Password: pass}
	switch inst.Engine {
	case models.DBEnginePostgres:
		info.Database = inst.AdminUser
		info.URI = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", inst.AdminUser, pass, inst.Host, inst.Port, inst.AdminUser)
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		info.URI = fmt.Sprintf("mysql://%s:%s@%s:%d/", inst.AdminUser, pass, inst.Host, inst.Port)
	case models.DBEngineRedis:
		info.URI = fmt.Sprintf("redis://:%s@%s:%d", pass, inst.Host, inst.Port)
	case models.DBEngineMongoDB:
		info.URI = fmt.Sprintf("mongodb://%s:%s@%s:%d/?authSource=admin", inst.AdminUser, pass, inst.Host, inst.Port)
	}
	return info, nil
}

// execDDL runs SQL statements against the instance as admin, using the engine's
// own client image as a one-shot container on the instance's networks.
func (s *Service) execDDL(ctx context.Context, inst *models.DatabaseInstance, statements []string) error {
	spec := specs[inst.Engine]
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	nets, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return err
	}
	image := s.engineImage(spec, inst)
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull client image: %w", err)
	}
	cmd, env := clientInvocation(inst, statements, adminPass)
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:     fmt.Sprintf("mb-dbddl-%d-%d", inst.ID, time.Now().UnixNano()%100000),
		Image:    image,
		Env:      env,
		Cmd:      cmd,
		Networks: nets,
	})
	if err != nil || exit != 0 {
		return fmt.Errorf("ddl exited %d: %s", exit, out)
	}
	return nil
}

func token(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// IDByUID resolves a database instance's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
