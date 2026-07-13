// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package monitoring exposes container metrics and workspace health overviews.
package monitoring

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ErrNoActiveContainer is returned when an app has no running release.
var ErrNoActiveContainer = errors.New("application has no active container")

// ErrTaskOnUnmanagedNode is the user-facing form of nodes.ErrTaskUnreachable: the
// app IS running, but on a swarm node with no Miabi agent, so there is no engine to
// read its container's resource usage through. Docker offers no manager-side
// equivalent of stats, so this is a hard limit, not a bug. Logs are unaffected —
// the manager aggregates those (see StreamAppLogs).
var ErrTaskOnUnmanagedNode = errors.New(
	"this app's task runs on a swarm node with no Miabi agent, so its resource usage cannot be read. " +
		"Add the node to Miabi (install the agent) to see metrics, stats and a shell")

// errStopStream stops a stats stream after enough samples.
var errStopStream = errors.New("stop")

// NodeDocker resolves the Docker client for a node id (0 = local). Lets metrics
// and log/stat streams reach an app's container on whichever node it runs.
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	// ForServiceTask finds the engine holding a swarm service's task container.
	// A service has no fixed node, and only the node running the task can see it.
	ForServiceTask(ctx context.Context, serviceName string) (docker.Client, string, error)
}

// ServerInfo resolves a node record by id, so the overview can label each app
// with the node it runs on.
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

type Service struct {
	apps       *repositories.ApplicationRepository
	releases   *repositories.ReleaseRepository
	dbs        *repositories.DatabaseRepository
	stacks     *repositories.StackRepository
	events     *repositories.AppEventRepository
	metrics    *repositories.MetricRepository
	clients    NodeDocker
	serverInfo ServerInfo
}

func NewService(apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, dbs *repositories.DatabaseRepository, stacks *repositories.StackRepository, events *repositories.AppEventRepository, metrics *repositories.MetricRepository, clients NodeDocker) *Service {
	return &Service{apps: apps, releases: releases, dbs: dbs, stacks: stacks, events: events, metrics: metrics, clients: clients}
}

// SetServerInfo wires the resolver used to label apps with their node's name.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

func (s *Service) activeContainer(appID uint) (string, error) {
	rel, err := s.releases.FindActive(appID)
	if err != nil || rel.ContainerID == "" {
		return "", ErrNoActiveContainer
	}
	return rel.ContainerID, nil
}

// eng resolves the Docker client for a node id, returning an offline client (all
// calls error clearly) when the node is unreachable.
func (s *Service) eng(serverID uint) docker.Client {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// appEngine resolves both the node Docker client and a container id for an app,
// so metrics/log/stat calls reach the right node. For a cluster (service) app it
// resolves a running task container of its Swarm service on the manager; for a
// container app it's the active release's container on the app's node.
//
// The app is loaded workspace-scoped so a caller can never read another
// workspace's container by guessing its app id — the {appID} path segment is
// otherwise unverified against the {workspace} the caller is a member of.
func (s *Service) appEngine(ctx context.Context, workspaceID, appID uint) (docker.Client, string, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, "", ErrNoActiveContainer
	}
	if app.RuntimeKind == models.RuntimeService {
		return s.serviceEngine(ctx, app)
	}
	cid, err := s.activeContainer(appID)
	if err != nil {
		return nil, "", err
	}
	return s.eng(app.ServerID), cid, nil
}

// serviceEngine resolves the Docker client and container id holding a cluster
// (service) app's task, translating the registry's outcome into this package's
// errors. Only metrics/stats need it — logs go through the manager instead, which
// works even when the task is unreachable (see StreamAppLogs).
func (s *Service) serviceEngine(ctx context.Context, app *models.Application) (docker.Client, string, error) {
	dc, cid, err := s.clients.ForServiceTask(ctx, node.AppAlias(app))
	switch {
	case err == nil:
		return dc, cid, nil
	case errors.Is(err, nodes.ErrTaskUnreachable):
		return nil, "", ErrTaskOnUnmanagedNode
	default:
		return nil, "", ErrNoActiveContainer
	}
}

