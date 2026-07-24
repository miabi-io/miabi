// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"io"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/gitops"
)

// GitOpsDeleteResult is the response to a project delete. Teardown carries the
// resources removed by a cascade delete (nil when cascade was not requested).
type GitOpsDeleteResult struct {
	Message  string        `json:"message"`
	Teardown *apply.Result `json:"teardown,omitempty"`
}

// GitOpsHandler exposes GitSource CRUD, sync, diff, and the inbound push
// webhook.
type GitOpsHandler struct {
	svc   *gitops.Service
	audit *audit.Logger
}

func NewGitOpsHandler(svc *gitops.Service, auditLog *audit.Logger) *GitOpsHandler {
	return &GitOpsHandler{svc: svc, audit: auditLog}
}

type CreateGitSourceRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"` // desired unique slug handle
		DisplayName string `json:"display_name"`         // free-text label (defaults to name)
		// RepoURL is optional when git_repository_id is set — the URL is taken
		// from the selected git repository (which also supplies credentials).
		RepoURL         string `json:"repo_url"`
		Ref             string `json:"ref"`
		Path            string `json:"path"`
		GitRepositoryID *uint  `json:"git_repository_id"`
		SyncPolicy      string `json:"sync_policy" enum:"manual,auto"`
		Prune           bool   `json:"prune"`
		SelfHeal        bool   `json:"self_heal"`
		AllowEmpty      bool   `json:"allow_empty"`
	} `json:"body"`
}

type UpdateGitSourceRequest struct {
	Body struct {
		Name            string `json:"name"`
		RepoURL         string `json:"repo_url"`
		Ref             string `json:"ref"`
		Path            string `json:"path"`
		GitRepositoryID *uint  `json:"git_repository_id"`
		SyncPolicy      string `json:"sync_policy" enum:"manual,auto"`
		Prune           bool   `json:"prune"`
		SelfHeal        bool   `json:"self_heal"`
		AllowEmpty      bool   `json:"allow_empty"`
	} `json:"body"`
}

func (h *GitOpsHandler) List(c *okapi.Context) error {
	out, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list git sources", err)
	}
	return ok(c, out)
}

func (h *GitOpsHandler) Create(c *okapi.Context, req *CreateGitSourceRequest) error {
	wsID := middlewares.WorkspaceID(c)
	src, err := h.svc.Create(wsID, gitops.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, RepoURL: req.Body.RepoURL, Ref: req.Body.Ref, Path: req.Body.Path,
		GitRepositoryID: req.Body.GitRepositoryID, SyncPolicy: models.GitSyncPolicy(req.Body.SyncPolicy),
		Prune: req.Body.Prune, SelfHeal: req.Body.SelfHeal, AllowEmpty: req.Body.AllowEmpty,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.create", src.ID)
	return created(c, src)
}

func (h *GitOpsHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	src, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("git source not found")
	}
	return ok(c, src)
}

