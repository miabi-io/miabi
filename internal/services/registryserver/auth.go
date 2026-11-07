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

package registryserver

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Realm is advertised in the WWW-Authenticate challenge on the login handshake.
const Realm = `Basic realm="Miabi Registry"`

// Namespace is the immutable storage prefix the gateway rewrites to, so images survive a workspace rename.
func Namespace(workspaceID uint) string { return fmt.Sprintf("ws_%d", workspaceID) }

// AuthInput is the forwarded registry request context (from Goma forwardAuth).
type AuthInput struct {
	Authorization string // raw Authorization header
	Method        string // X-Forwarded-Method (else the request method)
	URI           string // X-Forwarded-Uri (the /v2/... path)
}

// On allow the gateway rewrites the namespace segment to ws_<id>, so storage keys off the immutable id.
type AuthResult struct {
	Status      int    // 200 allow, 401 unauthenticated, 403 forbidden
	Challenge   bool   // emit WWW-Authenticate: Basic on a 401
	Workspace   string // workspace name (docker username) on allow
	WorkspaceID uint   // workspace id on allow
	UserID      uint   // token owner's user id on allow
	Namespace   string // ws_<id> — the namespace the gateway rewrites to
	Reason      string // human reason (denials)
}

// Allowed reports whether the request is authorized.
func (r AuthResult) Allowed() bool { return r.Status == http.StatusOK }

// Authorize treats the docker password as an API token; the first repo segment names the target workspace.
func (s *Service) Authorize(in AuthInput) AuthResult {
	_, token, ok := parseBasic(in.Authorization)
	if !ok {
		// No/blank credentials → challenge so docker login presents Basic auth.
		return AuthResult{Status: http.StatusUnauthorized, Challenge: true, Reason: "authentication required"}
	}

	repo, isBase, isCatalog := parseRepo(in.URI)

	// The platform worker token may act on any namespace; it never crosses a tenant boundary the user controls.
	if pt := s.platformToken(); pt != "" && subtleEqual(token, pt) {
		if isBase {
			return AuthResult{Status: http.StatusOK}
		}
		if isCatalog {
			return AuthResult{Status: http.StatusForbidden, Reason: "catalog access is not permitted"}
		}
		ws, err := s.resolveNamespace(firstSegment(repo))
		if err != nil {
			return AuthResult{Status: http.StatusForbidden, Reason: "unknown workspace namespace"}
		}
		if reason := s.authorizeMountSource(in.URI, ws.ID); reason != "" {
			return AuthResult{Status: http.StatusForbidden, Reason: reason}
		}
		return AuthResult{Status: http.StatusOK, Workspace: ws.Name, WorkspaceID: ws.ID, Namespace: Namespace(ws.ID)}
	}

	key, err := s.keys.Verify(token)
	if err != nil {
		return AuthResult{Status: http.StatusUnauthorized, Challenge: true, Reason: "invalid token"}
	}
	// The login handshake hits /v2/ with no repo, so any valid token is accepted.
	if isBase {
		return AuthResult{Status: http.StatusOK, UserID: key.UserID}
	}
	// Raw /v2/_catalog would expose every tenant's repositories; the UI lists via a namespace-filtered path.
	if isCatalog {
		return AuthResult{Status: http.StatusForbidden, Reason: "catalog access is not permitted"}
	}

	// Handles both the typed name and the rewritten ws_<id> upload Location, so no response rewriting is needed.
	ws, err := s.resolveNamespace(firstSegment(repo))
	if err != nil {
		return AuthResult{Status: http.StatusForbidden, Reason: "unknown workspace namespace"}
	}

	push := isPush(in.Method)
	if reason := s.authorizePrincipal(key, ws.ID, repo, push); reason != "" {
		return AuthResult{Status: http.StatusForbidden, Reason: reason}
	}
	if reason := s.authorizeMountSource(in.URI, ws.ID); reason != "" {
		return AuthResult{Status: http.StatusForbidden, Reason: reason}
	}
	// Soft per-workspace storage quota (push only; non-blocking, cached).
	if push && s.quotaExceeded(ws.ID) {
		return AuthResult{Status: http.StatusForbidden, Reason: "workspace registry storage quota exceeded"}
	}
	return AuthResult{
		Status:      http.StatusOK,
		Workspace:   ws.Name,
		WorkspaceID: ws.ID,
		UserID:      key.UserID,
		Namespace:   Namespace(ws.ID),
	}
}

// The mount source is in the query string, untouched by parseRepo and the gateway rewrite, so check it here.
func (s *Service) authorizeMountSource(uri string, workspaceID uint) string {
	from := mountSource(uri)
	if from == "" {
		return ""
	}
	// "tenant-a/../tenant-b" must not read as tenant-a here while naming tenant-b to the registry.
	from = path.Clean(from)
	if from == ".." || strings.HasPrefix(from, "../") || strings.HasPrefix(from, "/") {
		return "invalid blob mount source"
	}
	src, err := s.resolveNamespace(firstSegment(from))
	if err != nil {
		return "unknown blob mount source namespace"
	}
	if src.ID != workspaceID {
		return "cross-workspace blob mount is not permitted"
	}
	return ""
}