// AppMetrics returns a single resource-usage sample for an app's active container.
func (s *Service) AppMetrics(ctx context.Context, workspaceID, appID uint) (docker.StatsSample, error) {
	dc, cid, err := s.appEngine(ctx, workspaceID, appID)
	if err != nil {
		return docker.StatsSample{}, err
	}
	return dc.StatsOnce(ctx, cid)
}

// StreamAppMetrics streams live samples for an app's active container.
func (s *Service) StreamAppMetrics(ctx context.Context, workspaceID, appID uint, sink func(docker.StatsSample) error) error {
	dc, cid, err := s.appEngine(ctx, workspaceID, appID)
	if err != nil {
		return err
	}
	return dc.StreamStats(ctx, cid, sink)
}

// StreamAppLogs streams the active container's runtime logs (stdout/stderr),
// starting with the last `tail` lines. When follow is true it then follows live
// output until the context is cancelled; when false it returns after the tail.
//
// A cluster (service) app is read from the MANAGER via `docker service logs`, not
// from a container. That is deliberate and is the only thing that works in general:
// Swarm may have placed the task on a node Miabi has no Docker client for (an
// unmanaged swarm member with no agent), and the manager can still pull its logs
// over the swarm control plane. It also aggregates every replica, which reading a
// single container never could.
func (s *Service) StreamAppLogs(ctx context.Context, workspaceID, appID uint, follow bool, tail string, sink func(docker.LogLine) error) error {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return ErrNoActiveContainer
	}
	if tail == "" {
		tail = "200"
	}
	if app.RuntimeKind == models.RuntimeService {
		mgr, merr := s.clients.For(0)
		if merr != nil {
			return ErrNoActiveContainer
		}
		lerr := mgr.StreamServiceLogs(ctx, node.AppAlias(app), follow, tail, sink)
		if errors.Is(lerr, docker.ErrNotFound) {
			return ErrNoActiveContainer // the service does not exist (never deployed / removed)
		}
		return lerr
	}
	dc, cid, err := s.appEngine(ctx, workspaceID, appID)
	if err != nil {
		return err
	}
	return dc.StreamLogs(ctx, cid, follow, tail, sink)
}

// --- Workspace live usage (aggregated container stats) ---

// WorkspaceSample is a live, aggregated resource snapshot across a workspace's
// running application and database containers. It reflects what the containers
// actually consume right now — unlike the /usage endpoint, which reports declared
// reservations and quota counts.
type WorkspaceSample struct {
	At               time.Time `json:"at"`
	Containers       int       `json:"containers"`         // running containers sampled
	CPUPercent       float64   `json:"cpu_percent"`        // summed; 100% == one core fully used
	CPUCores         float64   `json:"cpu_cores"`          // cpu_percent / 100 (convenience)
	MemoryBytes      uint64    `json:"memory_bytes"`       // actual resident memory in use
	MemoryLimitBytes uint64    `json:"memory_limit_bytes"` // summed per-container limits (0 = uncapped)
	NetRxBytes       uint64    `json:"net_rx_bytes"`
	NetTxBytes       uint64    `json:"net_tx_bytes"`
}

// sampleTarget is one running container to sample, together with the engine that
// can actually see it. It holds the resolved client rather than a node id because a
// cluster (service) app has no fixed node — Swarm places its task wherever it
// likes, and only that node's engine can read the container.
type sampleTarget struct {
	dc          docker.Client
	containerID string
}

// minWorkspaceUsageInterval floors the live-stream cadence so a client cannot ask
// the platform to hammer every node's Docker daemon.
const minWorkspaceUsageInterval = time.Second

