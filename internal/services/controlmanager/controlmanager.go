// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package controlmanager watches for workloads that disappeared from under Miabi: an app whose active
// release container is gone from its node, a service app whose swarm service is gone from its cluster,
// and a volume whose data is gone from its node. Its scope is restoring what Miabi already decided, where
// Miabi decided it; it never places or moves a workload.
//
// Lost data is never repaired unattended. A volume that is gone is reported with the backup to restore it
// from, and every app mounting it is blocked: recreating the volume would hand the app an empty one, and
// starting a database on that initializes a new, empty cluster over the data it should have kept. So far
// nothing is acted on at all: findings become timeline events, alerts, metrics and an admin report.
package controlmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/settings"
)

// Mode is how far the control manager goes.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeObserve Mode = "observe"
	// ModeEnforce redeploys a container or service app that disappeared, where it already runs. It never
	// places or moves a workload, and never acts on an app that is missing anything else it needs.
	ModeEnforce Mode = "enforce"
)

// actorName signs the events and audit entries of work nobody asked for.
const actorName = "control-manager"

const (
	sweepTimeout = 50 * time.Second
	// nodeGrace keeps a freshly connected agent's partial view from reading as deleted containers.
	nodeGrace = time.Minute
	// confirmAfter is how many sweeps must see an item missing before it is reported, so a container caught
	// between remove and run by Miabi's own deploy path is never taken for a deleted one.
	confirmAfter = 2
)

// NodeDocker resolves node engines. Satisfied by *nodes.Clients.
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	ConnectedSince(serverID uint) (time.Time, bool)
}

// ClusterManager reaches a cluster's swarm manager. Satisfied by *cluster.Service.
type ClusterManager interface {
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
}

// AppStore lists the apps expected to be running. Satisfied by *repositories.ApplicationRepository.
type AppStore interface {
	ListReconcilable() ([]models.Application, error)
}

// ReleaseStore resolves an app's active release. Satisfied by *repositories.ReleaseRepository.
type ReleaseStore interface {
	FindActive(appID uint) (*models.Release, error)
}

// DeploymentStore reports the apps with a deploy under way, and how a redeploy the control manager started
// is getting on. Satisfied by *repositories.DeploymentRepository.
type DeploymentStore interface {
	InProgressAppIDs() ([]uint, error)
	FindByID(id uint) (*models.Deployment, error)
}

// VolumeStore reads volume rows and adopts the engine's creation timestamp into one that has none.
// Satisfied by *repositories.VolumeRepository.
type VolumeStore interface {
	ListAll() ([]models.Volume, error)
	SetEngineCreatedAt(id uint, at string) error
}

// DatabaseStore does the same for a database instance's data volume.
// Satisfied by *repositories.DatabaseRepository.
type DatabaseStore interface {
	ListAllInstances() ([]models.DatabaseInstance, error)
	SetVolumeEngineCreatedAt(id uint, at string) error
}

// VolumeBackupStore lists a volume's archives, so a report of lost data can name the one to restore.
// Satisfied by *repositories.VolumeBackupRepository. Optional: without it a finding simply suggests none.
type VolumeBackupStore interface {
	ListByVolume(volumeID uint) ([]models.VolumeBackup, error)
}

// DatabaseBackupStore lists an instance's recovery points, which is what a database is restored from.
// Satisfied by *repositories.DatabaseBackupSetRepository. Optional.
type DatabaseBackupStore interface {
	ListByInstance(instanceID uint) ([]models.DatabaseBackupSet, error)
}

// ServerStore lists the nodes, so the gateway that fronts each one can be checked too. Satisfied by
// *node.Service.
type ServerStore interface {
	List(ctx context.Context) ([]models.Server, error)
}

// GatewayEnsurer redeploys a node's own gateway, idempotently. Satisfied by the wiring in internal/routes,
// which mints the node's gateway token and calls edgegateway.Ensure. It is never used for a gateway Miabi did
// not deploy: an imported one, including the platform's own on the manager, belongs to whoever installed it.
type GatewayEnsurer interface {
	EnsureGateway(ctx context.Context, serverID uint) error
}

