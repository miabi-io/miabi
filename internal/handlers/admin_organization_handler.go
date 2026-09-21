// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/organization"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// AdminOrganizationHandler manages tenant realms: the workspaces they own, their workspace cap and
// the cluster they are dedicated to. Reading is always allowed — a Community install has exactly one
// organization and the page should still show it — while creating a SECOND one is the gated action.
type AdminOrganizationHandler struct {
	svc        *organization.Service
	users      *repositories.UserRepository
	clusters   *repositories.ClusterRepository
	workspaces *repositories.WorkspaceRepository
	ee         enterprise.EE
	audit      *audit.Logger
}

func NewAdminOrganizationHandler(svc *organization.Service, users *repositories.UserRepository, clusters *repositories.ClusterRepository, workspaces *repositories.WorkspaceRepository, ee enterprise.EE, auditLog *audit.Logger) *AdminOrganizationHandler {
	return &AdminOrganizationHandler{svc: svc, users: users, clusters: clusters, workspaces: workspaces, ee: ee, audit: auditLog}
}

// organizationView is everything the detail page shows, in one call: the organization, the clusters
// dedicated to it, and the workspaces it holds against its cap.
type organizationView struct {
	*models.Organization
	Clusters   []clusterRef   `json:"clusters"`
	Workspaces []workspaceRef `json:"workspaces"`
	UserCount  int64          `json:"user_count"`
	OwnerName  string         `json:"owner_name,omitempty"`
	OwnerEmail string         `json:"owner_email,omitempty"`
}

type clusterRef struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Cordoned    bool   `json:"cordoned"`
}

type workspaceRef struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	OwnerID     uint   `json:"owner_id"`
}

// List returns every organization with its workspace count. Never gated: one organization always
// exists, and hiding it would leave the admin console unable to explain where workspaces live.
func (h *AdminOrganizationHandler) List(c *okapi.Context) error {
	orgs, err := h.svc.List()
	if err != nil {
		return c.AbortInternalServerError("failed to list organizations", err)
	}
	return ok(c, orgs)
}

func (h *AdminOrganizationHandler) Get(c *okapi.Context) error {
	org, err := h.org(c)
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	view := organizationView{Organization: org, Clusters: []clusterRef{}, Workspaces: []workspaceRef{}}
	if h.clusters != nil {
		list, err := h.clusters.ListByOrganization(org.ID)
		if err != nil {
			return c.AbortInternalServerError("failed to read the organization's clusters", err)
		}
		for i := range list {
			view.Clusters = append(view.Clusters, clusterRef{
				ID: list[i].ID, Name: list[i].Name, DisplayName: list[i].DisplayName, Cordoned: list[i].Cordoned,
			})
		}
	}
	if h.workspaces != nil {
		list, err := h.workspaces.ListByOrganization(org.ID)
		if err != nil {
			return c.AbortInternalServerError("failed to read the organization's workspaces", err)
		}
		for i := range list {
			view.Workspaces = append(view.Workspaces, workspaceRef{
				ID: list[i].ID, Name: list[i].Name, DisplayName: list[i].DisplayName, OwnerID: list[i].OwnerID,
			})
		}
	}
	view.UserCount = h.svc.UserCount(org.ID)
	if org.OwnerUserID != 0 {
		if u, err := h.users.FindByID(org.OwnerUserID); err == nil {
			view.OwnerName, view.OwnerEmail = u.Name, u.Email
		}
	}
	return ok(c, view)
}

type AdminCreateOrganizationRequest struct {
	Body struct {
		Name          string `json:"name" max:"32"`
		DisplayName   string `json:"display_name" required:"true" max:"120"`
		OwnerUserID   uint   `json:"owner_user_id"`
		MaxWorkspaces *int   `json:"max_workspaces"`
		// Per-user caps for this org's users; null inherits the platform default.
		MaxWorkspacesPerUser           *int `json:"max_workspaces_per_user"`
		MaxWorkspaceMembershipsPerUser *int `json:"max_workspace_memberships_per_user"`
	} `json:"body"`
}

// Create makes an organization. This is the entitled action: Community keeps the single default org
// it has always had, so nothing is taken away, but a second realm is Enterprise.
func (h *AdminOrganizationHandler) Create(c *okapi.Context, req *AdminCreateOrganizationRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagOrganizations); err != nil {
		return entitlementAbort(c, err)
	}
	if req.Body.OwnerUserID != 0 {
		if _, err := h.users.FindByID(req.Body.OwnerUserID); err != nil {
			return c.AbortBadRequest("no such owner user")
		}
	}
	org, err := h.svc.Create(organization.CreateInput{
		Handle:                         req.Body.Name,
		DisplayName:                    req.Body.DisplayName,
		OwnerUserID:                    req.Body.OwnerUserID,
		MaxWorkspaces:                  req.Body.MaxWorkspaces,
		MaxWorkspacesPerUser:           req.Body.MaxWorkspacesPerUser,
		MaxWorkspaceMembershipsPerUser: req.Body.MaxWorkspaceMembershipsPerUser,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "admin.organization.create", org.ID, map[string]any{"name": org.Name})
	return created(c, org)
}

type AdminUpdateOrganizationRequest struct {
	Body struct {
		DisplayName   *string `json:"display_name" max:"120"`
		OwnerUserID   *uint   `json:"owner_user_id"`
		MaxWorkspaces *int    `json:"max_workspaces"`
		// Per-user caps. Null leaves one as it is; -1 is unlimited and 0 none. Inherit* clears one
		// back to the platform default, which a null cannot express.
		MaxWorkspacesPerUser               *int  `json:"max_workspaces_per_user"`
		MaxWorkspaceMembershipsPerUser     *int  `json:"max_workspace_memberships_per_user"`
		InheritWorkspacesPerUser           *bool `json:"inherit_workspaces_per_user"`
		InheritWorkspaceMembershipsPerUser *bool `json:"inherit_workspace_memberships_per_user"`
		// DefaultClusterID is the location the org's new workspaces land in. Sending 0 clears it,
		// which a null cannot express — null means "leave as it is".
		DefaultClusterID *uint `json:"default_cluster_id"`
	} `json:"body"`
}