// workspaceTargets resolves every running app and database container in the
// workspace — the set the live aggregate samples. Best-effort per resource: an
// app with no active container (or an unreachable service task) is skipped rather
// than failing the whole reading.
func (s *Service) workspaceTargets(ctx context.Context, workspaceID uint) []sampleTarget {
	var targets []sampleTarget
	if apps, err := s.apps.ListByWorkspace(workspaceID); err == nil {
		for i := range apps {
			a := apps[i]
			if a.Status != models.AppStatusRunning {
				continue
			}
			if a.RuntimeKind == models.RuntimeService {
				// Sample the task wherever Swarm placed it. A task on an unmanaged swarm
				// node is absent from the aggregate: there is no engine to read it through,
				// and Docker has no manager-side stats to fall back on.
				if dc, cid, err := s.clients.ForServiceTask(ctx, node.AppAlias(&a)); err == nil {
					targets = append(targets, sampleTarget{dc: dc, containerID: cid})
				}
				continue
			}
			if cid, err := s.activeContainer(a.ID); err == nil {
				targets = append(targets, sampleTarget{dc: s.eng(a.ServerID), containerID: cid})
			}
		}
	}
	if dbs, err := s.dbs.ListByWorkspace(workspaceID); err == nil {
		for i := range dbs {
			d := dbs[i]
			if d.Status == models.DBStatusRunning && d.ContainerID != "" {
				targets = append(targets, sampleTarget{dc: s.eng(d.ServerID), containerID: d.ContainerID})
			}
		}
	}
	return targets
}

