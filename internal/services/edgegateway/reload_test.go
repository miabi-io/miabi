// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestPostReloadSendsBearerToken(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := postReload(context.Background(), srv.Client(), srv.URL, "tok123"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/gateway/reload" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestPostReloadErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := postReload(context.Background(), srv.Client(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestReloadNoOpForLocalManager(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "img", "miabi", "ops@example.com")
	// Local manager uses the watched file provider; reload must be a no-op even
	// with no address configured.
	if err := s.Reload(context.Background(), &models.Server{IsLocal: true}, "tok"); err != nil {
		t.Fatalf("expected no-op for local manager, got %v", err)
	}
}

func TestReloadRequiresAddress(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "img", "miabi", "ops@example.com")
	if err := s.Reload(context.Background(), &models.Server{Name: "edge-1"}, "tok"); err == nil {
		t.Fatal("expected error when server has no address")
	}
}

func TestRenderConfigEnablesReloadForEdge(t *testing.T) {
	s := NewService(nil, "https://miabi.example.com", "img", "miabi", "ops@example.com")
	got := s.RenderConfig(&models.Server{Name: "edge-1"})
	if !strings.Contains(got, "reload:") {
		t.Fatalf("edge config missing reload block:\n%s", got)
	}

	// The local manager relies on file-provider watch; no reload block.
	mgr := s.RenderConfig(&models.Server{Name: "manager", IsLocal: true})
	if strings.Contains(mgr, "reload:") {
		t.Fatalf("manager config should not have a reload block:\n%s", mgr)
	}
}
