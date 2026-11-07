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

// Package database provisions shared database server instances and manages the logical databases they host.
package database

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/platformimage"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/pkg/bundled"
)

var (
	ErrUnsupportedEngine  = errors.New("unsupported database engine")
	ErrSlugTaken          = errors.New("database name already taken in workspace")
	ErrNoLogicalDBs       = errors.New("this engine does not support multiple databases")
	ErrInstanceNotReady   = errors.New("database instance is not running yet")
	ErrNotFound           = errors.New("not found")
	ErrNameTaken          = errors.New("a database with this name already exists on the instance")
	ErrNoContainer        = errors.New("database has no container; re-provision it")
	ErrInstanceInUse      = errors.New("a database on this instance is attached to an application; detach it first")
	ErrInstanceLinked     = errors.New("this instance is linked to an application; unlink it first")
	ErrInstanceRunning    = errors.New("stop the database before deleting it")
	ErrNoWorkspaceNetwork = errors.New("could not resolve this workspace's network; the database was not created")
	ErrInstanceOwned      = errors.New("database is owned by another resource")
	// Upgrade errors.
	ErrInvalidVersion        = errors.New("invalid target version")
	ErrAlreadyOnVersion      = errors.New("instance is already on this version")
	ErrDowngrade             = errors.New("downgrade is not supported; data written by a newer version may be unreadable")
	ErrMongoMajorUpgrade     = errors.New("MongoDB major-version upgrades must be applied one major at a time and are not yet automated; create a new instance and migrate, or upgrade within the same major")
	ErrUpgradeInProgress     = errors.New("an upgrade is already in progress for this instance")
	ErrInstanceNotUpgradable = errors.New("instance must be running or stopped to upgrade")
	ErrDefaultNetwork        = errors.New("the workspace default network cannot be detached")
	ErrNoNetworkProvider     = errors.New("network management is not available")
	ErrInvalidResources      = errors.New("invalid database resources")
	ErrInstanceBusy          = errors.New("the instance is being provisioned or upgraded; try again once it is running")
)

type engineSpec struct {
	image                        func(version string) string
	defaultVersion               string
	port                         int
	dataDir                      string
	adminUser                    string
	adminEnv                     func(adminUser, adminPass string) []string
	cmd                          func(pass string) []string
	minMemoryMB, defaultMemoryMB int
	minMilliCPU, defaultMilliCPU int
}

var specs = map[models.DBEngine]engineSpec{
	models.DBEnginePostgres: {
		image:          func(v string) string { return "postgres:" + v },
		defaultVersion: bundled.PostgresVersion, port: 5432, dataDir: "/var/lib/postgresql/data",
		minMemoryMB: 256, defaultMemoryMB: 512, minMilliCPU: 250, defaultMilliCPU: 500,
		adminUser: "postgres",
		adminEnv: func(u, p string) []string {
			return []string{"POSTGRES_USER=" + u, "POSTGRES_PASSWORD=" + p}
		},
	},
	models.DBEngineMySQL: {
		image:          func(v string) string { return "mysql:" + v },
		defaultVersion: "8.4", port: 3306, dataDir: "/var/lib/mysql",
		minMemoryMB: 256, defaultMemoryMB: 1024, minMilliCPU: 300, defaultMilliCPU: 1000,
		adminUser: "root",
		adminEnv:  func(u, p string) []string { return []string{"MYSQL_ROOT_PASSWORD=" + p} },
	},
	models.DBEngineMariaDB: {
		image:          func(v string) string { return "mariadb:" + v },
		defaultVersion: "11", port: 3306, dataDir: "/var/lib/mysql",
		minMemoryMB: 256, defaultMemoryMB: 512, minMilliCPU: 300, defaultMilliCPU: 500,
		adminUser: "root",
		adminEnv:  func(u, p string) []string { return []string{"MARIADB_ROOT_PASSWORD=" + p} },
	},
	models.DBEngineRedis: {
		image:          func(v string) string { return "redis:" + v },
		defaultVersion: bundled.RedisVersion, port: 6379, dataDir: "/data",
		minMemoryMB: 64, defaultMemoryMB: 256, minMilliCPU: 100, defaultMilliCPU: 250,
		adminEnv: func(u, p string) []string { return nil },
		cmd:      func(p string) []string { return []string{"redis-server", "--requirepass", p} },
	},
	models.DBEngineMongoDB: {
		image:          func(v string) string { return "mongo:" + v },
		defaultVersion: "7.0", port: 27017, dataDir: "/data/db",
		minMemoryMB: 512, defaultMemoryMB: 1024, minMilliCPU: 500, defaultMilliCPU: 1000,
		adminUser: "admin",
		adminEnv: func(u, p string) []string {
			return []string{"MONGO_INITDB_ROOT_USERNAME=" + u, "MONGO_INITDB_ROOT_PASSWORD=" + p}
		},
	},
	models.DBEngineLibSQL: {
		image:          func(v string) string { return "ghcr.io/tursodatabase/libsql-server:" + v },
		defaultVersion: "latest", port: libsqlHTTPPort, dataDir: libsqlDataDir,
		minMemoryMB: 64, defaultMemoryMB: 256, minMilliCPU: 100, defaultMilliCPU: 250,
		adminEnv: func(u, p string) []string { return nil },
	},
}

