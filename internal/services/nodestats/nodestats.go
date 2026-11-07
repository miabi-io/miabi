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

// Package nodestats reads host CPU/memory per node: local procfs, agent pushes, or a short-lived sampling container.
package nodestats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hoststats"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

const (
	// Each sample costs a container start plus one second of measuring, so this is a dashboard figure.
	ttl = 60 * time.Second
	// localTTL is short because reading the local procfs costs nothing: the node page polls it.
	localTTL = 5 * time.Second
	// runTimeout covers image pull, container start and the sample window.
	runTimeout = 45 * time.Second
	// probeTimeout keeps one unreachable node from stalling a sweep.
	probeTimeout     = 5 * time.Second
	defaultHelperImg = "busybox:1.36"
)

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
}

// ErrLXCFS means the local node is an LXC guest whose procfs reports only the reading container.
var ErrLXCFS = errors.New("this node is an LXC guest: its /proc is served by lxcfs, which reports the control plane container's own usage rather than the node's")

// localNodes is implemented by clients that know which node is the control plane's own.
type localNodes interface {
	IsLocal(serverID uint) bool
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// Sample is a node's host reading plus whether it actually describes THAT node.
type Sample struct {
	hoststats.Stats
	// /proc is not cgroup-aware: a nested node reports its physical host, which must not be summed into fleet totals.
	DescribesNode bool
	// NodeMemTotalBytes is what Docker says this node has, which IS cgroup-aware.
	NodeMemTotalBytes int64
	// Source is SourceAgent or SourceSampled.
	Source string
	// MeasuredAt is when the control plane took or received the reading, never the node's clock.
	MeasuredAt    time.Time
	Load1         float64
	UptimeSeconds uint64
}

// Where a Sample came from.
const (
	SourceLocal   = "local"
	SourceAgent   = "agent"
	SourceSampled = "sampled"
)

type entry struct {
	at     time.Time
	sample Sample
	err    error
}

func (e entry) ttl() time.Duration {
	if e.err == nil && e.sample.Source == SourceLocal {
		return localTTL
	}
	return ttl
}

// Same number on an ordinary host, so this only absorbs rounding.
const memTolerancePct = 5

// Docker's MemTotal is the authority; with no Docker figure nothing contradicts the reading.
func describesNode(readTotal uint64, nodeTotal int64) bool {
	if nodeTotal <= 0 {
		return true
	}
	delta := nodeTotal - int64(readTotal)
	if delta < 0 {
		delta = -delta
	}
	return delta*100/nodeTotal <= memTolerancePct
}

// Service samples nodes on demand and caches the result per node.
type Service struct {
	clients NodeDocker
	images  ImageResolver

	servers Servers

	mu    sync.Mutex
	cache map[uint]entry
	// pushed holds the latest agent report per node; persisted is what was last written from one.
	pushed    map[uint]Sample
	persisted map[uint]Sample
	agentOn   bool
	now       func() time.Time
	// hostProc is the procfs the local node is read from (MIABI_HOST_PROC); /proc when unreadable.
	hostProc string
	// Concurrent readers of one node share a single sample instead of starting a container each.
	inflight map[uint]*sync.WaitGroup
}

func NewService(clients NodeDocker) *Service {
	return &Service{
		clients: clients, cache: map[uint]entry{}, inflight: map[uint]*sync.WaitGroup{},
		pushed: map[uint]Sample{}, persisted: map[uint]Sample{}, agentOn: true, now: time.Now,
	}
}

// SetHostProc sets the procfs directory the local node is read from.
func (s *Service) SetHostProc(path string) { s.hostProc = path }

// SetImageResolver wires the deployment-config resolver for the helper image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return defaultHelperImg
}

