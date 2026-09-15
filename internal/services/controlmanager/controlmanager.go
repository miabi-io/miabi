// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package controlmanager watches for workloads that disappeared from under Miabi: a container app whose active
// release container is gone from its node, and a service app whose swarm service is gone from its cluster. Its
// scope is restoring what Miabi decided where Miabi decided it; it never places or moves a workload. So far it
// only observes: findings become timeline events, metrics and an admin report, and nothing is redeployed.
package controlmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/settings"
)

// Mode is how far the control manager goes.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeObserve Mode = "observe"
)

const (
	sweepTimeout = 50 * time.Second
	// nodeGrace keeps a freshly connected agent's partial view from reading as deleted containers.
	nodeGrace = time.Minute
	// confirmAfter is how many sweeps must see an app missing before it is reported, so a container caught
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

// DeploymentStore reports the apps with a deploy under way. Satisfied by *repositories.DeploymentRepository.
type DeploymentStore interface {
	InProgressAppIDs() ([]uint, error)
}

// Recorder writes app timeline events. Satisfied by *events.Service.
type Recorder interface {
	Emit(workspaceID, appID uint, t models.AppEventType, sev models.AppEventSeverity, message string, meta map[string]string, actorID *uint)
}

// Settings reads platform settings. Satisfied by *settings.Provider.
type Settings interface {
	String(key, def string) string
}

// Service sweeps the platform for missing workloads.
type Service struct {
	nodes    NodeDocker
	clusters ClusterManager
	apps     AppStore
	releases ReleaseStore
	deploys  DeploymentStore
	events   Recorder
	settings Settings
	now      func() time.Time

	mu        sync.Mutex
	findings  map[uint]*Finding // by app id
	skipped   []Skip
	lastSweep *time.Time
	sweepTook time.Duration
}

// New builds the control manager. It does nothing until Tick is scheduled.
func New(nodes NodeDocker, clusters ClusterManager, apps AppStore, releases ReleaseStore, deploys DeploymentStore, events Recorder, settings Settings) *Service {
	return &Service{
		nodes: nodes, clusters: clusters, apps: apps, releases: releases, deploys: deploys, events: events, settings: settings,
		now:      time.Now,
		findings: map[uint]*Finding{},
	}
}

// Mode returns the configured mode. Any value but off observes: observing acts on nothing, and an
// unrecognized value must not quietly switch detection off.
func (s *Service) Mode() Mode {
	if strings.EqualFold(strings.TrimSpace(s.settings.String(settings.KeyControlManagerMode, "")), string(ModeOff)) {
		return ModeOff
	}
	return ModeObserve
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
	apps, err := s.desired()
	if err != nil {
		return err
	}
	seen, skipped := s.observe(ctx, apps)
	s.record(apps, seen, skipped, start)
	return nil
}

// desired returns the apps expected to be running, minus those with a deploy under way: the deploy path is
// removing and starting their containers, and what it leaves behind is judged by a later sweep.
func (s *Service) desired() ([]models.Application, error) {
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
	out := apps[:0]
	for _, a := range apps {
		if !deploying[a.ID] {
			out = append(out, a)
		}
	}
	return out, nil
}
