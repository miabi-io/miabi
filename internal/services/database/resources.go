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

package database

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Resources are an instance's container limits. Zero means unlimited.
type Resources struct {
	MemoryBytes int64
	NanoCPUs    int64
	// Size names the database size the limits come from (Enterprise); empty for limits set by hand.
	Size string
}

const (
	mebibyte      = 1 << 20
	nanosPerCore  = 1_000_000_000
	nanosPerMilli = 1_000_000
)

func (e engineSpec) minMemory() int64   { return int64(e.minMemoryMB) * mebibyte }
func (e engineSpec) minNanoCPUs() int64 { return int64(e.minMilliCPU) * nanosPerMilli }

// validate refuses negative limits, and limits too small for the engine to run on: memory that low ends with
// the kernel killing the database, and CPU that low with it timing out on start, rather than a clear error.
func (r Resources) validate(engine models.DBEngine, spec engineSpec) error {
	switch {
	case r.MemoryBytes < 0 || r.NanoCPUs < 0:
		return fmt.Errorf("%w: memory and CPU cannot be negative", ErrInvalidResources)
	case r.MemoryBytes > 0 && r.MemoryBytes < spec.minMemory():
		return fmt.Errorf("%w: %s needs at least %d MB of memory", ErrInvalidResources, engine, spec.minMemoryMB)
	case r.NanoCPUs > 0 && r.NanoCPUs < spec.minNanoCPUs():
		return fmt.Errorf("%w: %s needs at least %s CPU", ErrInvalidResources, engine, formatCores(spec.minNanoCPUs()))
	}
	return nil
}

// withDefaults sizes each dimension the plan caps but the request leaves unset, so an instance created without
// a size, by a marketplace install or a manifest, still counts against the budget.
func (r Resources) withDefaults(spec engineSpec, cpuCapped, memoryCapped bool) Resources {
	if memoryCapped && r.MemoryBytes == 0 {
		r.MemoryBytes = int64(spec.defaultMemoryMB) * mebibyte
	}
	if cpuCapped && r.NanoCPUs == 0 {
		r.NanoCPUs = int64(spec.defaultMilliCPU) * nanosPerMilli
	}
	return r
}

func formatCores(nanoCPUs int64) string {
	return strconv.FormatFloat(float64(nanoCPUs)/nanosPerCore, 'f', -1, 64)
}

// tuningArgs sizes the engine's own memory use to the container limit. Left alone, an engine sizes itself from
// the host's memory and is killed when it reaches the container's.
func tuningArgs(engine models.DBEngine, memoryBytes int64) []string {
	if memoryBytes <= 0 {
		return nil
	}
	mb := memoryBytes / mebibyte
	switch engine {
	case models.DBEnginePostgres:
		return []string{"-c", fmt.Sprintf("shared_buffers=%dMB", mb/4), "-c", fmt.Sprintf("effective_cache_size=%dMB", mb*3/4)}
	case models.DBEngineMySQL:

		if mb < 1024 {
			return []string{fmt.Sprintf("--innodb-buffer-pool-size=%dM", max(32, mb/4)), "--performance-schema=OFF"}
		}

		return []string{fmt.Sprintf("--innodb-buffer-pool-size=%dM", max(128, min(mb*6/10, mb-640)/128*128))}
	case models.DBEngineMariaDB:
		return []string{fmt.Sprintf("--innodb-buffer-pool-size=%dM", max(64, min(mb*6/10, mb-160)))}
	case models.DBEngineRedis:
		return []string{"--maxmemory", strconv.FormatInt(memoryBytes*3/4, 10)}
	case models.DBEngineMongoDB:
		return []string{"--wiredTigerCacheSizeGB", strconv.FormatFloat(max(0.25, float64(mb-1024)/2/1024), 'f', 2, 64)}
	}
	return nil
}