func (h *GitOpsHandler) Update(c *okapi.Context, req *UpdateGitSourceRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	wsID := middlewares.WorkspaceID(c)
	src, err := h.svc.Update(wsID, id, gitops.Input{
		Name: req.Body.Name, RepoURL: req.Body.RepoURL, Ref: req.Body.Ref, Path: req.Body.Path,
		GitRepositoryID: req.Body.GitRepositoryID, SyncPolicy: models.GitSyncPolicy(req.Body.SyncPolicy),
		Prune: req.Body.Prune, SelfHeal: req.Body.SelfHeal, AllowEmpty: req.Body.AllowEmpty,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.update", src.ID)
	return ok(c, src)
}

func (h *GitOpsHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	wsID := middlewares.WorkspaceID(c)
	// Opt-in cascade: ?cascade=true also tears down the resources this project
	// created. Default keeps them running.
	cascade := c.Query("cascade") == "true"
	teardown, err := h.svc.Delete(c.Request().Context(), wsID, id, cascade)
	if err != nil {
		return h.mapErr(c, err)
	}
	action := "gitops.delete"
	msg := "git source deleted"
	if cascade {
		action = "gitops.delete_cascade"
		msg = "git source and its resources deleted"
	}
	h.record(c, wsID, action, id)
	// Return the teardown result so the UI can show which resources were removed
	// (and any that failed). teardown is nil for a non-cascade delete.
	return ok(c, GitOpsDeleteResult{Message: msg, Teardown: teardown})
}

// Sync reconciles a source now.
func (h *GitOpsHandler) Sync(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	wsID := middlewares.WorkspaceID(c)
	src, err := h.svc.Sync(c.Request().Context(), wsID, id)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.sync", id)
	return ok(c, src)
}

// SyncResource reconciles a single resource of the source ("sync this resource"),
// leaving the rest of the project untouched. The path names the resource by kind
// and name (e.g. .../resources/Application/guestbook/sync).
func (h *GitOpsHandler) SyncResource(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	kind, name := c.Param("kind"), c.Param("name")
	if kind == "" || name == "" {
		return c.AbortBadRequest("resource kind and name are required")
	}
	wsID := middlewares.WorkspaceID(c)
	res, err := h.svc.SyncResource(c.Request().Context(), wsID, id, kind, name)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.sync_resource", id)
	return ok(c, res)
}

// DeleteResource deletes a single live resource of the source ("delete this
// resource"). Under auto-sync it is recreated on the next reconcile — the UI warns
// the user before calling this.
func (h *GitOpsHandler) DeleteResource(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	kind, name := c.Param("kind"), c.Param("name")
	if kind == "" || name == "" {
		return c.AbortBadRequest("resource kind and name are required")
	}
	wsID := middlewares.WorkspaceID(c)
	res, err := h.svc.DeleteResource(c.Request().Context(), wsID, id, kind, name)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.delete_resource", id)
	return ok(c, res)
}

// Diff returns the desired-vs-live plan for the source.
func (h *GitOpsHandler) Diff(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	plan, err := h.svc.Diff(c.Request().Context(), middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, plan)
}

// Topology returns the resource graph (nodes + dependency edges) for the source,
// powering the project-detail topology view.
func (h *GitOpsHandler) Topology(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	topo, err := h.svc.Topology(c.Request().Context(), middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, topo)
}

// Status returns the live runtime status of the source's managed resources,
// keyed by topology node key. Cheap (no git clone) — the detail page polls it.
func (h *GitOpsHandler) Status(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	st, err := h.svc.Status(middlewares.WorkspaceID(c), id)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, map[string]any{"statuses": st})
}

// Webhook handles an inbound provider push. It is unauthenticated; the request
// is verified against the source's webhook secret.
func (h *GitOpsHandler) Webhook(c *okapi.Context) error {
	wsID, err := uintParam(c, "workspace")
	if err != nil {
		return c.AbortBadRequest("invalid workspace id")
	}
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git source id")
	}
	src, err := h.svc.Get(wsID, id)
	if err != nil {
		return c.AbortNotFound("git source not found")
	}
	body, _ := io.ReadAll(c.Request().Body)
	sig := c.Header("X-Hub-Signature-256")
	if sig == "" {
		sig = c.Header("X-Gitlab-Token")
	}
	if !h.svc.VerifyWebhook(src, sig, body) {
		return c.AbortUnauthorized("invalid webhook signature")
	}
	go func() { _ = h.svc.SyncByID(c.Request().Context(), src.ID) }()
	return message(c, "sync triggered")
}

func (h *GitOpsHandler) id(c *okapi.Context) (uint, error) {
	return resolveID(c.Param("gitSourceID"), h.svc.IDByUID)
}

func (h *GitOpsHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "git_source", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *GitOpsHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, gitops.ErrNotFound):
		return c.AbortNotFound("git source not found")
	case errors.Is(err, gitops.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, gitops.ErrNameRequired), errors.Is(err, gitops.ErrURLRequired), errors.Is(err, gitops.ErrRepoNotFound):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, apply.ErrResourceNotFound):
		return c.AbortNotFound(err.Error())
	default:
		return c.AbortInternalServerError("git source operation failed", err)
	}
}