// PostgreSQL 18+ rejects a volume at the pre-18 ".../data" path, so mount one level up for those versions.
func dataMount(engine models.DBEngine, version, defaultDir string) string {
	if engine == models.DBEnginePostgres && postgresMajor(version) >= 18 {
		return "/var/lib/postgresql"
	}
	return defaultDir
}

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

// Enqueuer schedules background provisioning; an interface avoids a database->worker import cycle.
type Enqueuer interface {
	EnqueueProvisionDB(instanceID, serverID uint) error
	EnqueueUpgradeDB(instanceID, serverID uint, target, path string, stopApps bool) error
	EnqueueResizeDB(instanceID, serverID uint, previous Resources) error
}

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

type NodeGuard interface {
	Placeable(serverID uint) error
}

type EventRecorder interface {
	EmitDatabase(workspaceID, databaseID uint, name string, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint)
	DeleteByDatabase(databaseID uint) error
}

// ServerInfo resolves a node's display metadata; optional, read paths show an empty server name when unset.
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

type Service struct {
	repo       *repositories.DatabaseRepository
	clients    NodeDocker
	enqueuer   Enqueuer
	nodeGuard  NodeGuard
	serverInfo ServerInfo
	// nil keeps every database on Docker's own data root.
	storage  StorageClassPlacer
	secrets  SecretWriter
	images   ImageResolver
	networks NetworkProvider
	swarm    SwarmNetworks
	quota    *quota.Service
	sizes    SizeCatalog
	ownerOf  OwnerExister
	apps     AppController
	backups  LogicalBackup
	// Shared with the embedded worker so worker-driven phase changes reach an open SSE stream; nil-safe.
	bus         *eventbus.Bus
	events      EventRecorder
	sizeSyncing sync.Map
}

// OwnerExister lets Delete refuse to orphan a database that still backs an app or stack.
type OwnerExister func(kind string, id, workspaceID uint) bool

// SetOwnerExister wires the owner-existence check used by Delete (nil-safe).
func (s *Service) SetOwnerExister(fn OwnerExister) { s.ownerOf = fn }

func NewService(repo *repositories.DatabaseRepository, clients NodeDocker, enqueuer Enqueuer) *Service {
	return &Service{repo: repo, clients: clients, enqueuer: enqueuer}
}

// SetEventRecorder wires the timeline recorder (nil-safe: unset means no events).
func (s *Service) SetEventRecorder(r EventRecorder) { s.events = r }

// emit is best-effort and nil-safe: recording must never break a lifecycle operation.
func (s *Service) emit(inst *models.DatabaseInstance, t models.AppEventType, sev models.AppEventSeverity, msg string, meta map[string]string) {
	if s.events == nil || inst == nil {
		return
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta["engine"] = string(inst.Engine)
	meta["version"] = inst.Version
	s.events.EmitDatabase(inst.WorkspaceID, inst.ID, inst.Name, t, sev, msg, meta, nil)
}

// SetEventBus must receive the same bus as the embedded worker so its phase changes reach open streams.
func (s *Service) SetEventBus(b *eventbus.Bus) { s.bus = b }

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetNodeGuard wires the placement guard consulted when provisioning on a node.
func (s *Service) SetNodeGuard(g NodeGuard) { s.nodeGuard = g }

type StorageClassPlacer interface {
	PlaceVolume(ctx context.Context, workspaceID, serverID uint, requested, dockerName string) (className, devicePath string, err error)
	ReclaimVolumeDir(ctx context.Context, serverID uint, className, dockerName string)
}

func (s *Service) SetStorageClasses(p StorageClassPlacer) { s.storage = p }

// Shared by provision and bring-up so a recreated volume lands on the same disk.
func (s *Service) dataVolumeSpec(inst *models.DatabaseInstance) docker.VolumeSpec {
	spec := docker.VolumeSpec{
		Name: inst.VolumeName,
		Labels: map[string]string{
			docker.LabelDatabase:  fmt.Sprint(inst.ID),
			docker.LabelWorkspace: fmt.Sprint(inst.WorkspaceID),
		},
		SizeBytes: inst.VolumeSizeBytes,
	}
	if s.storage != nil && inst.StorageClassName != "" && inst.StorageClassName != models.DefaultStorageClassName {
		// Re-derives the path rather than storing it: the class owns where it puts a volume.
		if _, devicePath, err := s.storage.PlaceVolume(
			context.Background(), inst.WorkspaceID, inst.ServerID, inst.StorageClassName, inst.VolumeName,
		); err == nil {
			spec.DevicePath = devicePath
		} else {
			logger.Warn("database volume: storage class unavailable, falling back to the data root",
				"instance", inst.ID, "class", inst.StorageClassName, "error", err)
		}
	}
	return spec
}

func (s *Service) SetSecrets(w SecretWriter) { s.secrets = w }

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

type NetworkProvider interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
	Get(workspaceID, id uint) (*models.Network, error)
}

