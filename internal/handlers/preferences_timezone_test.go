// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
)

// Preferences accept only IANA names; an omitted timezone leaves the stored one alone.
func TestPreferencesTimezoneValidation(t *testing.T) {
	app := okapi.New()
	app.Put("/preferences", okapi.H(func(c *okapi.Context, _ *UpdatePreferencesRequest) error {
		return c.String(http.StatusOK, "ok")
	}))
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{}`, http.StatusOK},
		{`{"timezone":"UTC"}`, http.StatusOK},
		{`{"timezone":"Africa/Kinshasa"}`, http.StatusOK},
		{`{"timezone":"America/Argentina/Buenos_Aires"}`, http.StatusOK},
		{`{"timezone":""}`, http.StatusBadRequest},
		{`{"timezone":"Local"}`, http.StatusBadRequest},
		{`{"timezone":"Mars/Olympus"}`, http.StatusBadRequest},
		{`{"timezone":"+02:00"}`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.body, rec.Code, tc.want, rec.Body.String())
		}
	}
}