// Recorder writes timeline events. Satisfied by *events.Service.
type Recorder interface {
	Emit(workspaceID, appID uint, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint)
	EmitDatabase(workspaceID, databaseID uint, name string, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint)
}

// Settings reads platform settings. Satisfied by *settings.Provider.
type Settings interface {
	String(key, def string) string
}

// Service sweeps the platform for missing workloads.
type Service struct {
	nodes         NodeDocker
	clusters      ClusterManager
	apps          AppStore
	releases      ReleaseStore
	deploys       DeploymentStore
	volumes       VolumeStore
	databases     DatabaseStore
	volumeBackups VolumeBackupStore
	dbBackups     DatabaseBackupStore
	servers       ServerStore
	gateways      GatewayEnsurer
	events        Recorder
	settings      Settings
	now           func() time.Time

	// Enforcement (nil until SetEnforcement): what it takes to put an app back where it was.
	redeployer Redeployer
	placement  NodePlacement
	configs    ConfigStore
	auditor    Auditor

	mu              sync.Mutex
	findings        map[string]*Finding // by item key
	blocked         map[uint]string     // app id -> why it must not be redeployed
	attempts        map[string]*attempt // by item key: backoff, failures, breaker
	inflightDeploys map[uint]uint       // deployment id -> node id, for the in-flight budgets
	skipped         []Skip
	lastSweep       *time.Time
	sweepTook       time.Duration
}

// New builds the control manager. It does nothing until Tick is scheduled.
func New(nodes NodeDocker, clusters ClusterManager, apps AppStore, releases ReleaseStore, deploys DeploymentStore,
	volumes VolumeStore, databases DatabaseStore, events Recorder, settings Settings) *Service {
	return &Service{
		nodes: nodes, clusters: clusters, apps: apps, releases: releases, deploys: deploys,
		volumes: volumes, databases: databases, events: events, settings: settings,
		now:             time.Now,
		findings:        map[string]*Finding{},
		blocked:         map[uint]string{},
		attempts:        map[string]*attempt{},
		inflightDeploys: map[uint]uint{},
	}
}

// SetBackups wires the backup histories a report of lost data points at. Nil-safe: without them a finding
// still reports the loss, with no backup to name.
func (s *Service) SetBackups(volumes VolumeBackupStore, databases DatabaseBackupStore) {
	s.volumeBackups, s.dbBackups = volumes, databases
}

// SetGateways wires node-gateway watching: the nodes to check, and how to put a Miabi-deployed gateway back.
// Nil-safe — without them gateways are simply not watched.
func (s *Service) SetGateways(servers ServerStore, ensurer GatewayEnsurer) {
	s.servers, s.gateways = servers, ensurer
}

// Mode returns the configured mode. Off and enforce are both deliberate choices, so they are matched
// exactly; anything else observes, because an unrecognized value must neither switch detection off nor start
// redeploying things on its own.
func (s *Service) Mode() Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s.settings.String(settings.KeyControlManagerMode, "")))) {
	case ModeOff:
		return ModeOff
	case ModeEnforce:
		return ModeEnforce
	default:
		return ModeObserve
	}
}

// Tick runs one sweep with its own deadline. It is scheduled on the cron manager, which runs it only on the
// leading control plane, the process that holds the agent tunnels.
func (s *Service) Tick() error {
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()
	return s.Sweep(ctx)
}

// Sweep observes every expected workload once and updates the findings.
func (s *Service) Sweep(ctx context.Context) error {
	if s.Mode() == ModeOff {
		s.reset()
		return nil
	}
	start := s.now()
	items, err := s.plan(ctx)
	if err != nil {
		return err
	}
	seen, skipped := s.observe(ctx, items)
	s.record(items, seen, skipped, start)
	// Acting comes last, on what this sweep just confirmed: never on a finding it has not seen twice, and
	// never on one the same sweep marked blocked.
	s.act(ctx, items)
	return nil
}

// subject is a timeline an event about an item goes to.
type subject struct {
	workspaceID  uint
	appID        uint
	databaseID   uint
	databaseName string
}

