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
	"errors"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
)

type GitRepositoryHandler struct {
	svc   *gitrepo.Service
	audit *audit.Logger
}

func NewGitRepositoryHandler(svc *gitrepo.Service, auditLog *audit.Logger) *GitRepositoryHandler {
	return &GitRepositoryHandler{svc: svc, audit: auditLog}
}

type CreateGitRepoRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"` // desired unique slug handle
		DisplayName string `json:"display_name"`         // free-text label (defaults to name)
		URL         string `json:"url" required:"true"`
		AuthType    string `json:"auth_type" enum:"public,token,ssh"`
		Username    string `json:"username"`
		// Secret is optional — leave blank for a public repository.
		Secret string `json:"secret"`
	} `json:"body"`
}

type UpdateGitRepoRequest struct {
	Body struct {
		// Accepted so whole-object PATCHes keep working; a different name is refused with 409, not ignored.
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		URL         string `json:"url"`
		AuthType    string `json:"auth_type" enum:"public,token,ssh"`
		Username    string `json:"username"`
		Secret      string `json:"secret"`
	} `json:"body"`
}

func (h *GitRepositoryHandler) Create(c *okapi.Context, req *CreateGitRepoRequest) error {
	wsID := middlewares.WorkspaceID(c)
	g, err := h.svc.Create(wsID, gitrepo.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, URL: req.Body.URL, AuthType: models.GitAuthType(req.Body.AuthType),
		Username: req.Body.Username, Secret: req.Body.Secret,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitrepo.create", g.ID)
	return created(c, g)
}

func (h *GitRepositoryHandler) List(c *okapi.Context) error {
	repos, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list git repositories", err)
	}
	return ok(c, repos)
}

func (h *GitRepositoryHandler) Get(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git repository id")
	}
	g, err := h.svc.Get(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("git repository not found")
	}
	return ok(c, g)
}

func (h *GitRepositoryHandler) Update(c *okapi.Context, req *UpdateGitRepoRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git repository id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.gitopsConflict(wsID, id); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	g, err := h.svc.Update(wsID, id, gitrepo.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, URL: req.Body.URL,
		AuthType: models.GitAuthType(req.Body.AuthType),
		Username: req.Body.Username, Secret: req.Body.Secret,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitrepo.update", g.ID)
	return ok(c, g)
}

func (h *GitRepositoryHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git repository id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.gitopsConflict(wsID, id); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	if err := h.svc.Delete(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "gitrepo.delete", id)
	return message(c, "git repository deleted")
}

func (h *GitRepositoryHandler) Test(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid git repository id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.TestConnection(c.Request().Context(), wsID, id); err != nil {
		if errors.Is(err, gitrepo.ErrNotFound) {
			return c.AbortNotFound("git repository not found")
		}
		// The outcome is persisted either way, so the client can refresh the row and see the reason.
		return c.AbortWithError(400, err)
	}
	g, err := h.svc.Get(wsID, id)
	if err != nil {
		return message(c, "connection succeeded")
	}
	return ok(c, g)
}

// The GitOps project's next sync would undo any edit here.
func (h *GitRepositoryHandler) gitopsConflict(wsID, id uint) error {
	g, err := h.svc.Get(wsID, id)
	if err != nil || g.Metadata[models.MetaManagedBy] != models.ManagedByGitOps {
		return nil
	}
	return errors.New("this credential is declared by a GitOps project — change or remove it in that project's repository")
}

func (h *GitRepositoryHandler) id(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("gitRepoID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid git repository id")
	}
	return uint(id), nil
}

func (h *GitRepositoryHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "git_repository", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *GitRepositoryHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, gitrepo.ErrNameTaken), errors.Is(err, gitrepo.ErrNameImmutable):
		return c.AbortWithError(409, err)
	case errors.Is(err, gitrepo.ErrNameRequired), errors.Is(err, gitrepo.ErrURLRequired),
		errors.Is(err, gitrepo.ErrSecretRequired), errors.Is(err, gitrepo.ErrUnknownSecret):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, gitrepo.ErrNotFound):
		return c.AbortNotFound("git repository not found")
	default:
		return c.AbortInternalServerError("git repository operation failed", err)
	}
}