// WorkspaceLiveUsage samples every running container in the workspace once
// (concurrently) and returns the aggregate. Containers that error mid-sample —
// stopped just now, or on an unreachable node — are skipped, so one bad container
// never fails the whole reading.
func (s *Service) WorkspaceLiveUsage(ctx context.Context, workspaceID uint) (WorkspaceSample, error) {
	targets := s.workspaceTargets(ctx, workspaceID)
	agg := WorkspaceSample{At: time.Now()}
	if len(targets) == 0 {
		return agg, nil
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, t := range targets {
		wg.Add(1)
		go func(t sampleTarget) {
			defer wg.Done()
			sample, err := s.sample(ctx, t.dc, t.containerID)
			if err != nil {
				return
			}
			mu.Lock()
			agg.Containers++
			agg.CPUPercent += sample.CPUPercent
			agg.MemoryBytes += sample.MemoryUsage
			agg.MemoryLimitBytes += sample.MemoryLimit
			agg.NetRxBytes += sample.NetworkRxBytes
			agg.NetTxBytes += sample.NetworkTxBytes
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	agg.CPUCores = agg.CPUPercent / 100
	return agg, nil
}

// StreamWorkspaceUsage pushes an aggregate sample immediately, then every interval
// until ctx is cancelled (the SSE client disconnects). interval is floored so a
// client cannot drive excessive sampling.
func (s *Service) StreamWorkspaceUsage(ctx context.Context, workspaceID uint, interval time.Duration, sink func(WorkspaceSample) error) error {
	if interval < minWorkspaceUsageInterval {
		interval = minWorkspaceUsageInterval
	}
	emit := func() error {
		sample, err := s.WorkspaceLiveUsage(ctx, workspaceID)
		if err != nil {
			return err
		}
		return sink(sample)
	}
	if err := emit(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := emit(); err != nil {
				return err
			}
		}
	}
}

// WorkspaceHistoryPoint is one time-bucketed aggregate of the workspace's stored
// per-app metric samples (from the scraper), summed across its applications.
type WorkspaceHistoryPoint struct {
	At          time.Time `json:"at"`
	CPUPercent  float64   `json:"cpu_percent"`
	CPUCores    float64   `json:"cpu_cores"`
	MemoryBytes uint64    `json:"memory_bytes"`
}

// minHistoryBucket floors the bucket so a client can't request pathologically
// fine buckets over a long window.
const minHistoryBucket = 15 * time.Second

// WorkspaceUsageHistory builds a workspace-level resource time series from the
// scraper's stored per-app samples: each sample is bucketed to `bucket` and summed
// across the workspace's apps, so the series reflects total workspace consumption
// over time. Powers the dashboard sparkline. Within a bucket an app is counted
// once (its latest sample), so a bucket wider than the scrape interval never
// double-counts.
func (s *Service) WorkspaceUsageHistory(workspaceID uint, since time.Time, bucket time.Duration) ([]WorkspaceHistoryPoint, error) {
	if bucket < minHistoryBucket {
		bucket = minHistoryBucket
	}
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(apps))
	for i := range apps {
		ids = append(ids, apps[i].ID)
	}
	samples, err := s.metrics.ListByApps(ids, since)
	if err != nil {
		return nil, err
	}
	// Dedup to the latest sample per (bucket, app) before summing.
	type slot struct {
		bucket int64
		app    uint
	}
	latest := make(map[slot]models.MetricSample, len(samples))
	for _, sm := range samples {
		b := sm.RecordedAt.Truncate(bucket).Unix()
		k := slot{bucket: b, app: sm.ApplicationID}
		if cur, ok := latest[k]; !ok || sm.RecordedAt.After(cur.RecordedAt) {
			latest[k] = sm
		}
	}
	// Sum each bucket across apps.
	sums := map[int64]*WorkspaceHistoryPoint{}
	for k, sm := range latest {
		p := sums[k.bucket]
		if p == nil {
			p = &WorkspaceHistoryPoint{At: time.Unix(k.bucket, 0).UTC()}
			sums[k.bucket] = p
		}
		p.CPUPercent += sm.CPUPercent
		p.MemoryBytes += sm.MemoryBytes
	}
	buckets := make([]int64, 0, len(sums))
	for b := range sums {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
	points := make([]WorkspaceHistoryPoint, 0, len(buckets))
	for _, b := range buckets {
		p := sums[b]
		p.CPUCores = p.CPUPercent / 100
		points = append(points, *p)
	}
	return points, nil
}

// AppHealth describes one application's health.
type AppHealth struct {
	ID          uint             `json:"id"`
	Name        string           `json:"name"`         // unique slug handle
	DisplayName string           `json:"display_name"` // free-text label
	Status      models.AppStatus `json:"status"`
	Health      string           `json:"health"` // healthy | unhealthy | unknown
	ServerID    uint             `json:"server_id"`
	ServerName  string           `json:"server_name,omitempty"` // node the app runs on
	CreatedAt   time.Time        `json:"created_at"`
}

// RecentEvent is a workspace activity-feed entry: an application event enriched
// with the originating application's handle/label so the dashboard can show which
// app it belongs to without an extra lookup.
type RecentEvent struct {
	models.AppEvent
	AppName        string `json:"app_name"`         // unique slug handle
	AppDisplayName string `json:"app_display_name"` // free-text label
}

// Overview is a workspace-level summary.
type Overview struct {
	Apps         []AppHealth   `json:"apps"`
	TotalApps    int           `json:"total_apps"`
	Running      int           `json:"running"`
	Failed       int           `json:"failed"`
	Databases    int           `json:"databases"`
	Stacks       int           `json:"stacks"`
	RecentEvents []RecentEvent `json:"recent_events"`
}

// WorkspaceOverview aggregates app health and resource counts for a workspace.
func (s *Service) WorkspaceOverview(workspaceID uint) (Overview, error) {
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return Overview{}, err
	}
	ov := Overview{TotalApps: len(apps)}
	appNames := make(map[uint]models.Application, len(apps))
	serverNames := map[uint]string{} // cache: serverID -> node name
	for _, a := range apps {
		appNames[a.ID] = a
		h := AppHealth{ID: a.ID, Name: a.Name, DisplayName: a.DisplayName, Status: a.Status, Health: healthOf(a.Status, a.CurrentReleaseID != nil), ServerID: a.ServerID, CreatedAt: a.CreatedAt}
		h.ServerName = s.serverName(serverNames, a.ServerID)
		ov.Apps = append(ov.Apps, h)
		switch a.Status {
		case models.AppStatusRunning:
			ov.Running++
		case models.AppStatusFailed:
			ov.Failed++
		}
	}
	if dbs, err := s.dbs.ListByWorkspace(workspaceID); err == nil {
		ov.Databases = len(dbs)
	}
	if s.stacks != nil {
		if n, err := s.stacks.CountByWorkspace(workspaceID); err == nil {
			ov.Stacks = int(n)
		}
	}
	if s.events != nil {
		if evts, err := s.events.ListByWorkspace(workspaceID, 10); err == nil {
			ov.RecentEvents = make([]RecentEvent, 0, len(evts))
			for _, e := range evts {
				re := RecentEvent{AppEvent: e}
				if a, ok := appNames[e.ApplicationID]; ok {
					re.AppName, re.AppDisplayName = a.Name, a.DisplayName
				}
				ov.RecentEvents = append(ov.RecentEvents, re)
			}
		}
	}
	return ov, nil
}

// serverName resolves a node's display name, memoised per serverID for the
// lifetime of one overview call. Best-effort: returns "" if unresolved.
func (s *Service) serverName(cache map[uint]string, serverID uint) string {
	if name, ok := cache[serverID]; ok {
		return name
	}
	name := ""
	if s.serverInfo != nil {
		if srv, err := s.serverInfo.Get(serverID); err == nil && srv != nil {
			name = srv.Name
		}
	}
	cache[serverID] = name
	return name
}

// --- Metrics history (scraper) ---

// History returns stored metric samples for an app since `since`. The app is
// verified to belong to workspaceID first so a caller cannot read another
// workspace's stored metrics by guessing its app id; an app that isn't in the
// workspace yields an empty history rather than confirming it exists elsewhere.
func (s *Service) History(workspaceID, appID uint, since time.Time, limit int) ([]models.MetricSample, error) {
	if _, err := s.apps.FindInWorkspace(workspaceID, appID); err != nil {
		return []models.MetricSample{}, nil
	}
	return s.metrics.ListByApp(appID, since, limit)
}

// StartScraper runs a background loop that samples every running app's
// container at `interval` and prunes samples older than `retention`. It returns
// when ctx is cancelled.
func (s *Service) StartScraper(ctx context.Context, interval, retention time.Duration) {
	logger.Info("metrics scraper started", "interval", interval.String(), "retention", retention.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scrapeOnce(ctx)
			if _, err := s.metrics.Prune(time.Now().Add(-retention)); err != nil {
				logger.Error("metrics prune failed", "error", err)
			}
		}
	}
}

