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

package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/apply"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/gitops"
	"github.com/miabi-io/miabi/internal/webhooksig"
)

// Teardown is nil when cascade was not requested.
type GitOpsDeleteResult struct {
	Message  string        `json:"message"`
	Teardown *apply.Result `json:"teardown,omitempty"`
}

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
		// Optional when git_repository_id is set, which supplies the URL and credentials.
		RepoURL         string `json:"repo_url"`
		Ref             string `json:"ref"`
		Path            string `json:"path"`
		GitRepositoryID *uint  `json:"git_repository_id"`
		SyncPolicy      string `json:"sync_policy" enum:"manual,auto"`
		Prune           bool   `json:"prune"`
		SelfHeal        bool   `json:"self_heal"`
		AllowEmpty      bool   `json:"allow_empty"`
		// RequireSealedSecrets refuses a sync that carries a Secret with a plaintext value.
		RequireSealedSecrets bool `json:"require_sealed_secrets"`
	} `json:"body"`
}

type UpdateGitSourceRequest struct {
	Body struct {
		RepoURL         string `json:"repo_url"`
		Ref             string `json:"ref"`
		Path            string `json:"path"`
		GitRepositoryID *uint  `json:"git_repository_id"`
		SyncPolicy      string `json:"sync_policy" enum:"manual,auto"`
		Prune           bool   `json:"prune"`
		SelfHeal        bool   `json:"self_heal"`
		AllowEmpty      bool   `json:"allow_empty"`
		// RequireSealedSecrets refuses a sync that carries a Secret with a plaintext value.
		RequireSealedSecrets bool `json:"require_sealed_secrets"`
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
		RequireSealedSecrets: req.Body.RequireSealedSecrets,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitops.create", src.ID)
	h.svc.SyncInBackground(src.ID)
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
	if err := h.nestedConflict(wsID, id); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	src, err := h.svc.Update(wsID, id, gitops.Input{
		RepoURL: req.Body.RepoURL, Ref: req.Body.Ref, Path: req.Body.Path,
		GitRepositoryID: req.Body.GitRepositoryID, SyncPolicy: models.GitSyncPolicy(req.Body.SyncPolicy),
		Prune: req.Body.Prune, SelfHeal: req.Body.SelfHeal, AllowEmpty: req.Body.AllowEmpty,
		RequireSealedSecrets: req.Body.RequireSealedSecrets,
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
	if err := h.nestedConflict(wsID, id); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	// Default keeps the project's resources running.
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
	return ok(c, GitOpsDeleteResult{Message: msg, Teardown: teardown})
}

// The parent project's next sync would undo any edit here.
func (h *GitOpsHandler) nestedConflict(wsID, id uint) error {
	src, err := h.svc.Get(wsID, id)
	if err != nil || src.Metadata[models.MetaManagedBy] != models.ManagedByGitOps {
		return nil
	}
	parent := "another GitOps project"
	if pid, perr := strconv.ParseUint(src.Metadata[models.MetaGitOpsSource], 10, 64); perr == nil {
		if p, gerr := h.svc.Get(wsID, uint(pid)); gerr == nil {
			parent = fmt.Sprintf("the GitOps project %q", p.Name)
		}
	}
	return fmt.Errorf("this project is declared by %s — change or remove it in that project's repository", parent)
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

// Under auto-sync the resource is recreated on the next reconcile.
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

// Cheap (no git clone): the detail page polls it.
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

// Unauthenticated; the request is verified against the source's webhook secret.
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
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, webhooksig.MaxBodyBytes))
	if err != nil {
		return c.AbortWithError(http.StatusRequestEntityTooLarge, err)
	}
	sig := c.Header(webhooksig.SignatureHeader)
	if sig == "" {
		sig = c.Header(webhooksig.TokenHeader)
	}
	if !h.svc.VerifyWebhook(src, sig, body) {
		return c.AbortUnauthorized("invalid webhook signature")
	}
	// The request context is cancelled when the handler returns, which would kill the sync.
	ctx := context.WithoutCancel(c.Request().Context())
	go func() { _ = h.svc.SyncByID(ctx, src.ID) }()
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