// item is one thing the sweep expects to exist: an app's container, an app's swarm service, or the
// volume holding an app's or a database's data.
type item struct {
	key   string // unique across kinds: "app:7", "volume:3", "dbvolume:5"
	kind  string // container | service | volume
	name  string
	ref   string
	owner string // drift.Owner*
	id    uint   // the owning record's id

	workspaceID uint
	nodeID      uint
	clusterID   uint

	// app is carried so enforcement can redeploy it exactly where it already is.
	app *models.Application

	containerID string // container items: the active release's container
	service     string // service items: the swarm service name

	// gatewayName is the gateway container to inspect on the node; imported marks one Miabi did not deploy and
	// must never recreate.
	gatewayName string
	imported    bool

	volumeName string // volume items: the Docker volume
	// engineCreatedAt is what Miabi recorded for the volume. Empty means it was created before Miabi
	// recorded one, and the next sweep adopts whatever the engine reports.
	engineCreatedAt string
	adopt           func(at string) error
	// restore names the backup this item's data comes back from. Resolved only when a finding is first
	// raised, so a quiet sweep costs no backup queries.
	restore func() *Restore

	// subjects are the timelines an event about this item goes to. A volume no app mounts has none, so
	// it is reported without an event.
	subjects []subject
	// needs lists the volume items whose data this item depends on, so a missing volume blocks it.
	needs []string
}

func appKey(id uint) string      { return fmt.Sprintf("app:%d", id) }
func volumeKey(id uint) string   { return fmt.Sprintf("volume:%d", id) }
func dbVolumeKey(id uint) string { return fmt.Sprintf("dbvolume:%d", id) }
func gatewayKey(id uint) string  { return fmt.Sprintf("gateway:%d", id) }
func nodeRef(id uint) string     { return fmt.Sprintf("node:%d", id) }

// plan builds what the sweep expects to exist. Apps with a deploy under way are left out: the deploy
// path is removing and starting their containers, and what it leaves behind is judged by a later sweep.
func (s *Service) plan(ctx context.Context) ([]item, error) {
	apps, err := s.apps.ListReconcilable()
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	busy, err := s.deploys.InProgressAppIDs()
	if err != nil {
		return nil, fmt.Errorf("list deployments in progress: %w", err)
	}
	deploying := make(map[uint]bool, len(busy))
	for _, id := range busy {
		deploying[id] = true
	}
	volumes, err := s.volumes.ListAll()
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	instances, err := s.databases.ListAllInstances()
	if err != nil {
		return nil, fmt.Errorf("list database instances: %w", err)
	}

	// A volume is addressed by row id or by Docker name depending on how the app was built, so both
	// resolve to the same item.
	byID := make(map[uint]string, len(volumes))
	byName := make(map[string]string, len(volumes))
	for i := range volumes {
		v := &volumes[i]
		byID[v.ID] = volumeKey(v.ID)
		byName[v.DockerName] = volumeKey(v.ID)
	}

	items := make([]item, 0, len(apps)+len(volumes)+len(instances))
	mountedBy := map[string][]subject{}
	for i := range apps {
		a := &apps[i]
		if deploying[a.ID] {
			continue
		}
		it := item{
			name: a.Name, ref: appKey(a.ID), owner: drift.OwnerApp, id: a.ID, app: a,
			key: appKey(a.ID), workspaceID: a.WorkspaceID, nodeID: a.ServerID, clusterID: a.ClusterID,
			subjects: []subject{{workspaceID: a.WorkspaceID, appID: a.ID}},
		}
		for _, m := range a.Mounts {
			key := byID[m.VolumeID]
			if key == "" {
				key = byName[m.DockerName]
			}
			if key == "" {
				continue // a config projection, a host bind, or a volume Miabi no longer has a row for
			}
			it.needs = append(it.needs, key)
			mountedBy[key] = append(mountedBy[key], subject{workspaceID: a.WorkspaceID, appID: a.ID})
		}
		if a.RuntimeKind == models.RuntimeService {
			it.kind, it.service = kindService, node.AppAlias(a)
		} else {
			it.kind = kindContainer
			rel, rerr := s.releases.FindActive(a.ID)
			if rerr != nil || rel.ContainerID == "" {
				continue // no container recorded: nothing to judge it against
			}
			it.containerID = rel.ContainerID
		}
		items = append(items, it)
	}

	for i := range volumes {
		v := &volumes[i]
		// A host-driver volume is a bind to an operator-managed path, not a Docker volume.
		if v.Driver == models.VolumeDriverHost || v.DockerName == "" {
			continue
		}
		id := v.ID
		items = append(items, item{
			key: volumeKey(v.ID), kind: kindVolume, name: v.Name, ref: volumeKey(v.ID),
			owner: drift.OwnerVolume, id: v.ID, workspaceID: v.WorkspaceID, nodeID: v.ServerID, clusterID: v.ClusterID,
			volumeName: v.DockerName, engineCreatedAt: v.EngineCreatedAt,
			adopt:    func(at string) error { return s.volumes.SetEngineCreatedAt(id, at) },
			restore:  func() *Restore { return s.latestVolumeBackup(id) },
			subjects: mountedBy[volumeKey(v.ID)],
		})
	}

	for i := range instances {
		inst := &instances[i]
		if inst.VolumeName == "" {
			continue
		}
		id := inst.ID
		items = append(items, item{
			key: dbVolumeKey(inst.ID), kind: kindVolume, name: inst.Name, ref: dbVolumeKey(inst.ID),
			owner: drift.OwnerDatabase, id: inst.ID, workspaceID: inst.WorkspaceID, nodeID: inst.ServerID, clusterID: inst.ClusterID,
			volumeName: inst.VolumeName, engineCreatedAt: inst.VolumeEngineCreatedAt,
			adopt:   func(at string) error { return s.databases.SetVolumeEngineCreatedAt(id, at) },
			restore: func() *Restore { return s.latestRecoveryPoint(id) },
			subjects: []subject{{
				workspaceID: inst.WorkspaceID, databaseID: inst.ID, databaseName: inst.Name,
			}},
		})
	}

	items = append(items, s.gatewayItems(ctx)...)
	return items, nil
}