// SetNetworkProvider wires workspace networks; when unset, databases fall back to the shared gateway network.
func (s *Service) SetNetworkProvider(p NetworkProvider) { s.networks = p }

// SwarmNetworks creates workspace networks in a remote swarm. Implemented by services/cluster.
type SwarmNetworks interface {
	WorkspaceOverlay(clusterID uint, n models.Network) bool
	EnsureWorkspaceOverlay(ctx context.Context, clusterID uint, n models.Network) error
}

func (s *Service) SetSwarmNetworks(sw SwarmNetworks) { s.swarm = sw }

// Overlays in a remote swarm go through its manager; other overlays already exist; bridges are created on the node.
func (s *Service) ensureNetwork(ctx context.Context, dc docker.Client, clusterID uint, n models.Network) error {
	switch {
	case s.swarm != nil && s.swarm.WorkspaceOverlay(clusterID, n):
		return s.swarm.EnsureWorkspaceOverlay(ctx, clusterID, n)
	case n.SwarmScoped():
		return nil
	}
	_, err := dc.EnsureNetwork(ctx, n.DockerName)
	return err
}

// Errors rather than falling back to the shared proxy network: the pin is permanent and would lose isolation.
func (s *Service) defaultNetwork(ctx context.Context, workspaceID uint) (*models.Network, error) {
	if s.networks == nil {
		return nil, ErrNoWorkspaceNetwork
	}
	n, err := s.networks.EnsureDefault(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoWorkspaceNetwork, err)
	}
	if n == nil || n.DockerName == "" {
		return nil, ErrNoWorkspaceNetwork
	}
	return n, nil
}

// Helper jobs (DDL, size probe, backup, restore) join the same networks so they resolve the instance by name.
func instanceNetworkNames(inst *models.DatabaseInstance) []string {
	return inst.NetworkNames(node.AppNetwork)
}

