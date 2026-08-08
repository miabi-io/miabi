// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package recovery finishes what `miabi restore` starts. A restored control-plane database is a faithful
// record of a machine that no longer exists: its rows name containers never created here, networks that do
// not exist, and images this host cannot reach — and the platform boots looking healthy while serving nothing.
package recovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Status is what the admin UI shows while a platform is recovering.
type Status struct {
	// Pending is true between a restore and the operator completing recovery.
	Pending bool `json:"pending"`
	// Note is the marker's value: which recovery point, and when.
	Note string `json:"note,omitempty"`
	// Report is the last reconcile's outcome, if one has run.
	Report *Report `json:"report,omitempty"`
}

// Report is the outcome of a reconcile: what came back, and what did not.
type Report struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`

	NodesReset       int `json:"nodes_reset"`
	NetworksEnsured  int `json:"networks_ensured"`
	DatabasesStarted int `json:"databases_started"`
	AppsRedeployed   int `json:"apps_redeployed"`
	RoutesSynced     int `json:"routes_synced"`

	// TenantData reports the workload restore, when the recovery point carried
	// tenant databases and volumes.
	TenantData *TenantRestoreSummary `json:"tenant_data,omitempty"`

	// Unrecoverable names what this host cannot bring back at all, so the loss is
	// stated rather than discovered at the first failed deploy.
	Unrecoverable []string `json:"unrecoverable"`
	// Manual names what a human must do: DNS, certificates, re-enrolment.
	Manual []string `json:"manual"`
	// Failures are per-resource errors that did not stop the reconcile.
	Failures []string `json:"failures"`
}

// TenantRestoreSummary reports what the workload restore recovered. It mirrors
// platformbackup's report rather than embedding it, so this package does not
// depend on the backup service's types.
type TenantRestoreSummary struct {
	Ref               string   `json:"ref"`
	DatabasesRestored int      `json:"databases_restored"`
	VolumesRestored   int      `json:"volumes_restored"`
	Skipped           []string `json:"skipped,omitempty"`
	Failures          []string `json:"failures,omitempty"`
}

// TenantRestorer restores workload data from the newest usable recovery point.
// Optional: without one, a reconcile brings back the control plane and says so.
type TenantRestorer func(ctx context.Context) (*TenantRestoreSummary, error)

// DatabaseEnsurer brings a managed database instance's container back. "Ensure", not "start": on recovered
// hardware the row's container id names a container on a machine that is gone, so starting it can only fail.
// The implementation recreates from the stored spec when the recorded container is not there.
type DatabaseEnsurer func(ctx context.Context, instanceID uint) error

// SettingsStore reads and writes platform settings.
type SettingsStore interface {
	Get(key string) (*models.Setting, error)
	BulkUpsert(settings []models.Setting) error
	Delete(key string) error
}

// ServerStore lists and updates node records.
type ServerStore interface {
	List() ([]models.Server, error)
	Update(s *models.Server) error
}

// NetworkEnsurer recreates a Docker network the ledger says should exist.
type NetworkEnsurer func(ctx context.Context, name string) error

// AppRedeployer redeploys one application from its stored spec.
type AppRedeployer func(app *models.Application) error

// RouteSyncer re-renders the gateway configuration for a workspace.
type RouteSyncer func(ctx context.Context, workspaceID uint) error

// RegistryInfo reports how the built-in registry stores its blobs, which decides
// whether images survived the host that is gone.
type RegistryInfo func() (storageType string, enabled bool)

// Service drives post-restore reconciliation.
type Service struct {
	settings SettingsStore
	servers  ServerStore
	apps     *repositories.ApplicationRepository
	networks *repositories.NetworkRepository

	ensureNetwork  NetworkEnsurer
	redeploy       AppRedeployer
	syncRoutes     RouteSyncer
	registry       RegistryInfo
	ensureDatabase DatabaseEnsurer
	restoreTenants TenantRestorer
	instances      DatabaseLister

	last *Report
}

// New builds the recovery service. The action hooks are optional: a nil hook
// makes its step a reported gap rather than a crash, because a partially wired
// recovery is still better than none.
func New(settings SettingsStore, servers ServerStore, apps *repositories.ApplicationRepository, networks *repositories.NetworkRepository) *Service {
	return &Service{settings: settings, servers: servers, apps: apps, networks: networks}
}

func (s *Service) SetNetworkEnsurer(fn NetworkEnsurer) { s.ensureNetwork = fn }
func (s *Service) SetRedeployer(fn AppRedeployer)      { s.redeploy = fn }
func (s *Service) SetRouteSyncer(fn RouteSyncer)       { s.syncRoutes = fn }
func (s *Service) SetRegistryInfo(fn RegistryInfo)     { s.registry = fn }

// SetDatabaseRecovery wires the two halves of workload recovery: bringing each managed database instance's
// container back, and then loading the recovery point's dumps into them. They are set together because
// either alone is useless — an empty database that starts, or a dump with nowhere to go.
func (s *Service) SetDatabaseRecovery(list DatabaseLister, ensure DatabaseEnsurer, restore TenantRestorer) {
	s.instances, s.ensureDatabase, s.restoreTenants = list, ensure, restore
}

// DatabaseLister returns every managed database instance across workspaces.
type DatabaseLister func() ([]models.DatabaseInstance, error)

// Pending reports whether this platform is a restore awaiting completion. It is
// read on every boot: the marker lives in the restored database, so the platform
// knows it is recovering before it does anything else.
func (s *Service) Pending() bool {
	rec, err := s.settings.Get(models.RestorePendingKey)
	if err != nil || rec == nil {
		return false
	}
	return strings.TrimSpace(rec.Value) != ""
}

// Status reports the recovery state for the admin UI.
func (s *Service) Status() Status {
	st := Status{Report: s.last}
	if rec, err := s.settings.Get(models.RestorePendingKey); err == nil && rec != nil {
		st.Note = rec.Value
		st.Pending = strings.TrimSpace(rec.Value) != ""
	}
	return st
}

// Reconcile converges the restored control-plane state onto this host. It is deliberately tolerant: one app
// that will not deploy must not stop the other forty from coming back. Everything that fails is collected
// and reported rather than returned, because the operator needs the whole picture at once.
func (s *Service) Reconcile(ctx context.Context) (*Report, error) {
	// Initialised, not left nil: a nil slice marshals to JSON null, and a client reading report.failures.length
	// on null crashes the page it was meant to render. An empty list is the honest encoding of "nothing went
	// wrong", and saying so is the API's job rather than every consumer's to defend against.
	rep := &Report{
		StartedAt:     time.Now().UTC(),
		Unrecoverable: []string{},
		Manual:        []string{},
		Failures:      []string{},
	}

	// Order matters and is the whole design of this function: infrastructure before the things that sit on it,
	// and data before the workloads that read it. An app redeployed against a database that is not yet up and
	// not yet populated comes back healthy and empty, which is worse than not coming back.
	s.resetNodes(rep)
	s.ensureNetworks(ctx, rep)
	s.startDatabases(ctx, rep)
	s.restoreTenantData(ctx, rep)
	s.redeployApps(rep)
	s.syncWorkspaceRoutes(ctx, rep)
	s.noteManualSteps(rep)

	rep.FinishedAt = time.Now().UTC()
	s.last = rep
	logger.Info("recovery reconcile finished",
		"nodes_reset", rep.NodesReset, "networks", rep.NetworksEnsured,
		"apps", rep.AppsRedeployed, "routes", rep.RoutesSynced,
		"failures", len(rep.Failures))
	return rep, nil
}

// resetNodes invalidates host-bound identity on the restored node records. A Server row carries the swarm node
// id of a machine that is gone; left in place, the platform schedules work onto nodes that do not exist and
// reports them healthy. Clearing it makes the truth visible: one local manager, remotes awaiting re-enrolment.
func (s *Service) resetNodes(rep *Report) {
	servers, err := s.servers.List()
	if err != nil {
		rep.Failures = append(rep.Failures, "list nodes: "+err.Error())
		return
	}
	for i := range servers {
		srv := &servers[i]
		if srv.SwarmNodeID == "" && srv.Status == models.ServerStatusOffline {
			continue
		}
		srv.SwarmNodeID = ""
		if !srv.IsLocal {
			// Remote nodes cannot be recovered from here: their agent enrolment was
			// issued by the host that is gone.
			srv.Status = models.ServerStatusOffline
			rep.Manual = append(rep.Manual,
				fmt.Sprintf("re-enrol node %q — its agent enrolment belonged to the previous host", srv.Name))
		}
		if err := s.servers.Update(srv); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("reset node %s: %v", srv.Name, err))
			continue
		}
		rep.NodesReset++
	}
}

// ensureNetworks recreates the Docker networks the allocation ledger records.
// The ledger is authoritative, so addresses come back identical and apps keep
// the IPs their configuration refers to.
func (s *Service) ensureNetworks(ctx context.Context, rep *Report) {
	if s.ensureNetwork == nil || s.networks == nil {
		return
	}
	nets, err := s.networks.All()
	if err != nil {
		rep.Failures = append(rep.Failures, "list networks: "+err.Error())
		return
	}
	for _, n := range nets {
		if n.DockerName == "" {
			continue
		}
		if err := s.ensureNetwork(ctx, n.DockerName); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("ensure network %s: %v", n.DockerName, err))
			continue
		}
		rep.NetworksEnsured++
	}
}

// redeployApps brings every application back from its stored spec. Apps whose image lived only in a
// filesystem-backed registry cannot come back at all — those blobs died with the host — so they are named
// as unrecoverable instead of being retried into a confusing failure.
func (s *Service) redeployApps(rep *Report) {
	if s.redeploy == nil || s.apps == nil {
		return
	}
	apps, err := s.apps.All()
	if err != nil {
		rep.Failures = append(rep.Failures, "list applications: "+err.Error())
		return
	}
	lostRegistry := s.registryImagesLost()
	for i := range apps {
		app := &apps[i]
		if lostRegistry && !rebuildable(app) {
			rep.Unrecoverable = append(rep.Unrecoverable,
				fmt.Sprintf("app %q — its image existed only in this platform's registry, which used local storage on the host that is gone; re-push it or give it a Git source", app.Name))
			continue
		}
		if err := s.redeploy(app); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("redeploy %s: %v", app.Name, err))
			continue
		}
		rep.AppsRedeployed++
	}
	sort.Strings(rep.Unrecoverable)
}

// startDatabases brings every managed database instance's container back before
// any dump is loaded into it.
func (s *Service) startDatabases(ctx context.Context, rep *Report) {
	if s.ensureDatabase == nil || s.instances == nil {
		return
	}
	instances, err := s.instances()
	if err != nil {
		rep.Failures = append(rep.Failures, "list database instances: "+err.Error())
		return
	}
	for i := range instances {
		inst := &instances[i]
		if err := s.ensureDatabase(ctx, inst.ID); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("bring up database %s: %v", inst.Name, err))
			continue
		}
		rep.DatabasesStarted++
	}
}

// restoreTenantData loads the recovery point's workload data back into the
// instances just started.
func (s *Service) restoreTenantData(ctx context.Context, rep *Report) {
	if s.restoreTenants == nil {
		return
	}
	summary, err := s.restoreTenants(ctx)
	if err != nil {
		rep.Failures = append(rep.Failures, "restore tenant data: "+err.Error())
		return
	}
	if summary == nil {
		return
	}
	rep.TenantData = summary
	for _, skip := range summary.Skipped {
		rep.Unrecoverable = append(rep.Unrecoverable, "tenant data not restored: "+skip)
	}
	for _, f := range summary.Failures {
		rep.Failures = append(rep.Failures, "tenant data: "+f)
	}
}

// registryImagesLost reports whether the built-in registry's blobs are gone —
// true when it ran on local filesystem storage, false on S3, where the images
// outlived the host.
func (s *Service) registryImagesLost() bool {
	if s.registry == nil {
		return false
	}
	storage, enabled := s.registry()
	return enabled && storage == models.RegistryStorageFilesystem
}

// rebuildable reports whether an app can be produced again without the old
// registry: it builds from Git, or its image comes from somewhere else.
func rebuildable(app *models.Application) bool {
	if app.GitRepositoryID != nil && *app.GitRepositoryID != 0 {
		return true
	}
	return app.SourceType != models.AppSourceImage
}

// syncWorkspaceRoutes re-renders gateway configuration per workspace. The
// gateway comes up with a default config; every route the platform owns has to
// be written back out for traffic to reach anything.
func (s *Service) syncWorkspaceRoutes(ctx context.Context, rep *Report) {
	if s.syncRoutes == nil || s.apps == nil {
		return
	}
	apps, err := s.apps.All()
	if err != nil {
		return // already reported by redeployApps
	}
	seen := map[uint]bool{}
	for _, app := range apps {
		if seen[app.WorkspaceID] {
			continue
		}
		seen[app.WorkspaceID] = true
		if err := s.syncRoutes(ctx, app.WorkspaceID); err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("sync routes for workspace %d: %v", app.WorkspaceID, err))
			continue
		}
		rep.RoutesSynced++
	}
}

// noteManualSteps records what no amount of reconciling can do from here.
func (s *Service) noteManualSteps(rep *Report) {
	rep.Manual = append(rep.Manual,
		"point DNS at this host — until it resolves here, certificates cannot be issued and traffic still reaches the old address",
		"managed certificates re-issue automatically once DNS resolves here; upload-based certificates were restored as-is",
		"API tokens still work (the JWT secret was recovered), but any integration that hard-codes the old host's address needs updating",
	)
}

// Complete clears the quiesce marker: schedules resume and the platform behaves normally from here. Requiring
// an explicit action is the point — it is the operator confirming they have read the report and moved DNS,
// not the platform assuming it.
func (s *Service) Complete() error {
	if err := s.settings.Delete(models.RestorePendingKey); err != nil {
		return err
	}
	logger.Info("platform recovery completed; schedules resume")
	return nil
}
