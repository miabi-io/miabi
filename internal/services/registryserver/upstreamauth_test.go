// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"golang.org/x/crypto/bcrypt"
)

// The credential is what the gateway sends and what the registry checks, from one derivation — a
// mismatch between the two locks the gateway out of the registry entirely.
func TestUpstreamCredentialRoundTrips(t *testing.T) {
	s := &Service{}
	header := s.upstreamAuthHeader()

	raw, ok := strings.CutPrefix(header, "Basic ")
	if !ok {
		t.Fatalf("header = %q, want a Basic credential", header)
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("header is not valid base64: %v", err)
	}
	user, pass, ok := strings.Cut(string(dec), ":")
	if !ok || user != upstreamUser {
		t.Fatalf("credential = %q, want user %q", dec, upstreamUser)
	}
	if pass != s.upstreamPassword() {
		t.Error("the header carries a different password than upstreamPassword returns")
	}

	// The htpasswd file the registry reads must accept exactly that password.
	line, err := htpasswdLine(upstreamUser, pass)
	if err != nil {
		t.Fatal(err)
	}
	name, hash, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok || name != upstreamUser {
		t.Fatalf("htpasswd line = %q, want %q:<hash>", line, upstreamUser)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)); err != nil {
		t.Errorf("the registry would reject the gateway's own password: %v", err)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Error("htpasswd line has no trailing newline; distribution skips the last entry without one")
	}
}

// bcrypt truncates silently at 72 bytes, which would make a longer credential interchangeable with
// its own prefix. Refusing is the only safe answer.
func TestHtpasswdRefusesAnOverlongPassword(t *testing.T) {
	if _, err := htpasswdLine("u", strings.Repeat("x", 73)); err == nil {
		t.Error("a 73-byte password was hashed; bcrypt would have truncated it")
	}
}

type authDocker struct {
	docker.Client
	volumes []string
	copied  map[string]string
}

func (a *authDocker) CreateVolume(_ context.Context, name string, _ map[string]string, _ int64) (docker.Volume, error) {
	a.volumes = append(a.volumes, name)
	return docker.Volume{Name: name}, nil
}

func (a *authDocker) CopyToVolume(_ context.Context, volume, _, name string, content io.Reader, _ int64) error {
	if a.copied == nil {
		a.copied = map[string]string{}
	}
	b, _ := io.ReadAll(content)
	a.copied[volume+"/"+name] = string(b)
	return nil
}

// The file has to land in a volume of its own and be readable at the path the container's env
// names, or the registry starts with auth on and no way to satisfy it.
func TestEnsureUpstreamAuthWritesTheHtpasswd(t *testing.T) {
	dc := &authDocker{}
	s := &Service{}

	if err := s.ensureUpstreamAuth(context.Background(), dc, "registry:3"); err != nil {
		t.Fatal(err)
	}
	if len(dc.volumes) != 1 || dc.volumes[0] != authVolume {
		t.Errorf("volumes = %v, want just %q", dc.volumes, authVolume)
	}
	line, ok := dc.copied[authVolume+"/"+htpasswdName]
	if !ok {
		t.Fatalf("nothing written to %s/%s; wrote %v", authVolume, htpasswdName, dc.copied)
	}
	if !strings.HasPrefix(line, upstreamUser+":$2") {
		t.Errorf("htpasswd = %q, want a bcrypt entry for %q", line, upstreamUser)
	}

	env := upstreamAuthEnv()
	wantPath := "REGISTRY_AUTH_HTPASSWD_PATH=" + authPath + "/" + htpasswdName
	var hasPath, hasAuth, hasRealm bool
	for _, e := range env {
		switch {
		case e == wantPath:
			hasPath = true
		case e == "REGISTRY_AUTH=htpasswd":
			hasAuth = true
		case strings.HasPrefix(e, "REGISTRY_AUTH_HTPASSWD_REALM="):
			hasRealm = true
		}
	}
	if !hasAuth || !hasPath || !hasRealm {
		t.Errorf("env = %v, want auth, realm and the path matching the mount", env)
	}
}

// Every call site of the client builds its own request, so the credential rides a transport rather
// than each call — one that forgot it would fail only at run time, against a live registry.
func TestClientSendsTheCredentialOnEveryRequest(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repositories":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, func() string { return "Basic zzz" })
	if _, err := c.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "Basic zzz" {
		t.Errorf("Authorization = %q, want the supplied credential", got)
	}

	// A nil supplier is the un-credentialed case (tests, and a registry with auth off).
	plain := NewClient(srv.URL, nil)
	if _, err := plain.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "" {
		t.Errorf("Authorization = %q, want none when no supplier is given", got)
	}
}