// Get prefers a fresh agent push, else samples at most once per ttl; failures are cached so dead nodes aren't retried per tick.
func (s *Service) Get(ctx context.Context, serverID uint) (Sample, error) {
	s.mu.Lock()
	if p, ok := s.freshPushLocked(serverID); ok {
		s.mu.Unlock()
		return p, nil
	}
	if e, ok := s.cache[serverID]; ok && time.Since(e.at) < e.ttl() {
		s.mu.Unlock()
		return e.sample, e.err
	}
	if wg, running := s.inflight[serverID]; running {
		s.mu.Unlock()
		wg.Wait()
		s.mu.Lock()
		e := s.cache[serverID]
		s.mu.Unlock()
		return e.sample, e.err
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	s.inflight[serverID] = wg
	s.mu.Unlock()

	sample, err := s.sample(ctx, serverID)

	s.mu.Lock()
	s.cache[serverID] = entry{at: time.Now(), sample: sample, err: err}
	delete(s.inflight, serverID)
	s.mu.Unlock()
	wg.Done()
	return sample, err
}

// Cached reports false for a never-sampled node, which callers render as "unknown" rather than zero.
func (s *Service) Cached(serverID uint) (Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.freshPushLocked(serverID); ok {
		return p, true
	}
	e, ok := s.cache[serverID]
	if !ok || e.err != nil {
		return Sample{}, false
	}
	return e.sample, true
}

func (s *Service) sample(ctx context.Context, serverID uint) (Sample, error) {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return Sample{}, err
	}
	if s.isLocal(serverID) {
		if proc := s.localProc(); proc != "" {
			// A container on an LXC guest reads the physical machine: unmeasured beats a plausible wrong figure.
			if hoststats.ServedByLXCFS(proc) {
				return Sample{}, ErrLXCFS
			}
			return s.sampleLocal(ctx, dc, proc)
		}
	}
	image := s.helperImage()
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	// PullImage always hits the registry; per-node per-minute pulls would mean latency and Docker Hub rate limits.
	if present, perr := dc.ImageExists(runCtx, image); perr != nil || !present {
		if err := dc.PullImage(runCtx, image, nil); err != nil {
			return Sample{}, fmt.Errorf("pull helper image: %w", err)
		}
	}
	exit, out, err := dc.RunOneShot(runCtx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-hoststats-%d-%d", serverID, time.Now().UnixNano()),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{hoststats.SampleCommand[2]},
	})
	if err != nil {
		return Sample{}, err
	}
	if exit != 0 {
		return Sample{}, fmt.Errorf("host stats helper exited %d: %s", exit, out)
	}
	st, err := hoststats.ParseSample(out)
	if err != nil {
		return Sample{}, err
	}

	// Docker's MemTotal is cgroup-aware; when it disagrees, /proc describes the underlying machine.
	out2 := Sample{Stats: st, DescribesNode: true, Source: SourceSampled, MeasuredAt: s.now()}
	if info, ierr := dc.Info(runCtx); ierr == nil && info.MemTotal > 0 {
		out2.NodeMemTotalBytes = info.MemTotal
		out2.DescribesNode = describesNode(st.MemTotalBytes, info.MemTotal)
	}
	return out2, nil
}

func (s *Service) isLocal(serverID uint) bool {
	if l, ok := s.clients.(localNodes); ok {
		return l.IsLocal(serverID)
	}
	return serverID == 0
}

// "" when no procfs is readable; the local node is then sampled by container like any other.
func (s *Service) localProc() string {
	if s.hostProc != "" && hoststats.Available(s.hostProc) {
		return s.hostProc
	}
	if hoststats.Available("/proc") {
		return "/proc"
	}
	return ""
}

// The configured bind shows the guest's view on LXC hosts, unlike a container's /proc.
func (s *Service) sampleLocal(ctx context.Context, dc docker.Client, proc string) (Sample, error) {
	st, err := hoststats.Read(ctx, proc)
	if err != nil {
		return Sample{}, err
	}
	out := Sample{Stats: st, DescribesNode: true, Source: SourceLocal, MeasuredAt: s.now()}
	infoCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if info, ierr := dc.Info(infoCtx); ierr == nil && info.MemTotal > 0 {
		out.NodeMemTotalBytes = info.MemTotal
		out.DescribesNode = describesNode(st.MemTotalBytes, info.MemTotal)
	}
	return out, nil
}