// Returning only the primary network risks one the instance is not on: "could not translate host name".
func (s *Service) ensureInstanceNetworks(ctx context.Context, dc docker.Client, inst *models.DatabaseInstance) ([]string, error) {
	names := instanceNetworkNames(inst)
	for _, name := range names {
		n := models.Network{DockerName: name}
		for i := range inst.Networks {
			if inst.Networks[i].DockerName == name {
				n = inst.Networks[i]
				break
			}
		}
		if err := s.ensureNetwork(ctx, dc, inst.ClusterID, n); err != nil {
			return nil, fmt.Errorf("ensure network %s: %w", name, err)
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

func (s *Service) resolveEngineImage(spec engineSpec, engine models.DBEngine, version string) (image, tag string) {
	ref := spec.image(spec.defaultVersion) // built-in default, e.g. bundled.PostgresImage
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

type EngineDefault struct {
	Engine  models.DBEngine `json:"engine"`
	Image   string          `json:"image"`   // effective default image ref
	Version string          `json:"version"` // default tag
	// The least the engine runs on, and what an unsized instance gets in a capped workspace.
	MinMemoryMB     int     `json:"min_memory_mb"`
	DefaultMemoryMB int     `json:"default_memory_mb"`
	MinCPUCores     float64 `json:"min_cpu_cores"`
	DefaultCPUCores float64 `json:"default_cpu_cores"`
}

func (s *Service) EngineDefaults() []EngineDefault {
	order := []models.DBEngine{models.DBEnginePostgres, models.DBEngineMySQL, models.DBEngineMariaDB, models.DBEngineRedis, models.DBEngineMongoDB, models.DBEngineLibSQL}
	out := make([]EngineDefault, 0, len(order))
	for _, engine := range order {
		spec, ok := specs[engine]
		if !ok {
			continue
		}
		image, tag := s.resolveEngineImage(spec, engine, "")
		out = append(out, EngineDefault{
			Engine: engine, Image: image, Version: tag,
			MinMemoryMB: spec.minMemoryMB, DefaultMemoryMB: spec.defaultMemoryMB,
			MinCPUCores: float64(spec.minMilliCPU) / 1000, DefaultCPUCores: float64(spec.defaultMilliCPU) / 1000,
		})
	}
	return out
}

// Legacy rows without a pinned image rebuild it from the engine default and stored version.
func (s *Service) engineImage(spec engineSpec, inst *models.DatabaseInstance) string {
	if inst.Image != "" {
		return inst.Image
	}
	img, _ := s.resolveEngineImage(spec, inst.Engine, inst.Version)
	return img
}

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

// No DDL runs: libSQL serves the database as soon as the container starts.
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

// A container reports running before sqld accepts connections, so poll /health before marking it running.
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

// Probes from a helper container: the control plane is not necessarily on the workspace network.
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

func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

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

// Deliberately narrower than unreachable: an offline node may still run the container; a deleted one is not coming back.
func (s *Service) nodeGone(inst *models.DatabaseInstance) bool {
	if inst == nil || s.serverInfo == nil {
		return false // unwired: assume the node is fine rather than destroy on a guess
	}
	if s.clients != nil && inst.ServerID == s.clients.LocalID() {
		return false // the control-plane node is never deleted
	}
	srv, err := s.serverInfo.Get(inst.ServerID)
	return err != nil || srv == nil
}

// Docker would create an empty volume and the engine would init over it; upgrade paths manage volumes themselves.
func (s *Service) guardData(ctx context.Context, inst *models.DatabaseInstance) error {
	return datavolume.CheckVolume(ctx, s.clients, inst.ServerID, inst.VolumeName, inst.VolumeEngineCreatedAt, inst.Name)
}

// ConnectionInfo is the (sensitive) connection detail for a database.
type ConnectionInfo struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	URI      string `json:"uri"`
	// The read-only user's credentials, when the database has one (Postgres).
	ReadOnlyUsername string `json:"readonly_username,omitempty"`
	ReadOnlyPassword string `json:"readonly_password,omitempty"`
	ReadOnlyURI      string `json:"readonly_uri,omitempty"`
}

func (s *Service) Provision(ctx context.Context, workspaceID, serverID uint, name string, engine models.DBEngine, version string, volumeSizeBytes int64, storageClass string, res Resources, meta, annotations models.Metadata) (*models.DatabaseInstance, error) {
	if volumeSizeBytes < 0 {
		volumeSizeBytes = 0
	}
	spec, ok := specs[engine]
	if !ok {
		return nil, ErrUnsupportedEngine
	}
	res, err := s.resolveSize(workspaceID, spec, res, false)
	if err != nil {
		return nil, err
	}
	if err := res.validate(engine, spec); err != nil {
		return nil, err
	}
	cpuCapped, memoryCapped := s.quota.DatabaseComputeCapped(workspaceID)
	res = res.withDefaults(spec, cpuCapped, memoryCapped)
	if s.quota.Enabled() {
		if err := s.quota.CheckDatabaseComputeAdd(workspaceID, res.NanoCPUs, res.MemoryBytes, 0); err != nil {
			return nil, err
		}
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

	// libSQL authenticates with a JWT: generate the keypair up front so the implicit database can carry a client token.
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
		MemoryBytes:      res.MemoryBytes,
		NanoCPUs:         res.NanoCPUs,
		SizeClass:        res.Size,
		Metadata:         models.DefaultManagedBy(meta, models.ManagedByUser),
		Annotations:      annotations,
	}
	if err := s.repo.Create(inst); err != nil {
		return nil, err
	}
	// Persisted once so connection strings injected into apps stay valid for the instance's lifetime.
	inst.Host = fmt.Sprintf("mb-db-%s-%d", slug.Token(8), inst.ID)
	inst.VolumeName = inst.Host + "-data"
	// The class directory is named after the volume, which carries the id, so placement happens only now.
	if s.storage != nil {
		className, _, perr := s.storage.PlaceVolume(ctx, workspaceID, serverID, storageClass, inst.VolumeName)
		if perr != nil {
			_ = s.repo.Delete(inst.ID)
			return nil, perr
		}
		inst.StorageClassName = className
	}
	// Refuse rather than fall back to the shared proxy network: this pin is permanent and would expose the DB to every tenant.
	defNet, nerr := s.defaultNetwork(ctx, workspaceID)
	if nerr != nil {
		return nil, nerr
	}
	inst.NetworkName = defNet.DockerName
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	if err := s.repo.AddNetwork(inst, defNet); err != nil {
		logger.Warn("attach default network to database", "instance", inst.ID, "error", err)
	}

	// Created up front so the volume exists even if the async bring-up lags or fails to pull; bringUp re-ensures it.
	if dc, derr := s.clients.For(serverID); derr == nil {
		dv, err := dc.CreateVolumeWith(ctx, s.dataVolumeSpec(inst))
		if err != nil {
			logger.Warn("failed to pre-create database volume", "id", inst.ID, "volume", inst.VolumeName, "error", err)
		} else {
			// Recorded so a volume deleted and recreated by hand later reads as replaced, not intact.
			_ = s.repo.SetVolumeEngineCreatedAt(inst.ID, dv.CreatedAt)
		}
	}

	switch engine {
	case models.DBEngineRedis:
		// Redis has no logical databases, so the instance owns its connection secrets.
		s.provisionInstanceSecrets(workspaceID, inst)
	case models.DBEngineLibSQL:
		// One implicit logical record lets the per-database backup and app-injection pipeline apply unchanged.
		if err := s.createLibsqlDatabase(workspaceID, inst, libsqlToken); err != nil {
			logger.Error("create implicit libsql database", "instance", inst.ID, "error", err)
		}
	}

	if err := s.enqueuer.EnqueueProvisionDB(inst.ID, inst.ServerID); err != nil {
		return nil, err
	}
	return inst, nil
}

// Built-in owner keys are authoritative, so this overrides any prior owner.
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
		s.emit(inst, models.EventDatabaseProvisionFailed, models.SeverityError, "Provisioning failed: "+err.Error(), nil)
		return err
	}
	// libSQL has no DDL or readiness probe: its single implicit database just tracks the container.
	if inst.Engine == models.DBEngineLibSQL {
		s.markLibsqlDatabaseRunning(ctx, inst)
	} else {
		s.applyPendingDatabases(ctx, inst)
	}
	s.publishStatus(inst) // bring-up succeeded → running
	s.emit(inst, models.EventDatabaseProvisioned, models.SeverityInfo, fmt.Sprintf("Provisioned %s %s", inst.Engine, inst.Version), nil)
	return nil
}

// Best-effort: a failure leaves the database provisioning, since failing bring-up is not safely retryable.
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
	// Otherwise the DDL races engine startup and leaves the logical database stuck provisioning without its role.
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
		if err := s.applyReadOnly(ctx, inst, d); err != nil {
			logger.Error("apply pending read-only user", "database", d.ID, "name", d.Name, "error", err)
		}
		d.Status = models.DBStatusRunning
		if err := s.repo.UpdateDatabase(d); err != nil {
			logger.Error("mark pending database running", "database", d.ID, "error", err)
		}
	}
}

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
	// A remote node won't have the workspace networks until something creates them.
	netNames, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return err
	}
	dv, err := dc.CreateVolumeWith(ctx, s.dataVolumeSpec(inst))
	if err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	// Only recorded while unset, so a hand-replaced volume keeps the original timestamp and the sweep detects data loss.
	_ = s.repo.SetVolumeEngineCreatedAt(inst.ID, dv.CreatedAt)
	image := s.engineImage(spec, inst)
	s.publishProgress(inst, "Pulling "+image)
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	var cmd []string
	if spec.cmd != nil {
		cmd = spec.cmd(adminPass)
	}
	// Official images prepend the server binary to dash-prefixed args, so tuning flags work with or without a command.
	cmd = append(cmd, tuningArgs(inst.Engine, inst.MemoryBytes)...)
	env := spec.adminEnv(inst.AdminUser, adminPass)
	if inst.Engine == models.DBEngineLibSQL {
		if env, err = libsqlServerEnv(inst); err != nil {
			return fmt.Errorf("build libsql env: %w", err)
		}
	}
	s.publishProgress(inst, "Starting container")
	// asynq retries may leave a container whose ID was never recorded; remove it to avoid "name already in use".
	_ = dc.RemoveContainer(ctx, inst.Host, true)
	containerID, err := dc.RunContainer(ctx, docker.RunSpec{
		Name:           inst.Host,
		Hostname:       inst.Host, // alias hostname (mb-db-<token>-<id>)
		Image:          image,
		Env:            env,
		Cmd:            cmd,
		Networks:       netNames,
		NetworkAliases: []string{inst.Host},
		MemoryBytes:    inst.MemoryBytes,
		NanoCPUs:       inst.NanoCPUs,
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

// LiveStatus is the pollable view of an instance: stored lifecycle status plus live Docker state when a container exists.
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
	NodeMissing    bool                    `json:"node_missing,omitempty"`
	Upgrade        *models.UpgradeProgress `json:"upgrade,omitempty"`
	Stats          *docker.StatsSample     `json:"stats,omitempty"`
}

// LiveStatus falls back to the stored status when there is no container or the node is unreachable.
func (s *Service) LiveStatus(ctx context.Context, inst *models.DatabaseInstance) LiveStatus {
	ls := LiveStatus{Status: string(inst.Status), StoredStatus: inst.Status, Upgrade: inst.Upgrade}
	// The stored-status fallback assumes the node still runs it, which no longer holds once the node is deleted.
	if s.nodeGone(inst) {
		ls.NodeMissing = true
		ls.Running = false
		ls.HasContainer = false
		ls.Status = string(models.DBStatusStopped)
		return ls
	}
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

// Platform lifecycle states are authoritative; otherwise live container state wins, telling user-stops from crashes.
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

// AttachNetwork connects the instance to another workspace network, live when its container exists; idempotent.
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
	// Best-effort: a stopped or missing container or offline node reconciles on the next bring-up.
	if inst.ContainerID != "" {
		if dc, derr := s.dockerFor(inst); derr == nil {
			if eerr := s.ensureNetwork(ctx, dc, inst.ClusterID, *net); eerr == nil {
				if cerr := dc.NetworkConnect(ctx, net.DockerName, inst.ContainerID, []string{inst.Host}); cerr != nil {
					logger.Warn("attach network to running database", "instance", inst.ID, "network", net.DockerName, "error", cerr)
				}
			}
		}
	}
	return s.Get(workspaceID, instanceID)
}

// DetachNetwork refuses the workspace default network, which carries the alias apps reach the instance by.
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

func annotateStorage(inst *models.DatabaseInstance) {
	if spec, ok := specs[inst.Engine]; ok {
		inst.MountPath = dataMount(inst.Engine, inst.Version, spec.dataDir)
	}
}

// StreamLogs follows the container's logs into sink; tail "" uses a default, and follow=false returns after the tail.
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
	if err := s.guardData(ctx, inst); err != nil {
		return err
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
	s.emit(inst, models.EventDatabaseStarted, models.SeverityInfo, "Instance started", nil)
	// Heals logical databases left pending by a racy bring-up; async so the request returns immediately.
	go s.applyPendingDatabases(context.Background(), inst)
	return nil
}

// Stop stops the container but keeps the data volume and records so the instance can start again.
func (s *Service) Stop(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	// A node that no longer exists cannot run the container; record that rather than failing forever.
	if s.nodeGone(inst) {
		return s.markStoppedOrphan(inst)
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
	s.emit(inst, models.EventDatabaseStopped, models.SeverityInfo, "Instance stopped", nil)
	return nil
}

// On a deleted node nothing runs, so the guard would make the instance undeletable for good.
func (s *Service) runningBlocksDelete(inst *models.DatabaseInstance) bool {
	return inst.Status == models.DBStatusRunning && !s.nodeGone(inst)
}

func (s *Service) markStoppedOrphan(inst *models.DatabaseInstance) error {
	inst.Status = models.DBStatusStopped
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseStopped, models.SeverityWarning, "Instance stopped: its node was removed", nil)
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
	if err := s.guardData(ctx, inst); err != nil {
		return err
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
	s.emit(inst, models.EventDatabaseRestarted, models.SeverityInfo, "Instance restarted", nil)
	// Heal any logical databases left pending from a racy bring-up (no-op when none).
	go s.applyPendingDatabases(context.Background(), inst)
	return nil
}

// Delete refuses while the instance runs or while any of its databases is attached to an application.
func (s *Service) Delete(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if s.runningBlocksDelete(inst) {
		return ErrInstanceRunning
	}
	if dbs, err := s.repo.ListDatabases(inst.ID); err == nil {
		for _, d := range dbs {
			if d.ApplicationID != nil {
				return ErrInstanceInUse
			}
		}
	}
	if links, err := s.repo.ListInstanceLinks(inst.ID); err == nil && len(links) > 0 {
		return ErrInstanceLinked
	}
	// Refuse while an owning app/stack still exists; stale owners don't block.
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
	// A class-backed volume's data sits behind a bind, so its directory outlives the Docker volume.
	if s.storage != nil && inst.VolumeName != "" {
		s.storage.ReclaimVolumeDir(ctx, inst.ServerID, inst.StorageClassName, inst.VolumeName)
	}
	if err := s.repo.Delete(inst.ID); err != nil {
		return err
	}
	for i := range logicalDBs {
		s.cascadeDeleteSecrets(inst.WorkspaceID, SecretOwnerDatabase, logicalDBs[i].ID)
	}
	s.cascadeDeleteSecrets(inst.WorkspaceID, SecretOwnerInstance, inst.ID)
	// The timeline is instance-scoped, so it would otherwise linger as unreachable rows.
	if s.events != nil {
		if err := s.events.DeleteByDatabase(inst.ID); err != nil {
			logger.Error("delete database events", "id", inst.ID, "error", err)
		}
	}
	return nil
}

// ListDatabases returns an instance's logical databases.
func (s *Service) ListDatabases(workspaceID, instanceID uint) ([]models.Database, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, instanceID); err != nil {
		return nil, ErrNotFound
	}
	return s.repo.ListDatabases(instanceID)
}

// Kind "instance" rows are Redis instance links: ID and Name are the instance's.
type AppDatabase struct {
	models.Database
	Kind         string          `json:"kind"`
	InstanceName string          `json:"instance_name"`
	Engine       models.DBEngine `json:"engine"`
	Host         string          `json:"host"`
	Port         int             `json:"port"`
}

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
		d.EnvVars = InjectedKeys(inst.Engine, d.EnvPrefix, d.EnvMap, d.EnvVars)
		out = append(out, AppDatabase{
			Database: d, Kind: LinkKindDatabase, InstanceName: inst.Name, Engine: inst.Engine, Host: inst.Host, Port: inst.Port,
		})
	}
	links, err := s.repo.ListInstanceLinksByApp(workspaceID, appID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		inst, err := s.repo.FindByID(l.InstanceID)
		if err != nil {
			continue
		}
		appID := l.ApplicationID
		out = append(out, AppDatabase{
			Database: models.Database{
				ID: inst.ID, WorkspaceID: inst.WorkspaceID, InstanceID: inst.ID, Name: inst.Name,
				Username: inst.AdminUser, Status: inst.Status, ApplicationID: &appID,
				EnvPrefix: l.EnvPrefix, EnvMap: l.EnvMap, EnvVars: InjectedKeys(inst.Engine, l.EnvPrefix, l.EnvMap, l.EnvVars),
				SizeBytes: inst.SizeBytes, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
			},
			Kind: LinkKindInstance, InstanceName: inst.Name, Engine: inst.Engine, Host: inst.Host, Port: inst.Port,
		})
	}
	return out, nil
}

