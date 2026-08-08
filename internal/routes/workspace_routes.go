// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/workspace"
)

// workspaceRoutes registers workspace CRUD, members, invitations and the audit log. Viewer and
// Developer read; Admin manages members, invitations and metadata; Owner deletes.
func (r *Router) workspaceRoutes() []okapi.RouteDefinition {
	ws := r.v1.Group("/workspaces").WithTagInfo(okapi.GroupTag{Name: "Workspaces", Description: "Workspaces, members, invitations, and audit log."})

	authed := func(extra ...okapi.Middleware) []okapi.Middleware {
		return append([]okapi.Middleware{r.authenticate}, extra...)
	}
	scoped := func(min models.WorkspaceRole) []okapi.Middleware {
		return []okapi.Middleware{r.authenticate, r.scope, middlewares.RequireRole(min)}
	}

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        "",
			Group:       ws,
			Middlewares: authed(),
			Handler:     okapi.H(r.h.workspace.Create),
			Summary:     "Create a workspace",
			Request:     &handlers.CreateWorkspaceRequest{},
			Response:    &dto.Response[models.Workspace]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "",
			Group:       ws,
			Middlewares: authed(),
			Handler:     r.h.workspace.List,
			Summary:     "List my workspaces",
		},
		{
			Method:      http.MethodPost,
			Path:        "/invitations/accept",
			Group:       ws,
			Middlewares: authed(),
			Handler:     okapi.H(r.h.workspace.AcceptInvite),
			Summary:     "Accept an invitation",
			Request:     &handlers.AcceptInviteRequest{},
			Response:    &dto.Response[models.Workspace]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/invitations",
			Group:       ws,
			Middlewares: authed(),
			Handler:     r.h.workspace.MyInvitations,
			Summary:     "List my pending invitations",
			Response:    &dto.Response[[]workspace.PendingInvitation]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/invitations/{invitationID}/accept",
			Group:       ws,
			Middlewares: authed(),
			Handler:     r.h.workspace.AcceptMyInvitation,
			Summary:     "Accept a pending invitation by id",
			Response:    &dto.Response[models.Workspace]{},
		},

		// Single workspace (membership enforced via scope).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.workspace.Get,
			Summary:     "Get a workspace",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{workspace}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.workspace.Update),
			Summary:     "Update a workspace",
			Request:     &handlers.UpdateWorkspaceRequest{},
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{workspace}/name",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.workspace.UpdateName),
			Summary:     "Change a workspace name (its unique handle)",
			Request:     &handlers.UpdateWorkspaceNameRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     r.h.workspace.Delete,
			Summary:     "Delete a workspace",
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/deletion/jobs",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     r.h.workspace.StartDeletion,
			Summary:     "Start async workspace deletion (live progress via SSE)",
			Response:    &dto.Response[account.DeletionJob]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/deletion/jobs/{jobID}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     r.h.workspace.DeletionJob,
			Summary:     "Get a workspace deletion job snapshot",
			Response:    &dto.Response[account.DeletionJob]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/deletion/jobs/{jobID}/events",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleOwner),
			Handler:     r.h.workspace.DeletionJobEvents,
			Summary:     "Stream workspace deletion progress (SSE)",
		},

		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/members",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.workspace.ListMembers,
			Summary:     "List members",
		},
		{
			Method:      http.MethodPatch,
			Path:        "/{workspace}/members/{userID}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.workspace.UpdateMemberRole),
			Summary:     "Update a member's role",
			Request:     &handlers.UpdateMemberRoleRequest{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/{workspace}/members/{userID}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspace.RemoveMember,
			Summary:     "Remove a member",
			Response:    &dto.Response[dto.MessageData]{},
		},

		{
			Method:      http.MethodPost,
			Path:        "/{workspace}/invitations",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     okapi.H(r.h.workspace.Invite),
			Summary:     "Invite a member",
			Request:     &handlers.InviteRequest{},
			Response:    &dto.Response[handlers.InvitationCreated]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/invitations",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspace.ListInvitations,
			Summary:     "List pending invitations",
		},

		// Resource usage vs plan limits (viewer+).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/usage",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.usage.Get,
			Summary:     "Workspace resource usage vs plan limits",
			Response:    &dto.Response[handlers.WorkspaceUsage]{},
		},

		// Events (application activity across the workspace, viewer+).
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/events",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.events.WorkspaceList,
			Summary:     "List workspace application events",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/events/stream",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleViewer),
			Handler:     r.h.events.WorkspaceStream,
			Summary:     "Stream workspace application events (SSE)",
		},

		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/audit-logs",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspace.AuditLog,
			Summary:     "List workspace audit log",
		},
		{
			Method:      http.MethodGet,
			Path:        "/{workspace}/audit-logs/{auditID}",
			Group:       ws,
			Middlewares: scoped(models.WorkspaceRoleAdmin),
			Handler:     r.h.workspace.GetAuditLog,
			Summary:     "Get a workspace audit entry",
		},
	}
}