// gatewayItems expects a gateway on every node Miabi recorded one for. That includes the manager's own, which
// the platform stack installs and Miabi adopts as an imported gateway: an ingress nobody is watching is an
// outage waiting to be reported by a user, and on a swarm cluster's ingress node it fronts every app there.
func (s *Service) gatewayItems(ctx context.Context) []item {
	if s.servers == nil {
		return nil
	}
	servers, err := s.servers.List(ctx)
	if err != nil {
		return nil
	}
	out := make([]item, 0, len(servers))
	for i := range servers {
		srv := &servers[i]
		if srv.GatewayDeployedAt == nil || srv.Connectivity != models.ConnectivityEdgeGateway {
			continue
		}
		out = append(out, item{
			key: gatewayKey(srv.ID), kind: kindGateway, name: srv.Name, ref: nodeRef(srv.ID),
			owner: drift.OwnerNode, id: srv.ID, nodeID: srv.ID, clusterID: srv.ClusterID,
			gatewayName: edgegateway.ContainerNameFor(srv), imported: srv.GatewayImported,
		})
	}
	return out
}

// latestVolumeBackup names the newest completed archive of a volume. A failed or half-finished one is not
// something to point an operator at.
func (s *Service) latestVolumeBackup(volumeID uint) *Restore {
	if s.volumeBackups == nil {
		return nil
	}
	backups, err := s.volumeBackups.ListByVolume(volumeID)
	if err != nil {
		return nil
	}
	for i := range backups {
		b := &backups[i]
		if b.Status != models.BackupCompleted {
			continue
		}
		return &Restore{
			From: "volume-backup", BackupID: b.ID, Ref: b.Filename,
			CreatedAt: b.CreatedAt, SizeBytes: b.SizeBytes,
		}
	}
	return &Restore{From: "volume-backup"}
}

// latestRecoveryPoint names the newest completed recovery point of a database instance, which is what its
// data comes back from.
func (s *Service) latestRecoveryPoint(instanceID uint) *Restore {
	if s.dbBackups == nil {
		return nil
	}
	sets, err := s.dbBackups.ListByInstance(instanceID)
	if err != nil {
		return nil
	}
	for i := range sets {
		set := &sets[i]
		if set.Status != models.BackupCompleted {
			continue
		}
		return &Restore{From: "recovery-point", BackupID: set.ID, Ref: set.Ref, CreatedAt: set.CreatedAt}
	}
	return &Restore{From: "recovery-point"}
}
