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

// Package recovery converges a restored control-plane DB, whose rows describe a machine that no longer exists, onto this host.
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

	TenantData *TenantRestoreSummary `json:"tenant_data,omitempty"`

	// Stated up front rather than discovered at the first failed deploy.
	Unrecoverable []string `json:"unrecoverable"`
	// Manual names what a human must do: DNS, certificates, re-enrolment.
	Manual []string `json:"manual"`
	// Failures are per-resource errors that did not stop the reconcile.
	Failures []string `json:"failures"`
}

// Mirrors platformbackup's report rather than embedding it, avoiding a dependency on its types.
type TenantRestoreSummary struct {
	Ref               string   `json:"ref"`
	DatabasesRestored int      `json:"databases_restored"`
	VolumesRestored   int      `json:"volumes_restored"`
	Skipped           []string `json:"skipped,omitempty"`
	Failures          []string `json:"failures,omitempty"`
}

// Optional: without one, reconcile restores only the control plane and says so.
type TenantRestorer func(ctx context.Context) (*TenantRestoreSummary, error)

// Ensure, not start: the recorded container id names one on a machine that is gone, so recreate from spec.
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

// Decides whether images survived the lost host.
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

// Nil hooks make their step a reported gap rather than a crash.
func New(settings SettingsStore, servers ServerStore, apps *repositories.ApplicationRepository, networks *repositories.NetworkRepository) *Service {
	return &Service{settings: settings, servers: servers, apps: apps, networks: networks}
}

func (s *Service) SetNetworkEnsurer(fn NetworkEnsurer) { s.ensureNetwork = fn }
func (s *Service) SetRedeployer(fn AppRedeployer)      { s.redeploy = fn }
func (s *Service) SetRouteSyncer(fn RouteSyncer)       { s.syncRoutes = fn }
func (s *Service) SetRegistryInfo(fn RegistryInfo)     { s.registry = fn }

// Set together because either alone is useless: an empty database, or a dump with nowhere to go.
func (s *Service) SetDatabaseRecovery(list DatabaseLister, ensure DatabaseEnsurer, restore TenantRestorer) {
	s.instances, s.ensureDatabase, s.restoreTenants = list, ensure, restore
}

// DatabaseLister returns every managed database instance across workspaces.
type DatabaseLister func() ([]models.DatabaseInstance, error)

// Pending is read on every boot; the marker lives in the restored DB so recovery is known before anything else.
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

// Reconcile is tolerant: failures are collected and reported so one bad app never blocks the rest.
func (s *Service) Reconcile(ctx context.Context) (*Report, error) {
	// Non-nil so it marshals to [] not null; clients reading failures.length on null crash.
	rep := &Report{
		StartedAt:     time.Now().UTC(),
		Unrecoverable: []string{},
		Manual:        []string{},
		Failures:      []string{},
	}

	// Infrastructure before what sits on it, data before workloads: an app against an empty DB comes back healthy and empty.
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

// Restored Server rows carry swarm ids of gone machines; clearing them stops scheduling onto phantom nodes.
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
			// Remote agent enrolment was issued by the host that is gone, so it cannot be recovered here.
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

// The ledger is authoritative, so addresses come back identical.
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

// Apps whose image lived only in a filesystem-backed registry are reported unrecoverable, not retried.
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

// Containers must be up before any dump is loaded.
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

// True for local filesystem storage; on S3 the images outlived the host.
func (s *Service) registryImagesLost() bool {
	if s.registry == nil {
		return false
	}
	storage, enabled := s.registry()
	return enabled && storage == models.RegistryStorageFilesystem
}

// Rebuildable apps build from Git or pull images from elsewhere.
func rebuildable(app *models.Application) bool {
	if app.GitRepositoryID != nil && *app.GitRepositoryID != 0 {
		return true
	}
	return app.SourceType != models.AppSourceImage
}

// The gateway starts with a default config, so every route must be written back for traffic to flow.
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

// Complete requires an explicit action: the operator confirms they read the report and moved DNS.
func (s *Service) Complete() error {
	if err := s.settings.Delete(models.RestorePendingKey); err != nil {
		return err
	}
	logger.Info("platform recovery completed; schedules resume")
	return nil
}
