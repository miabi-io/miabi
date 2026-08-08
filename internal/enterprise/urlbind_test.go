//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package enterprise

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise/license"
)

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"https://miabi.acme.com":          "miabi.acme.com",
		"https://miabi.acme.com/":         "miabi.acme.com",
		"http://Miabi.Acme.com:8443/path": "miabi.acme.com",
		"miabi.acme.com":                  "miabi.acme.com",
		"  HTTPS://Miabi.Acme.com  ":      "miabi.acme.com",
		"":                                "",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func activeClaims(installID, url string) *license.Claims {
	now := time.Now()
	return &license.Claims{
		Edition: EditionEnterprise, InstallID: installID, URL: url, Flags: []string{FlagSSOSAML},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), GraceDays: 7,
	}
}

// bind builds an impl with the claims' binding pre-evaluated (as load/Install do).
func bind(instanceURL, installID string, c *license.Claims) *impl {
	e := &impl{instanceURL: instanceURL, installID: installID, claims: c}
	ok, reason := e.bindingResult(c, "")
	e.bindMismatch, e.bindReason = !ok, reason
	return e
}

func TestBinding_Unlimited(t *testing.T) {
	// Neither install id nor URL bound → works anywhere.
	e := bind("https://a.example.com", "mbi_1", activeClaims("", ""))
	if e.bindMismatch {
		t.Fatal("unbound license should never mismatch")
	}
	if !e.Has(FlagSSOSAML) {
		t.Error("unlimited license should grant its flag")
	}
}

func TestBinding_InstallIDMatch(t *testing.T) {
	// Install ID matches (case-insensitive); the strong binding.
	e := bind("", "MBI_ABC", activeClaims("mbi_abc", ""))
	if e.bindMismatch {
		t.Fatal("matching install id should not mismatch")
	}
	if err := e.Require(FlagSSOSAML); err != nil {
		t.Errorf("Require = %v, want nil", err)
	}
}

func TestBinding_InstallIDMismatch(t *testing.T) {
	e := bind("", "mbi_this", activeClaims("mbi_other", ""))
	if !e.bindMismatch {
		t.Fatal("different install id should mismatch")
	}
	if e.Has(FlagSSOSAML) {
		t.Error("mismatched license must not grant flags")
	}
	if !errors.Is(e.Require(FlagSSOSAML), ErrLicenseBindingMismatch) {
		t.Error("Require should return ErrLicenseBindingMismatch")
	}
	ent := e.Entitlements()
	if ent.State != StateBindingMismatch {
		t.Errorf("state = %q, want %q", ent.State, StateBindingMismatch)
	}
	if ent.BindingError != BindingErrorInstallID {
		t.Errorf("binding_error = %q, want %q", ent.BindingError, BindingErrorInstallID)
	}
	if ent.InstallID != "mbi_other" {
		t.Errorf("bound install id not surfaced: %q", ent.InstallID)
	}
	if len(ent.Flags) != 0 {
		t.Error("mismatched view should expose no flags")
	}
}

func TestBinding_URLMatch(t *testing.T) {
	// Install ID matches AND URL host matches (conjunctive).
	e := bind("http://miabi.acme.com:9000/app", "mbi_1", activeClaims("mbi_1", "https://miabi.acme.com"))
	if e.bindMismatch {
		t.Fatal("both bindings match; should not mismatch")
	}
	if !e.Has(FlagSSOSAML) {
		t.Error("matched license should grant its flag")
	}
}

func TestBinding_URLMismatch(t *testing.T) {
	// Install id matches but URL host differs → mismatch on url.
	e := bind("https://miabi.acme.com", "mbi_1", activeClaims("mbi_1", "https://other.example.com"))
	if !e.bindMismatch {
		t.Fatal("URL host differs; should mismatch")
	}
	if e.Entitlements().BindingError != BindingErrorURL {
		t.Errorf("binding_error = %q, want %q", e.Entitlements().BindingError, BindingErrorURL)
	}
}

func TestURLAllowed_RequestHost(t *testing.T) {
	const lic = "https://miabi.acme.com"
	cases := []struct {
		name       string
		license    string
		candidates []string
		want       bool
	}{
		{"unlimited", "", []string{"other.com"}, true},
		{"matches configured url", lic, []string{"https://miabi.acme.com", ""}, true},
		{"matches request host only (config unset)", lic, []string{"", "miabi.acme.com:443"}, true},
		{"matches neither → deny", lic, []string{"https://config.example", "wrong.example"}, false},
		{"no host known → fail open", lic, []string{"", ""}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := urlAllowed(c.license, c.candidates...); got != c.want {
				t.Errorf("urlAllowed(%q, %v) = %v, want %v", c.license, c.candidates, got, c.want)
			}
		})
	}
}

func TestBinding_UnknownInstanceURLFailsOpen(t *testing.T) {
	// A URL-only binding with an unknown instance URL can't be checked → fail open
	// so a legitimate install is never bricked. (An install_id binding, by
	// contrast, is always enforceable — the Install ID is always known.)
	e := bind("", "mbi_1", activeClaims("", "https://miabi.acme.com"))
	if e.bindMismatch {
		t.Error("unknown instance URL should fail open (allow)")
	}
}