// DatabaseConnectionForApp reveals the app's own scoped credentials, not the instance admin ones.
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

// AttachToApp links a logical database to an app; node-affinity and cross-app checks are the caller's job.
func (s *Service) AttachToApp(workspaceID, dbID, appID uint, env EnvLink) (*models.Database, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, dbID)
	if err != nil {
		return nil, ErrNotFound
	}
	d.ApplicationID = &appID
	d.EnvPrefix = env.Prefix
	d.EnvMap = env.Map
	d.EnvVars = env.Vars
	if err := s.repo.UpdateDatabase(d); err != nil {
		return nil, err
	}
	return d, nil
}

// DetachFromApp returns the database with its prior prefix so the caller can clean up injected env vars.
func (s *Service) DetachFromApp(workspaceID, dbID uint) (*models.Database, error) {
	d, err := s.repo.FindDatabaseInWorkspace(workspaceID, dbID)
	if err != nil {
		return nil, ErrNotFound
	}
	d.ApplicationID = nil
	d.EnvPrefix = ""
	d.EnvMap = nil
	d.EnvVars = nil
	if err := s.repo.UpdateDatabase(d); err != nil {
		return nil, err
	}
	return d, nil
}

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

// UniqueDatabaseName appends _2, _3, ... on collision so callers never hit ErrNameTaken.
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

