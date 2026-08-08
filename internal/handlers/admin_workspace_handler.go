// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/keyring"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// KeyRotator rotates a workspace's encryption key (re-encrypting its secrets).
// Implemented by the keyring service; injected, nil = endpoint returns 501.
type KeyRotator interface {
	Rotate(ctx context.Context, workspaceID uint) (keyring.RotateResult, error)
}

// AdminWorkspaceHandler exposes platform-wide workspace administration.
type AdminWorkspaceHandler struct {
	db         *gorm.DB
	workspaces *repositories.WorkspaceRepository
	audit      *audit.Logger
	keys       KeyRotator
}

func NewAdminWorkspaceHandler(db *gorm.DB, workspaces *repositories.WorkspaceRepository, auditLog *audit.Logger) *AdminWorkspaceHandler {
	return &AdminWorkspaceHandler{db: db, workspaces: workspaces, audit: auditLog}
}

// SetKeyRotator wires per-workspace encryption-key rotation (nil-safe).
func (h *AdminWorkspaceHandler) SetKeyRotator(k KeyRotator) { h.keys = k }

// RotateKey rotates the workspace's encryption key and re-encrypts its secrets.
func (h *AdminWorkspaceHandler) RotateKey(c *okapi.Context) error {
	if h.keys == nil {
		return c.AbortWithError(501, errors.New("key rotation is not enabled"))
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid workspace id")
	}
	res, err := h.keys.Rotate(c.Request().Context(), uint(id))
	if err != nil {
		return c.AbortInternalServerError("key rotation failed", err)
	}
	actor := middlewares.UserID(c)
	wsID := uint(id)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: "admin.workspace.rotate_key",
		TargetType: "workspace", TargetID: strconv.Itoa(id), IP: c.RealIP(),
		Metadata: map[string]any{"version": res.Version, "reencrypted": res.Reencrypted},
	})
	return ok(c, res)
}