func mountSource(uri string) string {
	i := strings.IndexByte(uri, '?')
	if i < 0 {
		return ""
	}
	q, err := url.ParseQuery(uri[i+1:])
	if err != nil {
		// The registry may parse a malformed query more leniently, so an unreadable mount source is refused.
		if strings.Contains(uri[i+1:], "from=") {
			return "\x00" // never resolves to a namespace → denied
		}
		return ""
	}
	return strings.Trim(strings.TrimSpace(q.Get("from")), "/")
}

func (s *Service) authorizePrincipal(key *models.APIKey, workspaceID uint, repo string, push bool) string {
	if !scopeAllows(key, push) {
		if push {
			return "push requires a write or deploy scope"
		}
		return "pull requires a read scope"
	}
	if reason := s.authorizeBoundApp(key, repo); reason != "" {
		return reason
	}
	if key.WorkspaceID != nil && *key.WorkspaceID != workspaceID {
		return "token is scoped to a different workspace"
	}
	// Machine-minted job credentials are app-bound and short-lived; their subject may be no user.
	if key.Ephemeral && key.WorkspaceID != nil {
		return ""
	}
	// Authorized by current membership, so removing someone from a workspace cuts off their tokens there.
	member, err := s.ws.FindMember(workspaceID, key.UserID)
	if err != nil {
		return "you are not a member of this workspace"
	}
	if !roleAllows(member.Role, push) {
		return "your role does not permit pushing to this workspace"
	}
	return ""
}

func (s *Service) authorizeBoundApp(key *models.APIKey, repo string) string {
	if key.ApplicationID == nil {
		return ""
	}
	if s.apps == nil {
		return "this credential is limited to one application, which could not be resolved"
	}
	app, err := s.apps.FindByID(*key.ApplicationID)
	if err != nil {
		return "this credential is limited to one application, which could not be resolved"
	}
	// repo is "<namespace>/<app>"; the segment after the namespace is the application's name.
	name := strings.TrimSpace(repoTail(repo))
	if name == "" || !strings.EqualFold(name, app.Name) {
		return "this credential is limited to the " + app.Name + " repository"
	}
	return ""
}

// repoTail returns everything after the first path segment (the workspace namespace).
func repoTail(repo string) string {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[i+1:]
	}
	return ""
}

// Push needs write/deploy or registry_write; pull needs read or registry_read.
func scopeAllows(key *models.APIKey, push bool) bool {
	// The general scopes follow the API's ladder, so admin and * keys work here too.
	if push {
		return models.ScopeSatisfies(key.Scopes, models.ScopeWrite) || models.ScopeSatisfies(key.Scopes, models.ScopeDeploy) ||
			key.HasScope(models.ScopeRegistryWrite)
	}
	return models.ScopeSatisfies(key.Scopes, models.ScopeRead) || key.HasScope(models.ScopeRegistryRead)
}

func roleAllows(role models.WorkspaceRole, push bool) bool {
	if push {
		return role.AtLeast(models.WorkspaceRoleDeveloper)
	}
	return role.AtLeast(models.WorkspaceRoleViewer)
}

// Without a workspace finder no namespace can be proven owned, so callers treat it as a denial.
func (s *Service) resolveNamespace(ns string) (*models.Workspace, error) {
	if s.ws == nil {
		return nil, errors.New("registry: no workspace finder is configured")
	}
	if id, ok := parseIDNamespace(ns); ok {
		return s.ws.FindByID(id)
	}
	return s.ws.FindByName(ns)
}

// Unambiguous because slugs disallow underscores.
func parseIDNamespace(ns string) (uint, bool) {
	rest, ok := strings.CutPrefix(ns, "ws_")
	if !ok || rest == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(id), true
}

func firstSegment(repo string) string {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i]
	}
	return repo
}

func parseBasic(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, found := strings.Cut(string(raw), ":")
	if !found || user == "" || pass == "" {
		return "", "", false
	}
	return user, pass, true
}

// The repository is the path prefix before the last /manifests/, /blobs/ or /tags/ marker.
func parseRepo(uri string) (repo string, isBase, isCatalog bool) {
	p := uri
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if i := strings.Index(p, "/v2"); i >= 0 {
		p = p[i:]
	}
	// Decode and clean first: the gateway normalizes proxied paths, so a raw first-segment read could authorize the wrong tenant.
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	p = path.Clean(p)
	// A path that climbed out of /v2 addresses no repository and must resolve to no workspace.
	if p != "/v2" && !strings.HasPrefix(p, "/v2/") {
		return "\x00", false, false // resolves to no workspace → denied
	}
	p = strings.TrimPrefix(p, "/v2")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", true, false
	}
	if p == "_catalog" {
		return "", false, true
	}
	for _, marker := range []string{"/manifests/", "/blobs/", "/tags/"} {
		if i := strings.LastIndex(p, marker); i >= 0 {
			return p[:i], false, false
		}
	}
	return p, false, false
}

// isPush maps the HTTP method to the registry action: writes are pushes.
func isPush(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
