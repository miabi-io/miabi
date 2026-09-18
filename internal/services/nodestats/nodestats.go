// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nodestats reads real host CPU and memory from any node, including remote ones, by running
// a short-lived container there and parsing its /proc. It exists because the agent tunnel proxies
// the Docker API and nothing else: there is no message a node could answer "what is your CPU" with.
// Running a container needs no agent change, works on nodes that have no agent at all, and needs no
// host path bound anywhere, since a container's /proc is the host's.
package nodestats

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hoststats"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

const (
	// ttl bounds how often a node is sampled. Each sample costs a container start plus the one
	// second SampleCommand spends measuring, so this is a dashboard figure, not a live graph.
	ttl = 60 * time.Second
	// runTimeout covers image pull, container start and the sample window.
	runTimeout       = 45 * time.Second
	defaultHelperImg = "busybox:1.36"
)

// NodeDocker resolves the Docker client for a node id (0 = local).
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
}

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// Sample is a node's host reading plus whether it actually describes THAT node.
type Sample struct {
	hoststats.Stats
	// DescribesNode is false when /proc reports a different machine than Docker does for this node.
	// /proc/meminfo and /proc/stat are not cgroup-aware, so a node that is itself a container or a
	// memory-limited VM reports the physical machine underneath it: informative to show on that
	// node's page, wrong to add into a fleet total, where several such nodes on one box would count
	// the same RAM repeatedly and exceed the fleet's real capacity.
	DescribesNode bool
	// NodeMemTotalBytes is what Docker says this node has, which IS cgroup-aware.
	NodeMemTotalBytes int64
}

type entry struct {
	at     time.Time
	sample Sample
	err    error
}

// memTolerancePct is how far the sampled MemTotal may sit from Docker's before the sample is
// treated as describing another machine. They are the same number on an ordinary host, so this only
// needs to absorb rounding.
const memTolerancePct = 5

// Service samples nodes on demand and caches the result per node.
type Service struct {
	clients NodeDocker
	images  ImageResolver

	mu    sync.Mutex
	cache map[uint]entry
	// inflight keeps concurrent readers of the same node behind one sample rather than starting a
	// container each: the dashboard and the node page can ask at the same moment.
	inflight map[uint]*sync.WaitGroup
}

func NewService(clients NodeDocker) *Service {
	return &Service{clients: clients, cache: map[uint]entry{}, inflight: map[uint]*sync.WaitGroup{}}
}

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

// Get returns a node's host stats, sampling it at most once per ttl. A failed sample is cached too,
// so an unreachable node is not retried on every dashboard tick.
func (s *Service) Get(ctx context.Context, serverID uint) (Sample, error) {
	s.mu.Lock()
	if e, ok := s.cache[serverID]; ok && time.Since(e.at) < ttl {
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

// Cached returns a node's last sample without triggering one. The bool is false when the node has
// never been sampled, which a caller renders as "unknown" rather than zero.
func (s *Service) Cached(serverID uint) (Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	image := s.helperImage()
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	// PullImage always contacts the registry, even for an image already on the node. Sampling every
	// node every minute would mean a Docker Hub round trip per node per minute — latency on every
	// sweep, and rate limiting on a fleet of any size.
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

	// Docker's MemTotal IS cgroup-aware, so it is the authority on what this node has. When the two
	// disagree, /proc is describing the machine the node runs on rather than the node.
	out2 := Sample{Stats: st, DescribesNode: true}
	if info, ierr := dc.Info(runCtx); ierr == nil && info.MemTotal > 0 {
		out2.NodeMemTotalBytes = info.MemTotal
		delta := info.MemTotal - int64(st.MemTotalBytes)
		if delta < 0 {
			delta = -delta
		}
		if delta*100/info.MemTotal > memTolerancePct {
			out2.DescribesNode = false
		}
	}
	return out2, nil
}
