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

// ErrTaskOnUnmanagedNode: Docker has no manager-side stats, so usage of a task on an agentless node cannot be read.
var ErrTaskOnUnmanagedNode = errors.New(
	"this app's task runs on a swarm node with no Miabi agent, so its resource usage cannot be read. " +
		"Add the node to Miabi (install the agent) to see metrics, stats and a shell")

var errStopStream = errors.New("stop")

type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	// Only the node running a service task can see its container.
	ForServiceTask(ctx context.Context, clusterID uint, serviceName string) (docker.Client, string, error)
}

type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// SwarmManager resolves the client that drives a cluster's swarm. Implemented by services/cluster.
type SwarmManager interface {
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
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
	swarm      SwarmManager
}

func NewService(apps *repositories.ApplicationRepository, releases *repositories.ReleaseRepository, dbs *repositories.DatabaseRepository, stacks *repositories.StackRepository, events *repositories.AppEventRepository, metrics *repositories.MetricRepository, clients NodeDocker) *Service {
	return &Service{apps: apps, releases: releases, dbs: dbs, stacks: stacks, events: events, metrics: metrics, clients: clients}
}

// SetSwarmManager wires the cluster path for service-app logs; nil reads the local engine.
func (s *Service) SetSwarmManager(m SwarmManager) { s.swarm = m }

func (s *Service) swarmManager(ctx context.Context, app *models.Application) (docker.Client, error) {
	if s.swarm == nil {
		return s.clients.For(0)
	}
	return s.swarm.Manager(ctx, app.ClusterID)
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

// Returns an offline client (all calls error clearly) when the node is unreachable.
func (s *Service) eng(serverID uint) docker.Client {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return docker.Offline(err)
	}
	return dc
}

// The app is loaded workspace-scoped, so a caller cannot read another workspace's container by guessing an id.
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

// Only metrics/stats need this; logs go through the manager, which works even when the task is unreachable.
func (s *Service) serviceEngine(ctx context.Context, app *models.Application) (docker.Client, string, error) {
	dc, cid, err := s.clients.ForServiceTask(ctx, app.ClusterID, node.AppAlias(app))
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

// StreamAppLogs reads cluster apps via the manager's service logs, the only path that works for tasks on unmanaged nodes.
func (s *Service) StreamAppLogs(ctx context.Context, workspaceID, appID uint, follow bool, tail string, sink func(docker.LogLine) error) error {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return ErrNoActiveContainer
	}
	if tail == "" {
		tail = "200"
	}
	if app.RuntimeKind == models.RuntimeService {
		mgr, merr := s.swarmManager(ctx, app)
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

// WorkspaceSample is live container consumption, unlike /usage which reports declared reservations and quotas.
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

// Holds the resolved client, not a node id: a service app's task has no fixed node.
type sampleTarget struct {
	dc          docker.Client
	containerID string
}

// Floors the live-stream cadence so a client cannot hammer every node's Docker daemon.
const minWorkspaceUsageInterval = time.Second

// Best-effort: an app with no active container or an unreachable task is skipped rather than failing the reading.
func (s *Service) workspaceTargets(ctx context.Context, workspaceID uint) []sampleTarget {
	var targets []sampleTarget
	if apps, err := s.apps.ListByWorkspace(workspaceID); err == nil {
		for i := range apps {
			a := apps[i]
			if a.Status != models.AppStatusRunning {
				continue
			}
			if a.RuntimeKind == models.RuntimeService {
				// A task on an unmanaged node is left out: there is no engine to read it and no manager-side stats.
				if dc, cid, err := s.clients.ForServiceTask(ctx, a.ClusterID, node.AppAlias(&a)); err == nil {
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

// WorkspaceLiveUsage samples concurrently; containers that error mid-sample are skipped so one never fails the reading.
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

// StreamWorkspaceUsage samples now and then every interval until ctx ends; interval is floored to limit sampling load.
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

type WorkspaceHistoryPoint struct {
	At          time.Time `json:"at"`
	CPUPercent  float64   `json:"cpu_percent"`
	CPUCores    float64   `json:"cpu_cores"`
	MemoryBytes uint64    `json:"memory_bytes"`
}

// Floors the bucket so a client can't request pathologically fine buckets over a long window.
const minHistoryBucket = 15 * time.Second

// WorkspaceUsageHistory counts each app once per bucket (latest sample), so wide buckets never double-count.
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

// RecentEvent carries the app's handle/label so the dashboard needs no extra lookup.
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

// Memoised per overview call; returns "" if unresolved.
func (s *Service) serverName(cache map[uint]string, serverID uint) string {
	if name, ok := cache[serverID]; ok {
		return name
	}
	name := ""
	if s.serverInfo != nil {
		if srv, err := s.serverInfo.Get(serverID); err == nil && srv != nil {
			name = srv.Label()
		}
	}
	cache[serverID] = name
	return name
}

// History verifies the app's workspace first; a foreign app yields empty rather than confirming it exists.
func (s *Service) History(workspaceID, appID uint, since time.Time, limit int) ([]models.MetricSample, error) {
	if _, err := s.apps.FindInWorkspace(workspaceID, appID); err != nil {
		return []models.MetricSample{}, nil
	}
	return s.metrics.ListByApp(appID, since, limit)
}

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

// Two samples so CPU is computed against a prior reading; a one-shot sample reports ~0%.
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
		// A deploy in progress keeps the previous release serving, so only a first-ever deploy is unknown.
		if hasCurrentRelease {
			return "healthy"
		}
		return "unknown"
	default: // created, stopped
		return "unknown"
	}
}
