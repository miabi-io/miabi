// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
)

type fakeKeys struct {
	key *models.APIKey
	err error
}

func (f fakeKeys) Verify(string) (*models.APIKey, error) { return f.key, f.err }

type fakeWS struct {
	byID    map[uint]*models.Workspace
	byName  map[string]*models.Workspace
	members map[string]*models.WorkspaceMember // "<wsID>:<userID>"
}

func (f fakeWS) FindByID(id uint) (*models.Workspace, error) {
	if w, ok := f.byID[id]; ok {
		return w, nil
	}
	return nil, errors.New("not found")
}

func (f fakeWS) FindByName(name string) (*models.Workspace, error) {
	if w, ok := f.byName[name]; ok {
		return w, nil
	}
	return nil, errors.New("not found")
}

func (f fakeWS) FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error) {
	if m, ok := f.members[fmt.Sprintf("%d:%d", workspaceID, userID)]; ok {
		return m, nil
	}
	return nil, errors.New("not a member")
}

// fixture: acme (id 7), other (id 8); user 42 is a developer in acme, user 99 a
// viewer in acme. Nobody is a member of other.
func wsFixture() fakeWS {
	acme := &models.Workspace{ID: 7, Name: "acme"}
	other := &models.Workspace{ID: 8, Name: "other"}
	return fakeWS{
		byID:   map[uint]*models.Workspace{7: acme, 8: other},
		byName: map[string]*models.Workspace{"acme": acme, "other": other},
		members: map[string]*models.WorkspaceMember{
			"7:42": {WorkspaceID: 7, UserID: 42, Role: models.WorkspaceRoleDeveloper},
			"7:99": {WorkspaceID: 7, UserID: 99, Role: models.WorkspaceRoleViewer},
		},
	}
}

// wsToken builds a service whose token is scoped to workspace acme (id 7).
func wsToken(scopes []string) *Service {
	id := uint(7)
	return &Service{keys: fakeKeys{key: &models.APIKey{WorkspaceID: &id, UserID: 1, Scopes: scopes}}, ws: wsFixture()}
}

