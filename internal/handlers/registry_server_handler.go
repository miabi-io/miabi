// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// RegistryServerHandler serves the internal forwardAuth endpoint (called by the
// gateway on every /v2/* request) and the per-workspace registry info.
type RegistryServerHandler struct {
	svc        *registryserver.Service
	workspaces *repositories.WorkspaceRepository
}

func NewRegistryServerHandler(svc *registryserver.Service, workspaces *repositories.WorkspaceRepository) *RegistryServerHandler {
	return &RegistryServerHandler{svc: svc, workspaces: workspaces}
}

// Authenticate is the Goma forwardAuth target: it authorizes a forwarded
// registry request from the docker Basic credentials, the original method, and
// the requested repository. 2xx = allow; 401 challenges the docker client with
// WWW-Authenticate; 403 denies (cross-tenant / scope). It is unauthenticated by
// design — only the gateway can reach it — and carries no Miabi session.
func (h *RegistryServerHandler) Authenticate(c *okapi.Context) error {
	res := h.svc.Authorize(registryserver.AuthInput{
		Authorization: c.Header("Authorization"),
		Method:        firstNonEmpty(c.Header("X-Forwarded-Method"), c.Request().Method),
		URI:           firstNonEmpty(c.Header("X-Forwarded-Uri"), c.Request().URL.RequestURI()),
	})
	hasAuth := c.Header("Authorization") != ""
	switch {
	case res.Status == http.StatusForbidden:
		// A 403 (cross-tenant / scope / unknown namespace) is a real
		// misconfiguration the operator needs to see — the reason never reaches the
		// docker client (the gateway returns a bare 403), so surface it here.
		logger.Warn("registry auth denied",
			"fwd_method", c.Header("X-Forwarded-Method"),
			"fwd_uri", c.Header("X-Forwarded-Uri"),
			"has_authorization", hasAuth,
			"reason", res.Reason,
		)
	case res.Status == http.StatusUnauthorized && hasAuth:
		// A 401 WITH credentials is a rejected token (invalid/expired) — a real
		// failure, distinct from the first-request challenge (no creds) that docker
		// always triggers. Surface it so a bad/missing runner token is visible.
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
		// The gateway copies these from the forwardAuth response and rewrites the
		// repository namespace segment to X-Miabi-Registry-Namespace (ws_<id>)
		// before forwarding to the registry — so storage keys off the immutable id
		// while users keep addressing by the workspace name (rename-safe). The
		// others are echoed to the registry backend for its logs.
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
	// DeleteEnabled reports the platform's registry delete switch. Without it a
	// client would offer a delete action that always fails — the role check alone
	// isn't enough to know whether deletion is possible.
	DeleteEnabled bool `json:"delete_enabled"`
}

// Info returns the registry connection details for the scoped workspace, so the
// UI can render the docker login/tag/push snippet. Membership is enforced by
// WorkspaceScope.
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

// Repositories lists a page of the workspace's registry repositories, each with
// its tag count and a preview of its newest tags. Query: page, size, q (filters
// on repository name), tag_limit. Membership enforced by WorkspaceScope.
//
// Only the page's repositories have their tags read from the registry, so the
// cost of this call is bounded by the page size rather than by how many images
// the workspace has.
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

// repoParam reads the repository name from the ?name= query parameter.
//
// It is a query parameter rather than a path segment because an image name may
// itself contain slashes (someone pushing "team/api"), which a path segment
// cannot carry unambiguously.
func repoParam(c *okapi.Context) (string, bool) {
	name := strings.TrimSpace(c.Query("name"))
	return name, name != ""
}

// RepositoryOverview returns a single repository's summary: tag count, a preview
// of the newest tags, and the newest tag with its digest, size, and provenance.
func (h *RegistryServerHandler) RepositoryOverview(c *okapi.Context) error {
	name, okName := repoParam(c)
	if !okName {
		return c.AbortBadRequest("a repository name is required")
	}
	ov, err := h.svc.Overview(c.Request().Context(), middlewares.WorkspaceID(c), name)
	if err != nil {
		if errors.Is(err, registryserver.ErrNotFound) {
			return c.AbortNotFound("repository not found")
		}
		return c.AbortInternalServerError("failed to load the repository", err)
	}
	return ok(c, ov)
}

// RepositoryTags lists a page of a repository's tags in display order, enriched
// with digest, size, in-use state, and build provenance. Query: name, page,
// size, q (filters on tag name).
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
		if errors.Is(err, registryserver.ErrNotFound) {
			return c.AbortNotFound("repository not found")
		}
		return c.AbortInternalServerError("failed to list tags", err)
	}
	return paginated(c, tags, int64(total), page, size)
}

// DeleteTag deletes a tag from a workspace repository (developer+; resolves the
// tag to its manifest digest and deletes the manifest).
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
	case errors.Is(err, registryserver.ErrNotFound):
		return c.AbortNotFound("tag not found")
	default:
		return c.AbortInternalServerError("failed to delete tag", err)
	}
}

// firstNonEmpty returns the first non-empty string.
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
