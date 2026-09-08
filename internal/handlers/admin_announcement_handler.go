// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/announcement"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// AdminAnnouncementHandler lets a platform administrator put a notice in the
// inboxes of the users it concerns — planned maintenance, a breaking upgrade, a
// policy change (Enterprise; gated announcements). It is the platform speaking,
// as opposed to the alerts Miabi derives from the event stream.
type AdminAnnouncementHandler struct {
	svc   *announcement.Service
	repo  *repositories.AnnouncementRepository
	users *repositories.UserRepository
	ee    enterprise.EE
	audit *audit.Logger
}

func NewAdminAnnouncementHandler(svc *announcement.Service, repo *repositories.AnnouncementRepository, users *repositories.UserRepository, ee enterprise.EE, auditLog *audit.Logger) *AdminAnnouncementHandler {
	return &AdminAnnouncementHandler{svc: svc, repo: repo, users: users, ee: ee, audit: auditLog}
}

// announcementBody is the shared create/update payload. It is named Message
// rather than Body because okapi resolves a request's body by looking for a field
// named Body, and applies that rule again to the body's own fields.
type announcementBody struct {
	Title        string `json:"title" required:"true" max:"200"`
	Message      string `json:"message" max:"4000"`
	Link         string `json:"link" max:"500"`
	ActionText   string `json:"action_text" max:"60"`
	Severity     string `json:"severity" enum:"info,warning,critical"`
	Audience     string `json:"audience" enum:"all,admins,owners,workspaces"`
	WorkspaceIDs []uint `json:"workspace_ids"`
	Pinned       bool   `json:"pinned"`
	Dismissal    string `json:"dismissal" enum:"once,never"`
	PublishAt    string `json:"publish_at"` // RFC3339; empty broadcasts immediately
	ExpiresAt    string `json:"expires_at"` // RFC3339; empty never expires
}

type CreateAnnouncementRequest struct {
	Body announcementBody `json:"body"`
}

type UpdateAnnouncementRequest struct {
	Body announcementBody `json:"body"`
}

type ListAnnouncementsRequest struct {
	Page int `query:"page" default:"0"`
	Size int `query:"size" default:"20"`
}

// AudiencePreviewRequest asks how many users an audience currently resolves to,
// so an operator sees the blast radius before broadcasting rather than after.
type AudiencePreviewRequest struct {
	Audience     string `query:"audience" enum:"all,admins,owners,workspaces"`
	WorkspaceIDs string `query:"workspaces"` // comma-separated ids
}

// AudiencePreview is the resolved size of an audience.
type AudiencePreview struct {
	Recipients int `json:"recipients"`
}

func (h *AdminAnnouncementHandler) List(c *okapi.Context, req *ListAnnouncementsRequest) error {
	if err := h.ee.Require(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	page, size, offset := normalizePageParams(req.Page, req.Size)
	rows, total, err := h.repo.List(size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list announcements", err)
	}
	now := time.Now().UTC()
	for i := range rows {
		rows[i].Status = rows[i].State(now)
	}
	return paginated(c, rows, total, page, size)
}

// Preview resolves an audience to a recipient count without sending anything.
func (h *AdminAnnouncementHandler) Preview(c *okapi.Context, req *AudiencePreviewRequest) error {
	if err := h.ee.Require(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	a := &models.Announcement{
		Audience:     models.AnnouncementAudience(orStr(req.Audience, string(models.AudienceAll))),
		WorkspaceIDs: parseIDList(req.WorkspaceIDs),
	}
	if !a.Audience.Valid() {
		return c.AbortBadRequest("unknown audience")
	}
	n, err := h.repo.CountRecipients(a)
	if err != nil {
		return c.AbortInternalServerError("failed to resolve the audience", err)
	}
	return ok(c, AudiencePreview{Recipients: n})
}

// Create broadcasts the announcement, or schedules it when publish_at is in the
// future. There is no draft state: an announcement nobody will receive is a note,
// and Retract covers the case where one went out wrong.
func (h *AdminAnnouncementHandler) Create(c *okapi.Context, req *CreateAnnouncementRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	a := &models.Announcement{
		CreatedBy:  middlewares.UserID(c),
		AuthorName: h.authorName(c),
	}
	if err := h.apply(c, a, req.Body); err != nil {
		return err
	}
	if err := h.svc.Create(a); err != nil {
		return c.AbortInternalServerError("failed to broadcast the announcement", err)
	}
	h.record(c, "admin.announcement.sent", a)
	return created(c, a)
}

// Update rewrites the announcement and every inbox it already reached.
func (h *AdminAnnouncementHandler) Update(c *okapi.Context, req *UpdateAnnouncementRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	a, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("announcement not found")
	}
	if err := h.apply(c, a, req.Body); err != nil {
		return err
	}
	if err := h.svc.Update(a); err != nil {
		return c.AbortInternalServerError("failed to update the announcement", err)
	}
	h.record(c, "admin.announcement.updated", a)
	return ok(c, a)
}

// Publish sends a scheduled announcement ahead of its time.
func (h *AdminAnnouncementHandler) Publish(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	a, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("announcement not found")
	}
	if a.PublishedAt != nil {
		return c.AbortBadRequest("this announcement has already been broadcast")
	}
	if err := h.svc.PublishNow(a); err != nil {
		return c.AbortInternalServerError("failed to broadcast the announcement", err)
	}
	h.record(c, "admin.announcement.sent", a)
	return ok(c, a)
}

