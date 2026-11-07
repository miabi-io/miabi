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
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/registryserver"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type RegistryServerHandler struct {
	svc        *registryserver.Service
	workspaces *repositories.WorkspaceRepository
}

func NewRegistryServerHandler(svc *registryserver.Service, workspaces *repositories.WorkspaceRepository) *RegistryServerHandler {
	return &RegistryServerHandler{svc: svc, workspaces: workspaces}
}

// 2xx allows, 401 challenges, 403 denies; unauthenticated by design since only the gateway can reach it.
func (h *RegistryServerHandler) Authenticate(c *okapi.Context) error {
	res := h.svc.Authorize(registryserver.AuthInput{
		Authorization: c.Header("Authorization"),
		Method:        firstNonEmpty(c.Header("X-Forwarded-Method"), c.Request().Method),
		URI:           firstNonEmpty(c.Header("X-Forwarded-Uri"), c.Request().URL.RequestURI()),
	})
	hasAuth := c.Header("Authorization") != ""
	switch {
	case res.Status == http.StatusForbidden:
		// The gateway returns a bare 403 to docker, so the reason is only visible in this log.
		logger.Warn("registry auth denied",
			"fwd_method", c.Header("X-Forwarded-Method"),
			"fwd_uri", c.Header("X-Forwarded-Uri"),
			"has_authorization", hasAuth,
			"reason", res.Reason,
		)
	case res.Status == http.StatusUnauthorized && hasAuth:
		// A 401 with credentials is a rejected token, unlike docker's credential-less first challenge.
		logger.Warn("registry auth rejected credentials",
			"fwd_method", c.Header("X-Forwarded-Method"),
			"fwd_uri", c.Header("X-Forwarded-Uri"),
			"reason", res.Reason,
		)
	default:
		logger.Debug("registry auth",
			"fwd_method", c.Header("X-Forwarded-Method"),
			"fwd_uri", c.Header("X-Forwarded-Uri"),
			"has_authorization", hasAuth,
			"status", res.Status,
			"reason", res.Reason,
		)
	}
	if res.Challenge {
		c.SetHeader("WWW-Authenticate", registryserver.Realm)
	}
	if res.Allowed() {
		// The gateway rewrites the namespace to ws_<id>, so storage keys off the immutable id (rename-safe).
		c.SetHeader("X-Miabi-Registry-Namespace", res.Namespace)
		c.SetHeader("X-Miabi-Workspace", res.Workspace)
		c.SetHeader("X-Miabi-Workspace-Id", strconv.FormatUint(uint64(res.WorkspaceID), 10))
		c.SetHeader("X-Miabi-User-Id", strconv.FormatUint(uint64(res.UserID), 10))
	}
	switch res.Status {
	case http.StatusOK:
		return c.String(http.StatusOK, "ok")
	case http.StatusUnauthorized:
		return c.AbortUnauthorized(orDefault(res.Reason, "authentication required"))
	default:
		return c.AbortForbidden(orDefault(res.Reason, "forbidden"))
	}
}

// RegistryInfo is the docker-login / push guidance for a workspace.
type RegistryInfo struct {
	Enabled      bool   `json:"enabled"`
	Host         string `json:"host"`
	Namespace    string `json:"namespace"`     // the workspace name
	ImagePrefix  string `json:"image_prefix"`  // <host>/<namespace>
	LoginExample string `json:"login_example"` // docker login snippet
	// The role check alone isn't enough to know whether deletion is possible.
	DeleteEnabled bool `json:"delete_enabled"`
}

// Membership is enforced by WorkspaceScope.
func (h *RegistryServerHandler) Info(c *okapi.Context) error {
	ws, err := h.workspaces.FindByID(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	st, err := h.svc.Get()
	if err != nil {
		return c.AbortInternalServerError("failed to load registry settings", err)
	}
	host := h.svc.HostFor(st)
	info := RegistryInfo{
		Enabled:       st.Enabled && host != "",
		Host:          host,
		Namespace:     ws.Name,
		DeleteEnabled: st.DeleteEnabled,
	}
	if host != "" {
		info.ImagePrefix = fmt.Sprintf("%s/%s", host, ws.Name)
		info.LoginExample = fmt.Sprintf("docker login %s -u %s -p <api-token>", host, ws.Name)
	}
	return ok(c, info)
}

// Only the page's repositories have tags read, so cost is bounded by page size.
func (h *RegistryServerHandler) Repositories(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	repos, total, err := h.svc.ListRepositoriesPage(
		c.Request().Context(), middlewares.WorkspaceID(c),
		c.Query("q"), offset, size, queryInt(c, "tag_limit", registryserver.DefaultTagPreview),
	)
	if err != nil {
		return c.AbortInternalServerError("failed to list repositories", err)
	}
	return paginated(c, repos, int64(total), page, size)
}

// A query param because an image name may contain slashes ("team/api").
func repoParam(c *okapi.Context) (string, bool) {
	name := strings.TrimSpace(c.Query("name"))
	return name, name != ""
}

func (h *RegistryServerHandler) RepositoryOverview(c *okapi.Context) error {
	name, okName := repoParam(c)
	if !okName {
		return c.AbortBadRequest("a repository name is required")
	}
	ov, err := h.svc.Overview(c.Request().Context(), middlewares.WorkspaceID(c), name)
	if err != nil {
		if errors.Is(err, registryserver.ErrInvalidRepository) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, registryserver.ErrNotFound) {
			return c.AbortNotFound("repository not found")
		}
		return c.AbortInternalServerError("failed to load the repository", err)
	}
	return ok(c, ov)
}

func (h *RegistryServerHandler) RepositoryTags(c *okapi.Context) error {
	name, okName := repoParam(c)
	if !okName {
		return c.AbortBadRequest("a repository name is required")
	}
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	tags, total, err := h.svc.ListTagsPage(
		c.Request().Context(), middlewares.WorkspaceID(c), name, c.Query("q"), offset, size,
	)
	if err != nil {
		if errors.Is(err, registryserver.ErrInvalidRepository) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, registryserver.ErrNotFound) {
			return c.AbortNotFound("repository not found")
		}
		return c.AbortInternalServerError("failed to list tags", err)
	}
	return paginated(c, tags, int64(total), page, size)
}

func (h *RegistryServerHandler) DeleteTag(c *okapi.Context) error {
	image := c.Param("repo")
	tag := c.Param("tag")
	if image == "" || tag == "" {
		return c.AbortBadRequest("repository and tag are required")
	}
	err := h.svc.DeleteTag(c.Request().Context(), middlewares.WorkspaceID(c), image, tag)
	switch {
	case err == nil:
		return message(c, "tag deleted")
	case errors.Is(err, registryserver.ErrDeleteDisabled), errors.Is(err, registryserver.ErrTagInUse):
		return c.AbortWithError(http.StatusConflict, err)
	case errors.Is(err, registryserver.ErrInvalidRepository), errors.Is(err, registryserver.ErrInvalidTag):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, registryserver.ErrNotFound):
		return c.AbortNotFound("tag not found")
	default:
		return c.AbortInternalServerError("failed to delete tag", err)
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
