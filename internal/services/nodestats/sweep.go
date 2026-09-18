// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodestats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const (
	// sweepConcurrency bounds how many nodes are measured at once, so the sweep takes about as long
	// on a large fleet as on a small one without opening a connection per node.
	sweepConcurrency = 8
	// dataRootMount is where a node's Docker data root is bound in the sizing helper. Read-only: the
	// helper only needs to stat the filesystem.
	dataRootMount = "/mnt/dataroot"
	// capacityDriftPct is how far memory or storage may move before it counts as a real change. A
	// filesystem gains and loses a few blocks constantly; a resized VM does not.
	capacityDriftPct = 2
)

// Servers is the node store the sweep reads and writes. Satisfied by repositories.ServerRepository.
type Servers interface {
	List() ([]models.Server, error)
	SetCapacity(id uint, cores int, memory, storage, storageFree int64, at time.Time) error
	SetUsage(id uint, cpuPercent float64, memUsed int64, at time.Time) error
}

// SetServers wires the node store the sweep persists into (nil disables the sweep).
func (s *Service) SetServers(store Servers) { s.servers = store }

// Sweep measures every reachable node and records what changed. Capacity comes from Docker, which
// is cgroup-aware and so describes the node itself; usage comes from a sample on the node and is
// stored only when it describes that node rather than the machine under it.
//
// It is best-effort by design: an unreachable node keeps its last known figures, because a node
// Miabi cannot currently reach has not lost its CPUs.
func (s *Service) Sweep(ctx context.Context) error {
	if s.servers == nil {
		return nil
	}
	list, err := s.servers.List()
	if err != nil {
		logger.Warn("node capacity sweep: list nodes failed", "error", err)
		return nil
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, sweepConcurrency)
	for i := range list {
		srv := list[i]
		if !srv.IsLocal && srv.Status == models.ServerStatusOffline {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.measureNode(ctx, srv)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Service) measureNode(ctx context.Context, srv models.Server) {
	dc, err := s.clients.For(srv.ID)
	if err != nil {
		return
	}
	infoCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	info, err := dc.Info(infoCtx)
	cancel()
	if err != nil {
		return
	}

	storage, free := s.measureDataRoot(ctx, dc, info.DataRoot)
	// A failed disk reading must not erase the last good one: keep the stored figure rather than
	// writing a zero that would read as "this node has no disk".
	if storage == 0 {
		storage, free = srv.StorageBytes, srv.StorageFreeBytes
	}

	if capacityChanged(srv, info, storage, free) {
		logCapacityChange(srv, info, storage)
		if err := s.servers.SetCapacity(srv.ID, info.CPUs, info.MemTotal, storage, free, time.Now()); err != nil {
			logger.Warn("node capacity sweep: record capacity failed", "node", srv.Name, "error", err)
		}
	}

	sample, err := s.Get(ctx, srv.ID)
	if err != nil || !sample.DescribesNode {
		return
	}
	used := int64(sample.MemUsedBytes)
	if info.MemTotal > 0 && used > info.MemTotal {
		used = info.MemTotal
	}
	if err := s.servers.SetUsage(srv.ID, sample.CPUPercent, used, time.Now()); err != nil {
		logger.Warn("node capacity sweep: record usage failed", "node", srv.Name, "error", err)
	}
}

// measureDataRoot sizes the filesystem holding the daemon's data root. Returns zeros when it cannot
// be read, which the caller treats as "keep what we had".
func (s *Service) measureDataRoot(ctx context.Context, dc docker.Client, dataRoot string) (total, free int64) {
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return 0, 0
	}
	image := s.helperImage()
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	if present, perr := dc.ImageExists(runCtx, image); perr != nil || !present {
		if err := dc.PullImage(runCtx, image, nil); err != nil {
			return 0, 0
		}
	}
	exit, out, err := dc.RunOneShot(runCtx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-nodecap-%d", time.Now().UnixNano()),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"df -Pk " + dataRootMount},
		// The source is read from the daemon's own info, never from client input, and mounted
		// read-only: this only ever stats a filesystem.
		Binds: []docker.BindMount{{Source: dataRoot, Target: dataRootMount, ReadOnly: true, NoCreate: true}},
	})
	if err != nil || exit != 0 {
		return 0, 0
	}
	return parseDF(out)
}

// parseDF reads `df -Pk` output: the last line's 2nd and 4th columns, in kB.
func parseDF(out string) (total, free int64) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, 0
	}
	var totalKB, freeKB int64
	if _, err := fmt.Sscanf(fields[1], "%d", &totalKB); err != nil {
		return 0, 0
	}
	if _, err := fmt.Sscanf(fields[3], "%d", &freeKB); err != nil {
		return 0, 0
	}
	return totalKB * 1024, freeKB * 1024
}

// capacityChanged reports whether anything moved enough to be worth a write. Core count is exact —
// a CPU appearing or disappearing is always news — while memory and disk allow a little drift.
func capacityChanged(srv models.Server, info docker.Info, storage, free int64) bool {
	if srv.CapacityMeasuredAt == nil {
		return true
	}
	if srv.CPUCores != info.CPUs {
		return true
	}
	return driftedPct(srv.MemoryBytes, info.MemTotal) ||
		driftedPct(srv.StorageBytes, storage) ||
		driftedPct(srv.StorageFreeBytes, free)
}

func driftedPct(old, now int64) bool {
	if old == now {
		return false
	}
	if old == 0 || now == 0 {
		return true
	}
	delta := old - now
	if delta < 0 {
		delta = -delta
	}
	return delta*100/old >= capacityDriftPct
}

// logCapacityChange says so out loud when a node gains or loses CPU or memory: that is a machine
// somebody resized, and it changes what will fit on it.
func logCapacityChange(srv models.Server, info docker.Info, storage int64) {
	if srv.CapacityMeasuredAt == nil {
		return // first measurement, not a change
	}
	if srv.CPUCores != info.CPUs || driftedPct(srv.MemoryBytes, info.MemTotal) {
		logger.Info("node capacity changed",
			"node", srv.Name,
			"cpu_cores", fmt.Sprintf("%d -> %d", srv.CPUCores, info.CPUs),
			"memory_bytes", fmt.Sprintf("%d -> %d", srv.MemoryBytes, info.MemTotal),
			"storage_bytes", fmt.Sprintf("%d -> %d", srv.StorageBytes, storage),
		)
	}
}

// compile-time check that the repository satisfies the store the sweep needs.
var _ Servers = (*repositories.ServerRepository)(nil)