func (h *AdminOrganizationHandler) Update(c *okapi.Context, req *AdminUpdateOrganizationRequest) error {
	org, err := h.org(c)
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	// Editing the default org's own label is not a multi-tenancy feature, so it stays available in
	// Community. Its limits are: with a licence an organization's caps govern its tenants, and
	// without one the platform defaults do — so an unlicensed operator has nothing to set here, and
	// letting them store a value that would never apply would be the misleading option.
	if !org.IsDefault || touchesOrgLimits(req) || req.Body.DefaultClusterID != nil {
		if err := h.ee.RequireMutable(enterprise.FlagOrganizations); err != nil {
			return entitlementAbort(c, err)
		}
	}
	in := organization.UpdateInput{
		DisplayName: req.Body.DisplayName, OwnerUserID: req.Body.OwnerUserID,
		MaxWorkspaces:                  req.Body.MaxWorkspaces,
		MaxWorkspacesPerUser:           req.Body.MaxWorkspacesPerUser,
		MaxWorkspaceMembershipsPerUser: req.Body.MaxWorkspaceMembershipsPerUser,
	}
	if req.Body.InheritWorkspacesPerUser != nil {
		in.InheritWorkspacesPerUser = *req.Body.InheritWorkspacesPerUser
	}
	if req.Body.InheritWorkspaceMembershipsPerUser != nil {
		in.InheritWorkspaceMembershipsPerUser = *req.Body.InheritWorkspaceMembershipsPerUser
	}
	if req.Body.DefaultClusterID != nil {
		if *req.Body.DefaultClusterID == 0 {
			in.ClearDefaultCluster = true
		} else {
			in.DefaultClusterID = req.Body.DefaultClusterID
		}
	}
	updated, err := h.svc.Update(org.ID, in)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "admin.organization.update", org.ID, limitChanges(org, updated))
	return ok(c, updated)
}

// SetDefault promotes an organization to the one every unassigned workspace, user and provider
// resolves to.
func (h *AdminOrganizationHandler) SetDefault(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagOrganizations); err != nil {
		return entitlementAbort(c, err)
	}
	org, err := h.org(c)
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	if err := h.svc.SetDefault(org.ID); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "admin.organization.set_default", org.ID, nil)
	return message(c, "default organization updated")
}

func (h *AdminOrganizationHandler) Delete(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagOrganizations); err != nil {
		return entitlementAbort(c, err)
	}
	org, err := h.org(c)
	if err != nil {
		return c.AbortNotFound("organization not found")
	}
	if err := h.svc.Delete(org.ID); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "admin.organization.delete", org.ID, map[string]any{"name": org.Name})
	return message(c, "organization deleted")
}

func (h *AdminOrganizationHandler) org(c *okapi.Context) (*models.Organization, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, organization.ErrNotFound
	}
	return h.svc.Get(uint(id))
}

// touchesOrgLimits reports whether an update tries to set any organization-scoped limit.
func touchesOrgLimits(req *AdminUpdateOrganizationRequest) bool {
	return req.Body.MaxWorkspaces != nil ||
		req.Body.MaxWorkspacesPerUser != nil ||
		req.Body.MaxWorkspaceMembershipsPerUser != nil ||
		req.Body.InheritWorkspacesPerUser != nil ||
		req.Body.InheritWorkspaceMembershipsPerUser != nil
}

// limitChanges records which caps an update moved, and from what. "Who lowered this limit" is the
// first question asked when a tenant hits one, and a nil metadata blob cannot answer it.
func limitChanges(before, after *models.Organization) map[string]any {
	out := map[string]any{}
	if before.MaxWorkspaces != after.MaxWorkspaces {
		out["max_workspaces"] = []int{before.MaxWorkspaces, after.MaxWorkspaces}
	}
	if !samePtr(before.MaxWorkspacesPerUser, after.MaxWorkspacesPerUser) {
		out["max_workspaces_per_user"] = []any{before.MaxWorkspacesPerUser, after.MaxWorkspacesPerUser}
	}
	if !samePtr(before.MaxWorkspaceMembershipsPerUser, after.MaxWorkspaceMembershipsPerUser) {
		out["max_workspace_memberships_per_user"] = []any{before.MaxWorkspaceMembershipsPerUser, after.MaxWorkspaceMembershipsPerUser}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func samePtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (h *AdminOrganizationHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, organization.ErrNotFound):
		return c.AbortNotFound("organization not found")
	case errors.Is(err, organization.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, organization.ErrDefaultProtected), errors.Is(err, organization.ErrNotEmpty):
		return c.AbortWithError(409, err)
	case errors.Is(err, organization.ErrClusterNotOurs):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, organization.ErrNameInvalid), errors.Is(err, organization.ErrNameReserved),
		errors.Is(err, organization.ErrInvalidMax), errors.Is(err, organization.ErrNameImmutable):
		return c.AbortBadRequest(err.Error())
	}
	return c.AbortInternalServerError("organization operation failed", err)
}

func (h *AdminOrganizationHandler) record(c *okapi.Context, action string, id uint, meta map[string]any) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "organization",
		TargetID: strconv.FormatUint(uint64(id), 10), IP: c.RealIP(), Metadata: meta,
	})
}
