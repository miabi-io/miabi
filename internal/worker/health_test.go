// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzIsAlwaysOK(t *testing.T) {
	h := NewHealthServer(":0", nil, nil)
	rec := httptest.NewRecorder()
	h.healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — liveness must not depend on Redis or the database", rec.Code)
	}
	var got HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
}

func TestReadyzFailsWhenDependenciesAreUnreachable(t *testing.T) {
	h := NewHealthServer(":0", nil, nil)
	rec := httptest.NewRecorder()
	h.readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var got ReadyResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "not ready" {
		t.Errorf("status = %q, want not ready", got.Status)
	}
	if got.Database == "ok" || got.Redis == "ok" {
		t.Errorf("both dependencies should report a reason, got %+v", got)
	}
}

func TestHealthServerRoutesAndShutdown(t *testing.T) {
	h := NewHealthServer("127.0.0.1:0", nil, nil)
	mux, ok := h.srv.Handler.(*http.ServeMux)
	if !ok {
		t.Fatal("handler is not a ServeMux")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if _, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil)); pattern != path {
			t.Errorf("%s is not routed (matched %q)", path, pattern)
		}
	}
	// Shutting down a server that never listened must not block or panic.
	h.Start()()
}
