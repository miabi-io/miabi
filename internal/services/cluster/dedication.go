// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"sort"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// A cluster with an owning organization is dedicated to it: nothing outside that organization may
// be placed there, not even by a platform admin (see placement's access.allows, which checks
// ownership before the admin bypass). The console says so, and reads it from here rather than from
// a stored flag, which would be a third copy of what OrganizationID and Visibility already carry.

// OrgLabels names organizations in bulk. Satisfied by the organization service.
type OrgLabels func(ids []uint) map[uint]string

// SetOrgLabels wires how a dedicated cluster's organization is named. Without it a cluster still
// reports as dedicated, just unnamed.
func (s *Service) SetOrgLabels(fn OrgLabels) { s.orgLabels = fn }

// OrgDefaultAligner repoints an organization's default location after its clusters changed.
// Satisfied by the organization service's AlignDefaultCluster.
type OrgDefaultAligner func(orgID, prefer uint) error

// SetOrgDefaultAligner wires that realignment. Without it a newly dedicated organization keeps a
// default location it can no longer place in.
func (s *Service) SetOrgDefaultAligner(fn OrgDefaultAligner) { s.orgAligner = fn }

// alignOrgDefaults realigns the organizations on either side of a cluster changing hands: the one
// it just went to, and the one it came from, whose default may have been this very cluster.
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

// DedicationImpact is what a cluster currently holds, broken down by the organization that owns it.
// The console shows it before an admin changes who the location belongs to: a change of owner
// decides who may see and place here, and strands whatever the new owner does not own.
type DedicationImpact struct {
	ClusterID uint  `json:"cluster_id"`
	Workloads int64 `json:"workloads"`
	// Tenants are the organizations with workloads here, largest first. Name is empty for the
	// workspaces that belong to no organization.
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

// markNodeDedication gives each node the dedication of the cluster it belongs to. A node holds no
// organization of its own: it would go stale the moment the node moved cluster.
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
