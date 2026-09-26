// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
)

func TestIgnoreClientGone(t *testing.T) {
	app := okapi.New()
	app.Use(ignoreClientGone)
	app.Get("/stream", func(c *okapi.Context) error { return context.Canceled })
	app.Get("/broken", func(c *okapi.Context) error { return errors.New("boom") })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx))
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("a client that went away was answered as a server error")
	}

	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/broken", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a real failure = %d, want 500", rec.Code)
	}
}