// Resize sets an instance's CPU and memory limits and recreates its container on the same data volume, so the
// engine is re-tuned to the new memory. The database restarts briefly; a stopped instance stays stopped.
func (s *Service) Resize(ctx context.Context, inst *models.DatabaseInstance, res Resources) (*models.DatabaseInstance, error) {
	if inst.Status == models.DBStatusUpgrading || inst.Status == models.DBStatusProvisioning {
		return nil, ErrInstanceBusy
	}
	spec, ok := specs[inst.Engine]
	if !ok {
		return nil, ErrUnsupportedEngine
	}
	res, err := s.resolveSize(inst.WorkspaceID, spec, res, true)
	if err != nil {
		return nil, err
	}
	if err := res.validate(inst.Engine, spec); err != nil {
		return nil, err
	}
	cpuCapped, memoryCapped := s.quota.DatabaseComputeCapped(inst.WorkspaceID)
	switch {
	case memoryCapped && res.MemoryBytes == 0:
		return nil, fmt.Errorf("%w: this workspace's plan caps database memory, so the instance needs a memory limit", ErrInvalidResources)
	case cpuCapped && res.NanoCPUs == 0:
		return nil, fmt.Errorf("%w: this workspace's plan caps database CPU, so the instance needs a CPU limit", ErrInvalidResources)
	}
	previous := Resources{MemoryBytes: inst.MemoryBytes, NanoCPUs: inst.NanoCPUs, Size: inst.SizeClass}
	if res == previous {
		return inst, nil
	}
	if err := s.quota.CheckDatabaseComputeAdd(inst.WorkspaceID, res.NanoCPUs, res.MemoryBytes, inst.ID); err != nil {
		return nil, err
	}
	inst.MemoryBytes, inst.NanoCPUs, inst.SizeClass = res.MemoryBytes, res.NanoCPUs, res.Size
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	// Without a container there is nothing to recreate: the next bring-up starts with the new limits.
	if inst.ContainerID == "" {
		return inst, nil
	}
	if err := s.enqueuer.EnqueueResizeDB(inst.ID, inst.ServerID, previous); err != nil {
		return nil, err
	}
	return inst, nil
}

// RunResize recreates the instance's container with its new limits, on the worker. A container that does not
// come up with them is brought back with the previous limits, so a resize never leaves the database down.
func (s *Service) RunResize(ctx context.Context, instanceID uint, previous Resources) error {
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
	// Recreating the container would mount its data volume again, so a lost one stops the resize here rather
	// than bringing the instance up on an empty volume.
	if err := s.guardData(ctx, inst); err != nil {
		return err
	}
	wasStopped := inst.Status == models.DBStatusStopped
	s.publishProgress(inst, "Applying new resources")
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		logger.Warn("database resize failed; restoring previous resources", "id", inst.ID, "error", err)
		inst.MemoryBytes, inst.NanoCPUs, inst.SizeClass = previous.MemoryBytes, previous.NanoCPUs, previous.Size
		_ = s.repo.Update(inst)
		msg := "Applying new resources failed, previous resources restored: " + err.Error()
		if rb := s.bringUp(ctx, inst, spec, adminPass); rb != nil {
			inst.Status = models.DBStatusFailed
			_ = s.repo.Update(inst)
			msg = fmt.Sprintf("Applying new resources failed (%v) and restoring the previous ones failed (%v)", err, rb)
		}
		s.publishStatus(inst)
		s.emit(inst, models.EventDatabaseProvisionFailed, models.SeverityError, msg, nil)
		return err
	}
	if wasStopped {
		if dc, derr := s.dockerFor(inst); derr == nil && dc.StopContainer(ctx, inst.ContainerID, 10) == nil {
			inst.Status = models.DBStatusStopped
			_ = s.repo.Update(inst)
		}
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseRestarted, models.SeverityInfo, "Restarted with "+describeResources(inst), nil)
	return nil
}

func describeResources(inst *models.DatabaseInstance) string {
	cpu, memory := "unlimited CPU", "unlimited memory"
	if inst.NanoCPUs > 0 {
		cpu = formatCores(inst.NanoCPUs) + " CPU"
	}
	if inst.MemoryBytes > 0 {
		memory = fmt.Sprintf("%d MB of memory", inst.MemoryBytes/mebibyte)
	}
	if inst.SizeClass != "" {
		return fmt.Sprintf("size %s: %s and %s", inst.SizeClass, cpu, memory)
	}
	return cpu + " and " + memory
}
