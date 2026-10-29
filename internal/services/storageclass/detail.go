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

package storageclass

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// NodeDisk is one node's disk under a class: for the built-in class, the filesystem holding that
// node's Docker data root, as the node capacity sweep last measured it.
type NodeDisk struct {
	ServerID       uint       `json:"server_id"`
	ServerName     string     `json:"server_name"`
	IsLocal        bool       `json:"is_local"`
	CapacityBytes  int64      `json:"capacity_bytes"`
	AvailableBytes int64      `json:"available_bytes"`
	MeasuredAt     *time.Time `json:"measured_at,omitempty"`
}

// Detail is a class with what it holds and the disks behind it.
type Detail struct {
	*models.StorageClass
	// Nodes is set for the built-in class only: it spans every node, each on its own data root.
	Nodes     []NodeDisk                       `json:"nodes,omitempty"`
	Usage     []repositories.StorageClassUsage `json:"usage"`
	UsedBytes int64                            `json:"used_bytes"`
	Volumes   int                              `json:"volumes"`
	Databases int                              `json:"databases"`
}

// dataRootDisks turns the nodes' measured data roots into the built-in class's disks and their
// totals. A node never measured is listed but left out of the totals, so it does not read as empty.
func dataRootDisks(servers []models.Server) (disks []NodeDisk, capacity, available int64, at *time.Time) {
	for i := range servers {
		srv := &servers[i]
		d := NodeDisk{ServerID: srv.ID, ServerName: srv.Label(), IsLocal: srv.IsLocal, MeasuredAt: srv.CapacityMeasuredAt}
		if srv.CapacityMeasuredAt != nil && srv.StorageBytes > 0 {
			d.CapacityBytes, d.AvailableBytes = srv.StorageBytes, srv.StorageFreeBytes
			capacity += srv.StorageBytes
			available += srv.StorageFreeBytes
			if at == nil || srv.CapacityMeasuredAt.Before(*at) {
				at = srv.CapacityMeasuredAt
			}
		}
		disks = append(disks, d)
	}
	return disks, capacity, available, at
}

// fillBuiltin gives the built-in class the size of the disks it actually uses. It has no path of its
// own to measure, so without this it reads as 0 bytes on every install.
func (s *Service) fillBuiltin(c *models.StorageClass) []NodeDisk {
	if c == nil || c.Managed() {
		return nil
	}
	servers, err := s.repo.Servers()
	if err != nil {
		return nil
	}
	disks, capacity, available, at := dataRootDisks(servers)
	if at != nil {
		c.CapacityBytes, c.AvailableBytes, c.MeasuredAt = capacity, available, at
	}
	return disks
}

// Detail returns a class with the volumes and databases on it, from stored measurements only.
func (s *Service) Detail(id uint) (*Detail, error) {
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	out := &Detail{StorageClass: c, Nodes: s.fillBuiltin(c)}
	if out.Usage, err = s.repo.Usage(c.Name); err != nil {
		return nil, err
	}
	if out.Usage == nil {
		out.Usage = []repositories.StorageClassUsage{}
	}
	names := map[uint]string{}
	if servers, err := s.repo.Servers(); err == nil {
		for i := range servers {
			names[servers[i].ID] = servers[i].Label()
			if servers[i].IsLocal {
				names[0] = servers[i].Label()
			}
		}
	}
	for i := range out.Usage {
		u := &out.Usage[i]
		u.ServerName = names[u.ServerID]
		out.UsedBytes += u.UsedBytes
		if u.Kind == "database" {
			out.Databases++
		} else {
			out.Volumes++
		}
	}
	return out, nil
}
