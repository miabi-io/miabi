// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
)

type licensedEE struct {
	fakeEE
	ent enterprise.Entitlements
}

func (l licensedEE) Entitlements() enterprise.Entitlements { return l.ent }

// Non-admins get the flags the UI gates on, and nothing that identifies the customer or the install.
func TestLicenseEntitlementsExposeOnlyGatingFields(t *testing.T) {
	expires := time.Now().Add(90 * 24 * time.Hour)
	h := NewLicenseHandler(licensedEE{ent: enterprise.Entitlements{
		Edition: "enterprise", Tier: "business", State: "valid",
		Flags:    map[string]bool{enterprise.FlagAdvancedCanary: true},
		Customer: "Acme Corp", LicenseID: "lic_secret123", InstallID: "inst_abc",
		URL: "https://portal.example.com", Limits: map[string]int{"node_limit": 50}, NotAfter: &expires,
	}}, nil, nil, nil, nil, "inst_this", nil)

	rec := httptest.NewRecorder()
	c := okapi.NewContext(okapi.New(), rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/license/entitlements", nil))
	if err := h.Entitlements(c); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{`"edition":"enterprise"`, `"state":"valid"`, `"` + enterprise.FlagAdvancedCanary + `":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
	for _, leak := range []string{"Acme Corp", "lic_secret123", "inst_abc", "inst_this", "portal.example.com", "node_limit", "not_after"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaks %q: %s", leak, body)
		}
	}
}