// PrepareDatabase reserves a logical database without DDL; the CREATE runs once the instance is up.
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

// RecreateDatabase drops and recreates a logical database with the same credentials, for a force restore.
func (s *Service) RecreateDatabase(ctx context.Context, inst *models.DatabaseInstance, db *models.Database) error {
	if inst.Engine == models.DBEngineLibSQL {
		// libSQL has no DDL; a force restore replaces the data wholesale via the backup tool.
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
	// The role outlived the dropped database and needs its grants back before the dump that grants to it loads.
	return s.applyReadOnly(ctx, inst, db)
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
		s.dropReadOnlyRole(ctx, inst, d)
	}
	if err := s.repo.DeleteDatabase(d.ID); err != nil {
		return err
	}
	s.cascadeDeleteSecrets(d.WorkspaceID, SecretOwnerDatabase, d.ID)
	return nil
}

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

func (s *Service) DatabaseConnection(inst *models.DatabaseInstance, d *models.Database) (ConnectionInfo, error) {
	pass, err := crypto.Decrypt(d.PasswordEnc)
	if err != nil {
		return ConnectionInfo{}, err
	}
	info := ConnectionInfo{Host: inst.Host, Port: inst.Port, Username: d.Username, Password: pass, Database: d.Name}
	switch inst.Engine {
	case models.DBEnginePostgres:
		info.URI = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", d.Username, pass, inst.Host, inst.Port, d.Name)
		if d.ReadOnlyUsername != "" {
			ro, err := crypto.Decrypt(d.ReadOnlyPasswordEnc)
			if err != nil {
				return ConnectionInfo{}, err
			}
			info.ReadOnlyUsername, info.ReadOnlyPassword = d.ReadOnlyUsername, ro
			info.ReadOnlyURI = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", d.ReadOnlyUsername, ro, inst.Host, inst.Port, d.Name)
		}
	case models.DBEngineMySQL, models.DBEngineMariaDB:
		info.URI = fmt.Sprintf("mysql://%s:%s@%s:%d/%s", d.Username, pass, inst.Host, inst.Port, d.Name)
	case models.DBEngineMongoDB:
		// The scoped user is created on its own database, so authSource = the db.
		info.URI = fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s", d.Username, pass, inst.Host, inst.Port, d.Name, d.Name)
	case models.DBEngineLibSQL:
		// libSQL clients connect over HTTP with the JWT as authToken; pass is the token.
		info.URI = fmt.Sprintf("libsql://%s:%d?authToken=%s", inst.Host, inst.Port, pass)
	}
	return info, nil
}