// Retract deletes the announcement and every delivery it made. Retraction stays
// available on a degraded license: taking back a wrong notice is not a new
// configuration, and blocking it would leave the mistake in every inbox.
func (h *AdminAnnouncementHandler) Retract(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAnnouncements); err != nil {
		return entitlementAbort(c, err)
	}
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	a, err := h.repo.FindByID(id)
	if err != nil {
		return c.AbortNotFound("announcement not found")
	}
	if err := h.svc.Retract(a.ID); err != nil {
		return c.AbortInternalServerError("failed to retract the announcement", err)
	}
	h.record(c, "admin.announcement.retracted", a)
	return ok(c, map[string]string{"message": "announcement retracted"})
}

// apply validates the payload onto the announcement, leaving persistence to the
// service.
func (h *AdminAnnouncementHandler) apply(c *okapi.Context, a *models.Announcement, b announcementBody) error {
	title := strings.TrimSpace(b.Title)
	if title == "" {
		return c.AbortBadRequest("a title is required")
	}
	audience := models.AnnouncementAudience(orStr(b.Audience, string(models.AudienceAll)))
	if !audience.Valid() {
		return c.AbortBadRequest("unknown audience")
	}
	if audience == models.AudienceWorkspaces && len(b.WorkspaceIDs) == 0 {
		return c.AbortBadRequest("select at least one workspace for this audience")
	}
	publishAt, err := parseOptionalTime(b.PublishAt)
	if err != nil {
		return c.AbortBadRequest("publish_at must be an RFC3339 timestamp")
	}
	expiresAt, err := parseOptionalTime(b.ExpiresAt)
	if err != nil {
		return c.AbortBadRequest("expires_at must be an RFC3339 timestamp")
	}
	if publishAt != nil && expiresAt != nil && !expiresAt.After(*publishAt) {
		return c.AbortBadRequest("expires_at must be after publish_at")
	}
	dismissal := models.AnnouncementDismissal(orStr(b.Dismissal, string(models.DismissOnce)))
	if !dismissal.Valid() {
		return c.AbortBadRequest("unknown dismissal mode")
	}
	// An unpinned notice rests in the bell, where there is nothing to withhold.
	if !b.Pinned {
		dismissal = models.DismissOnce
	}

	a.Title = title
	a.Message = strings.TrimSpace(b.Message)
	a.Link = strings.TrimSpace(b.Link)
	a.ActionText = strings.TrimSpace(b.ActionText)
	a.Severity = models.AlertSeverity(orStr(b.Severity, string(models.AlertInfo)))
	a.Audience = audience
	a.WorkspaceIDs = nil
	if audience == models.AudienceWorkspaces {
		a.WorkspaceIDs = b.WorkspaceIDs
	}
	a.Pinned = b.Pinned
	a.Dismissal = dismissal
	a.ExpiresAt = expiresAt
	// A broadcast that has gone out cannot be rescheduled; only its expiry moves.
	if a.PublishedAt == nil {
		a.PublishAt = publishAt
	}
	return nil
}

func (h *AdminAnnouncementHandler) authorName(c *okapi.Context) string {
	if h.users == nil {
		return ""
	}
	u, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil || u == nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

func (h *AdminAnnouncementHandler) record(c *okapi.Context, action string, a *models.Announcement) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: action, TargetType: "announcement",
		TargetID: strconv.Itoa(int(a.ID)), IP: c.RealIP(),
		Metadata: map[string]any{
			"title":      a.Title,
			"audience":   string(a.Audience),
			"severity":   string(a.Severity),
			"pinned":     a.Pinned,
			"dismissal":  string(a.Dismissal),
			"recipients": a.Recipients,
		},
	})
}

// parseOptionalTime reads an optional RFC3339 timestamp; an empty string means unset.
func parseOptionalTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, err
	}
	utc := t.UTC()
	return &utc, nil
}

// parseIDList reads a comma-separated id list from a query parameter.
func parseIDList(v string) []uint {
	var out []uint
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.ParseUint(part, 10, 64); err == nil && n > 0 {
			out = append(out, uint(n))
		}
	}
	return out
}
