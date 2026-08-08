// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Namespace is the immutable storage namespace for a workspace — the repository
// prefix the gateway rewrites the (mutable) workspace name to before forwarding
// to the registry, so images survive a workspace rename. e.g. ws_1.
func Namespace(workspaceID uint) string { return fmt.Sprintf("ws_%d", workspaceID) }

// AuthInput is the forwarded registry request context (from Goma forwardAuth).
type AuthInput struct {
	Authorization string // raw Authorization header
	Method        string // X-Forwarded-Method (else the request method)
	URI           string // X-Forwarded-Uri (the /v2/... path)
}

// AuthResult is the authorization decision the auth endpoint translates to HTTP. On allow it also carries
// the rewrite target: the gateway sets the request's repository namespace segment to Namespace (ws_<id>)
// before forwarding, so storage keys off the immutable id while users address by name.
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

// Authorize decides whether a forwarded registry request is allowed. The docker password is an API token
// and the username is advisory. The repository's first path segment names the target workspace: a
// workspace-scoped token may act only on its own; a user token on any it is a member of, gated by role.
func (s *Service) Authorize(in AuthInput) AuthResult {
	_, token, ok := parseBasic(in.Authorization)
	if !ok {
		// No/blank credentials → challenge so docker login presents Basic auth.
		return AuthResult{Status: http.StatusUnauthorized, Challenge: true, Reason: "authentication required"}
	}

	repo, isBase, isCatalog := parseRepo(in.URI)

	// Platform principal: the build/deploy worker pushes/pulls built images with
	// the configured shared token. It may act on any workspace namespace (it never
	// crosses a tenant boundary the user controls), so authorize it directly.
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
	// The login handshake hits GET /v2/ with no repo — allow any valid token so
	// the credential is accepted and stored (no namespace to authorize yet).
	if isBase {
		return AuthResult{Status: http.StatusOK, UserID: key.UserID}
	}
	// Raw /v2/_catalog would expose every tenant's repositories, so deny it at the
	// edge; the workspace UI lists repos via a server-side, namespace-filtered
	// path instead.
	if isCatalog {
		return AuthResult{Status: http.StatusForbidden, Reason: "catalog access is not permitted"}
	}

	// Resolve the target workspace from the repository's first path segment. This uniformly handles both the
	// human name the user types ("acme/...") and the rewritten id form the registry hands back in a
	// blob-upload Location ("ws_<id>/..."), so the upload handshake works without response rewriting.
	ws, err := s.resolveNamespace(firstSegment(repo))
	if err != nil {
		return AuthResult{Status: http.StatusForbidden, Reason: "unknown workspace namespace"}
	}

	push := isPush(in.Method)
	if reason := s.authorizePrincipal(key, ws.ID, push); reason != "" {
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

// authorizeMountSource authorizes the source repository of a cross-repository blob mount against the target
// workspace. That source travels in the QUERY STRING, which neither parseRepo nor the gateway's path-regex
// rewrite touches — so without this a member could lift layers out of another tenant's namespace via a digest.
func (s *Service) authorizeMountSource(uri string, workspaceID uint) string {
	from := mountSource(uri)
	if from == "" {
		return ""
	}
	// Normalized for the same reason the path is: "tenant-a/../tenant-b" must not
	// read as tenant-a here while naming tenant-b to the registry.
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

// mountSource extracts the "from" repository of a cross-repository blob mount
// from a request URI, or "" when the request is not one.
func mountSource(uri string) string {
	i := strings.IndexByte(uri, '?')
	if i < 0 {
		return ""
	}
	q, err := url.ParseQuery(uri[i+1:])
	if err != nil {
		// An unparseable query on an upload could still carry a mount the registry
		// itself parses more leniently. Naming a source we can't read is refused.
		if strings.Contains(uri[i+1:], "from=") {
			return "\x00" // never resolves to a namespace → denied
		}
		return ""
	}
	return strings.Trim(strings.TrimSpace(q.Get("from")), "/")
}

// authorizePrincipal returns "" when the token may perform the action on the
// workspace, else a human denial reason.
func (s *Service) authorizePrincipal(key *models.APIKey, workspaceID uint, push bool) string {
	if !scopeAllows(key, push) {
		if push {
			return "push requires a write or deploy scope"
		}
		return "pull requires a read scope"
	}
	if key.WorkspaceID != nil {
		if *key.WorkspaceID != workspaceID {
			return "token is scoped to a different workspace"
		}
		return ""
	}
	// An account-wide (user) token is authorized by the owner's membership.
	member, err := s.ws.FindMember(workspaceID, key.UserID)
	if err != nil {
		return "you are not a member of this workspace"
	}
	if !roleAllows(member.Role, push) {
		return "your role does not permit pushing to this workspace"
	}
	return ""
}

// scopeAllows maps the action to the required token scope. A push needs a
// general write/deploy scope OR the dedicated registry_write; a pull needs read
// OR registry_read. (HasScope already covers "*".)
func scopeAllows(key *models.APIKey, push bool) bool {
	if push {
		return key.HasScope(models.ScopeWrite) || key.HasScope(models.ScopeDeploy) || key.HasScope(models.ScopeRegistryWrite)
	}
	return key.HasScope(models.ScopeRead) || key.HasScope(models.ScopeRegistryRead)
}

// roleAllows maps the action to the required workspace role: any member may
// pull; developer and above may push.
func roleAllows(role models.WorkspaceRole, push bool) bool {
	if push {
		return role.AtLeast(models.WorkspaceRoleDeveloper)
	}
	return role.AtLeast(models.WorkspaceRoleViewer)
}

// resolveNamespace resolves the repository namespace segment to a workspace — either the rewritten id form
// "ws_<id>" or the workspace name. Without a workspace finder wired, no namespace can be proven to belong
// to anyone — every caller of this treats that as a denial rather than a pass.
func (s *Service) resolveNamespace(ns string) (*models.Workspace, error) {
	if s.ws == nil {
		return nil, errors.New("registry: no workspace finder is configured")
	}
	if id, ok := parseIDNamespace(ns); ok {
		return s.ws.FindByID(id)
	}
	return s.ws.FindByName(ns)
}

// parseIDNamespace parses the "ws_<id>" storage namespace back to its id. A
// workspace name can never take this form (slugs disallow underscores), so it is
// unambiguous.
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

// parseRepo extracts the repository name from a /v2/... URI. It returns isBase for the "/v2/" root (login
// handshake) and isCatalog for "/v2/_catalog". Registry API actions live under /manifests/, /blobs/, or
// /tags/, so the repository is the path prefix before the last such marker.
func parseRepo(uri string) (repo string, isBase, isCatalog bool) {
	p := uri
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if i := strings.Index(p, "/v2"); i >= 0 {
		p = p[i:]
	}
	// Percent-decode and resolve the path BEFORE any segment is read. This is a tenant boundary, and it is only sound
	// if the path authorized here is the path the registry serves. The gateway normalizes what it proxies, so a
	// traversal reaches the registry as tenant-b while a raw first-segment read approves it as tenant-a.
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	p = path.Clean(p)
	// Clean anchors at "/", so a path that climbed lands outside /v2 rather than keeping a "../" prefix.
	// Require the result to still be a registry path: if it is not, the request addresses no repository and
	// must resolve to no workspace, never to whatever segment it happens to start with.
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
