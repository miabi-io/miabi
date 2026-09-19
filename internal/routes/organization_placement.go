// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"github.com/miabi-io/miabi/internal/services/organization"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// orgPlacement answers the placement engine's two organization questions. It lives here rather than
// in either service because it joins three of them, and placement must not depend on the
// organization service directly.
type orgPlacement struct {
	orgs       *organization.Service
	workspaces *repositories.WorkspaceRepository
	clusters   *repositories.ClusterRepository
}

// OrganizationOfWorkspace resolves the realm a workspace belongs to, mapping an unassigned workspace
// to the default organization. 0 when it cannot be read, which leaves the caller on shared clusters.
func (o orgPlacement) OrganizationOfWorkspace(workspaceID uint) uint {
	ws, err := o.workspaces.FindByID(workspaceID)
	if err != nil {
		return 0
	}
	return o.orgs.Resolve(ws.OrganizationID)
}

// OwnsClusters reports whether the organization has clusters of its own, which confines it to them.
// It fails open — a read error must not strand every create — because the per-cluster ownership check
// still refuses another tenant's cluster on its own.
func (o orgPlacement) OwnsClusters(orgID uint) bool {
	if orgID == 0 {
		return false
	}
	n, err := o.clusters.CountByOrganization(orgID)
	return err == nil && n > 0
}
