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

package cluster

import (
	"sort"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Dedication is derived from OrganizationID and Visibility rather than a third stored copy.

// OrgLabels names organizations in bulk. Satisfied by the organization service.
type OrgLabels func(ids []uint) map[uint]string

// Without it a cluster still reports as dedicated, just unnamed.
func (s *Service) SetOrgLabels(fn OrgLabels) { s.orgLabels = fn }

type OrgDefaultAligner func(orgID, prefer uint) error

// Without it a newly dedicated organization keeps a default it can no longer place in.
func (s *Service) SetOrgDefaultAligner(fn OrgDefaultAligner) { s.orgAligner = fn }

// The previous owner's default may have been this very cluster.
func (s *Service) alignOrgDefaults(clusterID, from, to uint) {
	if s.orgAligner == nil {
		return
	}
	for _, orgID := range []uint{to, from} {
		if orgID == 0 || (orgID == to && to == from) {
			continue
		}
		prefer := uint(0)
		if orgID == to {
			prefer = clusterID
		}
		if err := s.orgAligner(orgID, prefer); err != nil {
			logger.Warn("could not realign the organization's default location", "organization", orgID, "error", err)
		}
	}
}

// Shown before an ownership change, which strands whatever the new owner does not own.
type DedicationImpact struct {
	ClusterID uint  `json:"cluster_id"`
	Workloads int64 `json:"workloads"`
	// Name is empty for workspaces in no organization.
	Tenants []TenantWorkloads `json:"tenants"`
}

// TenantWorkloads is one organization's share of a cluster.
type TenantWorkloads struct {
	OrganizationID uint   `json:"organization_id"`
	Name           string `json:"name,omitempty"`
	Workloads      int64  `json:"workloads"`
}

// Dedication reports what a change of owner would affect in this cluster.
func (s *Service) Dedication(clusterID uint) (*DedicationImpact, error) {
	if s.store == nil {
		return &DedicationImpact{ClusterID: clusterID, Tenants: []TenantWorkloads{}}, nil
	}
	byOrg, err := s.store.WorkloadsByOrganization(clusterID)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(byOrg))
	for id := range byOrg {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	labels := map[uint]string{}
	if s.orgLabels != nil && len(ids) > 0 {
		labels = s.orgLabels(ids)
	}
	out := &DedicationImpact{ClusterID: clusterID, Tenants: make([]TenantWorkloads, 0, len(byOrg))}
	for id, n := range byOrg {
		out.Workloads += n
		out.Tenants = append(out.Tenants, TenantWorkloads{OrganizationID: id, Name: labels[id], Workloads: n})
	}
	sort.Slice(out.Tenants, func(i, j int) bool {
		if out.Tenants[i].Workloads != out.Tenants[j].Workloads {
			return out.Tenants[i].Workloads > out.Tenants[j].Workloads
		}
		return out.Tenants[i].OrganizationID < out.Tenants[j].OrganizationID
	})
	return out, nil
}

// markDedication annotates each cluster with whether it belongs to an organization, and which.
func (s *Service) markDedication(list []models.Cluster) {
	ids := make([]uint, 0, len(list))
	for i := range list {
		if list[i].OrganizationID != nil {
			ids = append(ids, *list[i].OrganizationID)
		}
	}
	if len(ids) == 0 {
		return
	}
	labels := map[uint]string{}
	if s.orgLabels != nil {
		labels = s.orgLabels(ids)
	}
	for i := range list {
		if list[i].OrganizationID == nil {
			continue
		}
		list[i].Dedicated = true
		list[i].OrganizationName = labels[*list[i].OrganizationID]
	}
}

// A node holds no organization of its own: it would go stale when the node moved cluster.
func (s *Service) markNodeDedication(servers []models.Server) {
	if len(servers) == 0 {
		return
	}
	list, err := s.Clusters()
	if err != nil {
		return
	}
	byID := make(map[uint]*models.Cluster, len(list))
	for i := range list {
		byID[list[i].ID] = &list[i]
		if list[i].IsDefault {
			byID[models.DefaultClusterID] = &list[i]
		}
	}
	for i := range servers {
		c, ok := byID[servers[i].ClusterID]
		if !ok || !c.Dedicated {
			continue
		}
		servers[i].Dedicated = true
		servers[i].OrganizationName = c.OrganizationName
		servers[i].ClusterName = c.Label()
	}
}
