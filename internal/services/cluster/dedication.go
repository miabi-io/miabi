// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import "github.com/miabi-io/miabi/internal/models"

// A cluster with an owning organization is dedicated to it: nothing outside that organization may
// be placed there, not even by a platform admin (see placement's access.allows, which checks
// ownership before the admin bypass). The console says so, and reads it from here rather than from
// a stored flag, which would be a third copy of what OrganizationID and Visibility already carry.

// OrgLabels names organizations in bulk. Satisfied by the organization service.
type OrgLabels func(ids []uint) map[uint]string

// SetOrgLabels wires how a dedicated cluster's organization is named. Without it a cluster still
// reports as dedicated, just unnamed.
func (s *Service) SetOrgLabels(fn OrgLabels) { s.orgLabels = fn }

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
