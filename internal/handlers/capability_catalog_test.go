// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
)

func catalogOf(t *testing.T, enabled bool) CapabilityCatalog {
	t.Helper()
	rec := httptest.NewRecorder()
	c := okapi.NewContext(okapi.New(), rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if err := NewCapabilityHandler(enabled, nil, nil).Catalog(c); err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	var body struct {
		Data CapabilityCatalog `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return body.Data
}

// With the switch off the catalogue is empty: a console that renders a picker
// from it must not offer a capability no request could obtain.
func TestCatalogIsEmptyWhenGrantsAreDisabled(t *testing.T) {
	got := catalogOf(t, false)

	if got.Enabled {
		t.Error("enabled = true with the switch off")
	}
	if len(got.Capabilities) != 0 {
		t.Errorf("offered %d capabilities with the switch off", len(got.Capabilities))
	}
	if len(got.Devices) != 0 {
		t.Errorf("offered %d devices with the switch off", len(got.Devices))
	}
	// Empty arrays rather than nulls, so the console tells "nothing on offer" from
	// "the field is missing" without a special case.
	raw := rawCatalog(t, false)
	for _, field := range []string{`"capabilities":[]`, `"devices":[]`} {
		if !strings.Contains(raw, field) {
			t.Errorf("payload %s does not contain %s", raw, field)
		}
	}
}

func TestCatalogListsEverythingWhenEnabled(t *testing.T) {
	got := catalogOf(t, true)

	if !got.Enabled {
		t.Error("enabled = false with the switch on")
	}
	if len(got.Capabilities) == 0 || len(got.Devices) == 0 {
		t.Fatalf("catalogue is empty with the switch on: %d caps, %d devices",
			len(got.Capabilities), len(got.Devices))
	}
	if got.MaxCapabilities == 0 || got.MaxDevices == 0 {
		t.Error("the limits are not reported, so the console cannot enforce them")
	}
}

func rawCatalog(t *testing.T, enabled bool) string {
	t.Helper()
	rec := httptest.NewRecorder()
	c := okapi.NewContext(okapi.New(), rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if err := NewCapabilityHandler(enabled, nil, nil).Catalog(c); err != nil {
		t.Fatal(err)
	}
	return rec.Body.String()
}