// InstanceConnection returns the admin connection, used for Redis and diagnostics.
func (s *Service) InstanceConnection(inst *models.DatabaseInstance) (ConnectionInfo, error) {
	// libSQL has no admin credentials; its connection lives on the implicit logical database.
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

func (s *Service) execDDL(ctx context.Context, inst *models.DatabaseInstance, statements []string) error {
	return s.execDDLIn(ctx, inst, "", statements)
}

// execDDLIn is execDDL connected to database dbName ("" for the admin's own; Postgres only).
func (s *Service) execDDLIn(ctx context.Context, inst *models.DatabaseInstance, dbName string, statements []string) error {
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
	cmd, env := clientInvocationIn(inst, dbName, statements, adminPass)
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

// MarkNodeRemoved marks a deleted node's instances stopped but keeps rows, volume names and credentials.
func (s *Service) MarkNodeRemoved(serverID uint) {
	if serverID == 0 {
		return
	}
	instances, err := s.repo.ListAllInstances()
	if err != nil {
		logger.Warn("node removed: could not list database instances", "server", serverID, "error", err)
		return
	}
	for i := range instances {
		inst := &instances[i]
		if inst.ServerID != serverID || inst.Status == models.DBStatusStopped {
			continue
		}
		if err := s.markStoppedOrphan(inst); err != nil {
			logger.Warn("node removed: could not mark instance stopped",
				"server", serverID, "instance", inst.ID, "error", err)
			continue
		}
		logger.Info("database instance marked stopped: its node was removed",
			"server", serverID, "instance", inst.ID, "name", inst.Name, "workspace", inst.WorkspaceID)
	}
}

func (s *Service) AuditSharedNetworkInstances() {
	instances, err := s.repo.ListAllInstances()
	if err != nil {
		logger.Warn("shared-network database audit: list failed", "error", err)
		return
	}
	var affected int
	for i := range instances {
		inst := instances[i]
		if inst.NetworkName != "" && inst.NetworkName != node.AppNetwork {
			continue
		}
		affected++
		logger.Error("database instance is on the shared proxy network and is reachable by other workspaces; recreate it to move it",
			"instance", inst.ID, "name", inst.Name, "workspace", inst.WorkspaceID,
			"network", cmp.Or(inst.NetworkName, node.AppNetwork))
	}
	if affected > 0 {
		logger.Error("databases share the proxy network with other tenants", "count", affected)
	}
}