// userToken builds a service whose token is account-wide, owned by userID.
func userToken(userID uint, scopes []string) *Service {
	return &Service{keys: fakeKeys{key: &models.APIKey{UserID: userID, Scopes: scopes}}, ws: wsFixture()}
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestAuthorize(t *testing.T) {
	rw := []string{models.ScopeRead, models.ScopeWrite}

	cases := []struct {
		name       string
		svc        *Service
		in         AuthInput
		wantStatus int
		challenge  bool
	}{
		{"no credentials challenges", wsToken(rw), AuthInput{Method: "GET", URI: "/v2/"}, http.StatusUnauthorized, true},
		{"malformed basic challenges", wsToken(rw), AuthInput{Authorization: "Basic !!!", Method: "GET", URI: "/v2/"}, http.StatusUnauthorized, true},
		{
			"invalid token challenges",
			&Service{keys: fakeKeys{err: errors.New("nope")}, ws: wsFixture()},
			AuthInput{Authorization: basic("acme", "bad"), Method: "GET", URI: "/v2/acme/web/manifests/1"},
			http.StatusUnauthorized, true,
		},
		{"base v2 allowed for any valid token", wsToken(rw), AuthInput{Authorization: basic("x", "t"), Method: "GET", URI: "/v2/"}, http.StatusOK, false},
		{"catalog forbidden", wsToken(rw), AuthInput{Authorization: basic("x", "t"), Method: "GET", URI: "/v2/_catalog"}, http.StatusForbidden, false},
		{"unknown namespace forbidden", wsToken(rw), AuthInput{Authorization: basic("x", "t"), Method: "GET", URI: "/v2/ghost/web/manifests/1"}, http.StatusForbidden, false},

		// --- workspace-scoped token (form #1) ---
		{"ws token pulls own ns", wsToken([]string{models.ScopeRead}), AuthInput{Authorization: basic("acme", "t"), Method: "GET", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"ws token pushes own ns", wsToken(rw), AuthInput{Authorization: basic("acme", "t"), Method: "PUT", URI: "/v2/acme/web/blobs/uploads/x"}, http.StatusOK, false},
		{"ws token rejected on other ns", wsToken(rw), AuthInput{Authorization: basic("acme", "t"), Method: "GET", URI: "/v2/other/web/manifests/1"}, http.StatusForbidden, false},
		{"ws token replay via ws_7 id form", wsToken(rw), AuthInput{Authorization: basic("acme", "t"), Method: "PUT", URI: "/v2/ws_7/web/blobs/uploads/x?_state=y"}, http.StatusOK, false},
		{"ws token rejected on ws_8 id form", wsToken(rw), AuthInput{Authorization: basic("acme", "t"), Method: "GET", URI: "/v2/ws_8/web/manifests/1"}, http.StatusForbidden, false},
		{"read-only ws token cannot push", wsToken([]string{models.ScopeRead}), AuthInput{Authorization: basic("acme", "t"), Method: "POST", URI: "/v2/acme/web/blobs/uploads/"}, http.StatusForbidden, false},

		// --- account-wide user token (form #2) ---
		{"user (developer) pulls a member workspace", userToken(42, rw), AuthInput{Authorization: basic("jane", "t"), Method: "GET", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"user (developer) pushes a member workspace", userToken(42, rw), AuthInput{Authorization: basic("jane", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"user (viewer) pulls a member workspace", userToken(99, []string{models.ScopeRead}), AuthInput{Authorization: basic("vic", "t"), Method: "GET", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"user (viewer) cannot push", userToken(99, rw), AuthInput{Authorization: basic("vic", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusForbidden, false},
		{"user non-member forbidden", userToken(42, rw), AuthInput{Authorization: basic("jane", "t"), Method: "GET", URI: "/v2/other/web/manifests/1"}, http.StatusForbidden, false},
		{"user with read-only scope cannot push", userToken(42, []string{models.ScopeRead}), AuthInput{Authorization: basic("jane", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusForbidden, false},
		{"user via ws_7 id form pushes", userToken(42, rw), AuthInput{Authorization: basic("jane", "t"), Method: "PUT", URI: "/v2/ws_7/web/blobs/uploads/x"}, http.StatusOK, false},

		// --- dedicated registry scopes ---
		{"registry_write pushes", wsToken([]string{models.ScopeRegistryWrite}), AuthInput{Authorization: basic("acme", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"registry_read pulls", wsToken([]string{models.ScopeRegistryRead}), AuthInput{Authorization: basic("acme", "t"), Method: "GET", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
		{"registry_read cannot push", wsToken([]string{models.ScopeRegistryRead}), AuthInput{Authorization: basic("acme", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusForbidden, false},
		{"user registry_write pushes a member workspace", userToken(42, []string{models.ScopeRegistryWrite}), AuthInput{Authorization: basic("jane", "t"), Method: "PUT", URI: "/v2/acme/web/manifests/1"}, http.StatusOK, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.svc.Authorize(tc.in)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", got.Status, got.Reason, tc.wantStatus)
			}
			if got.Challenge != tc.challenge {
				t.Errorf("challenge = %v, want %v", got.Challenge, tc.challenge)
			}
			// On an allow for a real repo, the rewrite target must point at acme/ws_7.
			if tc.wantStatus == http.StatusOK && got.WorkspaceID != 0 {
				if got.Workspace != "acme" || got.Namespace != "ws_7" || got.WorkspaceID != 7 {
					t.Errorf("target = (%q,%q,%d), want (acme,ws_7,7)", got.Workspace, got.Namespace, got.WorkspaceID)
				}
			}
		})
	}
}

func TestAuthorizePlatformToken(t *testing.T) {
	// The platform token (build/deploy worker) authorizes any namespace; keys are
	// never consulted (Verify would fail here, proving the short-circuit).
	svc := &Service{cfg: config.RegistryConfig{PlatformToken: "plat-secret"}, ws: wsFixture(), keys: fakeKeys{err: errors.New("nope")}}

	cases := []struct {
		name       string
		in         AuthInput
		wantStatus int
		wantNs     string
	}{
		{"push to acme", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "PUT", URI: "/v2/acme/app-5/manifests/9"}, http.StatusOK, "ws_7"},
		{"push to other (any namespace)", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "PUT", URI: "/v2/other/app-1/blobs/uploads/"}, http.StatusOK, "ws_8"},
		{"pull via ws_8 id form", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "GET", URI: "/v2/ws_8/app-1/manifests/9"}, http.StatusOK, "ws_8"},
		{"base v2", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "GET", URI: "/v2/"}, http.StatusOK, ""},
		{"catalog forbidden", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "GET", URI: "/v2/_catalog"}, http.StatusForbidden, ""},
		{"unknown namespace", AuthInput{Authorization: basic("_miabi", "plat-secret"), Method: "PUT", URI: "/v2/ghost/app-1/manifests/9"}, http.StatusForbidden, ""},
		{"wrong token falls through to key path", AuthInput{Authorization: basic("_miabi", "wrong"), Method: "GET", URI: "/v2/acme/app-1/manifests/9"}, http.StatusUnauthorized, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.Authorize(tc.in)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", got.Status, got.Reason, tc.wantStatus)
			}
			if tc.wantNs != "" && got.Namespace != tc.wantNs {
				t.Errorf("namespace = %q, want %q", got.Namespace, tc.wantNs)
			}
		})
	}
}

// A cross-repository blob mount names its source in the query string, which the
// gateway's namespace rewrite (a path regex) never touches. Without an explicit
// check a member of one workspace could push into their own namespace while
// lifting layers out of another tenant's, needing only a digest.
func TestAuthorizeBlobMount(t *testing.T) {
	rw := []string{models.ScopeRead, models.ScopeWrite}
	const uploads = "/v2/acme/web/blobs/uploads/"

	cases := []struct {
		name       string
		svc        *Service
		uri        string
		wantStatus int
	}{
		{"mount within the workspace is allowed", wsToken(rw), uploads + "?mount=sha256:abc&from=acme/api", http.StatusOK},
		{"mount within the workspace by id form", wsToken(rw), uploads + "?mount=sha256:abc&from=ws_7/api", http.StatusOK},
		{"mount from another workspace is refused", wsToken(rw), uploads + "?mount=sha256:abc&from=other/api", http.StatusForbidden},
		{"mount from another workspace by id form", wsToken(rw), uploads + "?mount=sha256:abc&from=ws_8/api", http.StatusForbidden},
		{"mount from an unknown namespace", wsToken(rw), uploads + "?mount=sha256:abc&from=ghost/api", http.StatusForbidden},
		{"plain upload is unaffected", wsToken(rw), uploads, http.StatusOK},
		{"upload state param is not a mount", wsToken(rw), uploads + "uuid?_state=abc", http.StatusOK},
		{"mount with no source is not a cross-repo mount", wsToken(rw), uploads + "?mount=sha256:abc", http.StatusOK},
		// An account-wide token is bound by the same rule: membership of the target
		// workspace says nothing about the source one.
		{"user token cannot mount across workspaces", userToken(42, rw), uploads + "?mount=sha256:abc&from=other/api", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.svc.Authorize(AuthInput{Authorization: basic("acme", "t"), Method: "POST", URI: tc.uri})
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", got.Status, got.Reason, tc.wantStatus)
			}
		})
	}
}

// The platform principal pushes on any workspace's behalf, but a build only ever
// mounts inside the namespace it is pushing to — so the same rule applies.
func TestAuthorizePlatformTokenBlobMount(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{PlatformToken: "plat-secret"}, ws: wsFixture(), keys: fakeKeys{err: errors.New("nope")}}
	auth := basic("_miabi", "plat-secret")

	got := svc.Authorize(AuthInput{Authorization: auth, Method: "POST", URI: "/v2/acme/web/blobs/uploads/?mount=sha256:abc&from=acme/api"})
	if got.Status != http.StatusOK {
		t.Errorf("same-namespace mount = %d (%s), want 200", got.Status, got.Reason)
	}
	got = svc.Authorize(AuthInput{Authorization: auth, Method: "POST", URI: "/v2/acme/web/blobs/uploads/?mount=sha256:abc&from=other/api"})
	if got.Status != http.StatusForbidden {
		t.Errorf("cross-namespace mount = %d (%s), want 403", got.Status, got.Reason)
	}
}

func TestParseRepo(t *testing.T) {
	cases := []struct {
		uri     string
		repo    string
		base    bool
		catalog bool
	}{
		{"/v2/", "", true, false},
		{"/v2/_catalog", "", false, true},
		{"/v2/acme/web/manifests/1.0", "acme/web", false, false},
		{"/v2/acme/web/blobs/uploads/uuid", "acme/web", false, false},
		{"/v2/acme/team/web/tags/list", "acme/team/web", false, false},
		{"/v2/acme/web/manifests/sha256:abc?foo=bar", "acme/web", false, false},
		{"https://registry.example.com/v2/acme/web/manifests/1", "acme/web", false, false},
	}
	for _, tc := range cases {
		repo, base, catalog := parseRepo(tc.uri)
		if repo != tc.repo || base != tc.base || catalog != tc.catalog {
			t.Errorf("parseRepo(%q) = (%q,%v,%v), want (%q,%v,%v)", tc.uri, repo, base, catalog, tc.repo, tc.base, tc.catalog)
		}
	}
}

func TestFirstSegmentAndIDNamespace(t *testing.T) {
	if firstSegment("acme/team/web") != "acme" {
		t.Errorf("firstSegment wrong")
	}
	if id, ok := parseIDNamespace("ws_42"); !ok || id != 42 {
		t.Errorf("parseIDNamespace(ws_42) = (%d,%v), want (42,true)", id, ok)
	}
	for _, ns := range []string{"acme", "ws_", "ws_x", "ws-5", "wsabc"} {
		if _, ok := parseIDNamespace(ns); ok {
			t.Errorf("parseIDNamespace(%q) should be false", ns)
		}
	}
}

// A token for one workspace must not reach another's namespace by smuggling the
// target through path traversal. The gateway normalizes the path it proxies
// upstream, so authorizing the raw form would approve "a" and serve "b" — a
// confirmed cross-tenant write, not a theoretical one.
func TestParseRepoNormalizesTraversal(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"/v2/tenant-a/../tenant-b/app/blobs/uploads/", "tenant-b/app"},
		{"/v2/tenant-a/%2e%2e/tenant-b/app/blobs/uploads/", "tenant-b/app"},
		{"/v2/tenant-a/app/../../tenant-b/app/manifests/latest", "tenant-b/app"},
		{"/v2//tenant-b/app/blobs/uploads/", "tenant-b/app"},
		// Ordinary requests are untouched.
		{"/v2/tenant-a/app/blobs/uploads/", "tenant-a/app"},
		{"/v2/ws_2/app/manifests/latest", "ws_2/app"},
		{"/v2/tenant-a/nested/app/blobs/uploads/", "tenant-a/nested/app"},
	}
	for _, tc := range cases {
		got, _, _ := parseRepo(tc.uri)
		if got != tc.want {
			t.Errorf("parseRepo(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

// A path that climbs out of /v2 belongs to no namespace and must never resolve
// to the first segment it happens to contain.
func TestParseRepoRefusesEscapingPath(t *testing.T) {
	for _, uri := range []string{"/v2/../../etc/passwd", "/v2/%2e%2e/%2e%2e/etc"} {
		repo, isBase, isCatalog := parseRepo(uri)
		if isBase || isCatalog {
			t.Errorf("parseRepo(%q) classified as base/catalog", uri)
		}
		if repo == "" || !strings.Contains(repo, "\x00") {
			t.Errorf("parseRepo(%q) = %q, want an unresolvable namespace", uri, repo)
		}
	}
}

// The mount source rides in the query string, where the same trick applies.
func TestMountSourceTraversalIsRefused(t *testing.T) {
	svc := &Service{ws: wsFixture()}
	// tenant-a is workspace 1 in the fixture; a source that cleans to another
	// tenant must not pass by naming tenant-a first.
	if reason := svc.authorizeMountSource(
		"/v2/tenant-a/app/blobs/uploads/?mount=sha256:x&from=tenant-a/../tenant-b/app", 1); reason == "" {
		t.Error("a traversing blob mount source was accepted")
	}
}