// AdminWorkspace is a workspace row for the admin workspaces table.
type AdminWorkspace struct {
	ID             uint      `json:"id"`
	Name           string    `json:"name"`
	DisplayName    string    `json:"display_name"`
	OwnerID        uint      `json:"owner_id"`
	OwnerName      string    `json:"owner_name"`
	OwnerEmail     string    `json:"owner_email"`
	Privileged     bool      `json:"privileged"`
	AppsCount      int64     `json:"apps_count"`
	DatabasesCount int64     `json:"databases_count"`
	StacksCount    int64     `json:"stacks_count"`
	MembersCount   int64     `json:"members_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type SetWorkspacePrivilegedRequest struct {
	Body struct {
		Privileged bool `json:"privileged"`
	} `json:"body"`
}

// AdminWorkspaceDetail is a workspace enriched with its owner, resource counts,
// members, and recent activity for the admin detail page.
type AdminWorkspaceDetail struct {
	models.Workspace
	OwnerName      string                   `json:"owner_name"`
	OwnerEmail     string                   `json:"owner_email"`
	AppsCount      int64                    `json:"apps_count"`
	DatabasesCount int64                    `json:"databases_count"`
	StacksCount    int64                    `json:"stacks_count"`
	VolumesCount   int64                    `json:"volumes_count"`
	NetworksCount  int64                    `json:"networks_count"`
	MembersCount   int64                    `json:"members_count"`
	Members        []models.WorkspaceMember `json:"members"`
	RecentEvents   []models.AuditLog        `json:"recent_events"`
}

// Get returns a single workspace with owner, counts, members, and recent activity.
func (h *AdminWorkspaceHandler) Get(c *okapi.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid workspace id")
	}
	ws, err := h.workspaces.FindByID(uint(id))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}

	// Empty slices marshal as [] (not null) so the web UI can read .length safely.
	detail := AdminWorkspaceDetail{
		Workspace:    *ws,
		Members:      []models.WorkspaceMember{},
		RecentEvents: []models.AuditLog{},
	}
	if owner, err := h.userByID(ws.OwnerID); err == nil {
		detail.OwnerName, detail.OwnerEmail = owner.Name, owner.Email
	}
	detail.AppsCount = h.countWhere(&models.Application{}, ws.ID)
	detail.DatabasesCount = h.countWhere(&models.DatabaseInstance{}, ws.ID)
	detail.StacksCount = h.countWhere(&models.Stack{}, ws.ID)
	detail.VolumesCount = h.countWhere(&models.Volume{}, ws.ID)
	detail.NetworksCount = h.countWhere(&models.Network{}, ws.ID)
	if members, err := h.workspaces.ListMembers(ws.ID); err == nil {
		detail.Members = members
		detail.MembersCount = int64(len(members))
	}
	var events []models.AuditLog
	if err := h.db.Where("workspace_id = ?", ws.ID).Order("created_at DESC").Limit(20).Find(&events).Error; err == nil && len(events) > 0 {
		detail.RecentEvents = events
	}
	return ok(c, detail)
}

func (h *AdminWorkspaceHandler) userByID(id uint) (*models.User, error) {
	var u models.User
	if err := h.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (h *AdminWorkspaceHandler) countWhere(model any, workspaceID uint) int64 {
	var n int64
	h.db.Model(model).Where("workspace_id = ?", workspaceID).Count(&n)
	return n
}

// List returns every workspace with its owner and resource counts.
func (h *AdminWorkspaceHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	workspaces, total, err := h.workspaces.ListPaged(c.Query("search"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list workspaces", err)
	}
	owners := h.ownerMap(workspaces)
	apps := groupCountByWorkspace(h.db, &models.Application{})
	dbs := groupCountByWorkspace(h.db, &models.DatabaseInstance{})
	stacks := groupCountByWorkspace(h.db, &models.Stack{})
	members := groupCountByWorkspace(h.db, &models.WorkspaceMember{})

	out := make([]AdminWorkspace, 0, len(workspaces))
	for _, w := range workspaces {
		o := owners[w.OwnerID]
		out = append(out, AdminWorkspace{
			ID: w.ID, Name: w.Name, DisplayName: w.DisplayName, OwnerID: w.OwnerID,
			OwnerName: o.Name, OwnerEmail: o.Email, Privileged: w.Privileged,
			AppsCount: apps[w.ID], DatabasesCount: dbs[w.ID], StacksCount: stacks[w.ID],
			MembersCount: members[w.ID], CreatedAt: w.CreatedAt,
		})
	}
	return paginated(c, out, total, page, size)
}

// SetPrivileged toggles a workspace's privileged flag (auto-approve port bindings).
func (h *AdminWorkspaceHandler) SetPrivileged(c *okapi.Context, req *SetWorkspacePrivilegedRequest) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid workspace id")
	}
	ws, err := h.workspaces.FindByID(uint(id))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	// The platform system workspace is always privileged (it runs platform-managed
	// infrastructure like the per-node gateways); refuse to disable it.
	if ws.System && !req.Body.Privileged {
		return c.AbortForbidden("the platform system workspace is always privileged")
	}
	if err := h.workspaces.SetPrivileged(ws.ID, req.Body.Privileged); err != nil {
		return c.AbortInternalServerError("failed to update workspace", err)
	}
	ws.Privileged = req.Body.Privileged
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &ws.ID, Action: "admin.workspace.set_privileged",
		TargetType: "workspace", TargetID: strconv.Itoa(int(ws.ID)), IP: c.RealIP(),
		Metadata: map[string]any{"privileged": req.Body.Privileged},
	})
	return ok(c, ws)
}

func (h *AdminWorkspaceHandler) ownerMap(workspaces []models.Workspace) map[uint]models.User {
	ids := make([]uint, 0, len(workspaces))
	for _, w := range workspaces {
		ids = append(ids, w.OwnerID)
	}
	var users []models.User
	if len(ids) > 0 {
		h.db.Where("id IN ?", ids).Find(&users)
	}
	m := make(map[uint]models.User, len(users))
	for _, u := range users {
		m[u.ID] = u
	}
	return m
}

// groupCountByWorkspace maps workspace_id -> row count for a model with a workspace_id column.
func groupCountByWorkspace(db *gorm.DB, model any) map[uint]int64 {
	var rows []struct {
		WorkspaceID uint
		N           int64
	}
	db.Model(model).Select("workspace_id, count(*) as n").Group("workspace_id").Scan(&rows)
	m := make(map[uint]int64, len(rows))
	for _, r := range rows {
		m[r.WorkspaceID] = r.N
	}
	return m
}