func (s *Service) scrapeOnce(ctx context.Context) {
	apps, err := s.apps.ListRunning()
	if err != nil {
		logger.Error("metrics scrape: list apps failed", "error", err)
		return
	}
	for _, a := range apps {
		cid, err := s.activeContainer(a.ID)
		if err != nil {
			continue
		}
		sample, err := s.sample(ctx, s.eng(a.ServerID), cid)
		if err != nil {
			continue
		}
		_ = s.metrics.Insert(&models.MetricSample{
			ApplicationID: a.ID, RecordedAt: time.Now(),
			CPUPercent: sample.CPUPercent, MemoryBytes: sample.MemoryUsage,
			MemoryPercent: sample.MemoryPercent, NetRxBytes: sample.NetworkRxBytes, NetTxBytes: sample.NetworkTxBytes,
		})
	}
}

// sample reads up to two stream samples so CPU is computed against a prior
// reading (a single one-shot sample would report ~0% CPU).
func (s *Service) sample(ctx context.Context, dc docker.Client, containerID string) (docker.StatsSample, error) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var last docker.StatsSample
	n := 0
	err := dc.StreamStats(sctx, containerID, func(x docker.StatsSample) error {
		last = x
		n++
		if n >= 2 {
			return errStopStream
		}
		return nil
	})
	if n == 0 {
		return last, err
	}
	return last, nil
}

func healthOf(status models.AppStatus, hasCurrentRelease bool) string {
	switch status {
	case models.AppStatusRunning:
		return "healthy"
	case models.AppStatusFailed:
		return "unhealthy"
	case models.AppStatusDeploying:
		// A deploy in progress keeps the previous release serving (rolling/canary),
		// so the app is still healthy on its current version. Only a first-ever
		// deploy — nothing running yet — is genuinely unknown.
		if hasCurrentRelease {
			return "healthy"
		}
		return "unknown"
	default: // created, stopped
		return "unknown"
	}
}
