// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func routeTLS(spec string) string {
	return `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: nginx, ports: [{ container: 80 }] }
---
apiVersion: miabi.io/v1
kind: Route
metadata: { name: web }
spec:
  hosts: [shop.example.com]
  app: web
` + spec + "\n"
}

// A custom TLS route must name its certificate, or apply refuses it.
func TestCustomTLSNamesItsCertificate(t *testing.T) {
	set, err := d.Parse([]byte(routeTLS("  tls: custom\n  certificate: shop-wildcard")))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Route/web")
	if r.Route.TLSCertificate() != "shop-wildcard" || r.Route.TLSMode() != "custom" {
		t.Errorf("certificate = %q, mode = %q", r.Route.TLSCertificate(), r.Route.TLSMode())
	}
}

// Either TLS field alone behaves as a mistake, so both are refused at parse.
func TestTLSFieldsMustAgree(t *testing.T) {
	cases := []struct{ name, spec, want string }{
		{"custom without a certificate", "  tls: custom", "needs a certificate"},
		{"certificate on an acme route", "  tls: acme\n  certificate: shop-wildcard", "applies to tls: custom"},
		{"certificate with tls off", "  tls: off\n  certificate: shop-wildcard", "turns TLS off"},
		{"provider on a custom route", "  tls: custom\n  certificate: c\n  tlsProvider: internal-ca", "applies to ACME certificates"},
		{"provider with tls off", "  tls: off\n  tlsProvider: internal-ca", "turns TLS off"},
		{"block: certificate with TLS off", "  tls: { enabled: false, certificate: c }", "turns TLS off"},
		{"block: provider with TLS off", "  tls: { enabled: false, provider: internal-ca }", "turns TLS off"},
		{"block: provider and certificate", "  tls: { provider: internal-ca, certificate: c }", "applies to ACME certificates"},
		{"both certificate spellings", "  tls: { certificate: a }\n  certificate: b", "old spelling of tls.certificate"},
		{"both provider spellings", "  tls: { provider: a }\n  tlsProvider: b", "old spelling of tls.provider"},
		{"unknown old mode", "  tls: letsencrypt", "tls must be a block"},
		{"unknown block field", "  tls: { mode: acme }", "field mode not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse([]byte(routeTLS(tc.spec)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestACMEWithAProviderIsValid(t *testing.T) {
	if _, err := d.Parse([]byte(routeTLS("  tls: acme\n  tlsProvider: internal-ca"))); err != nil {
		t.Errorf("a provider on an acme route was refused: %v", err)
	}
}

// Swapping the served certificate must plan, or rotating a cert through Git would do nothing.
func TestCertificateChangeConverges(t *testing.T) {
	mk := func(cert string) *d.ResourceSet {
		set, err := d.Parse([]byte(routeTLS("  tls: custom\n  certificate: " + cert)))
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	if !d.BuildPlan(mk("new-cert"), mk("old-cert"), d.PlanOptions{}).HasChanges() {
		t.Error("changing the certificate did not plan a change")
	}
	if d.BuildPlan(mk("same"), mk("same"), d.PlanOptions{}).HasChanges() {
		t.Error("an unchanged certificate planned a change")
	}
}

func TestCertificateNameIsValidated(t *testing.T) {
	_, err := d.Parse([]byte(routeTLS("  tls: custom\n  certificate: Not A Name")))
	if err == nil || !strings.Contains(err.Error(), "must be a resource name") {
		t.Errorf("error = %v", err)
	}
}

// The block and the old string form must plan the same, or upgrading a manifest would churn routes.
func TestTLSBlockMatchesTheOldForm(t *testing.T) {
	cases := []struct{ name, old, block, mode, cert, provider string }{
		{"acme by default", "", "  tls: {}", "acme", "", ""},
		{"acme", "  tls: acme", "  tls: { enabled: true }", "acme", "", ""},
		{"acme with a provider", "  tls: acme\n  tlsProvider: internal-ca", "  tls: { provider: internal-ca }", "acme", "", "internal-ca"},
		{"custom", "  tls: custom\n  certificate: shop", "  tls: { certificate: shop }", "custom", "shop", ""},
		{"off", "  tls: off", "  tls: { enabled: false }", "off", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(spec string) *d.ResourceSet {
				set, err := d.Parse([]byte(routeTLS(spec)))
				if err != nil {
					t.Fatalf("%q: %v", spec, err)
				}
				return set
			}
			oldSet, newSet := parse(tc.old), parse(tc.block)
			r, _ := newSet.Get("Route/web")
			if r.Route.TLSMode() != tc.mode || r.Route.TLSCertificate() != tc.cert || r.Route.TLSProvider() != tc.provider {
				t.Errorf("block read as mode=%q cert=%q provider=%q", r.Route.TLSMode(), r.Route.TLSCertificate(), r.Route.TLSProvider())
			}
			if d.BuildPlan(newSet, oldSet, d.PlanOptions{}).HasChanges() {
				t.Error("the block planned a change against the old form")
			}
		})
	}
}

// Marshal writes the block, so an old manifest round-trips into the new spelling.
func TestOldTLSFormMarshalsAsTheBlock(t *testing.T) {
	set, err := d.Parse([]byte(routeTLS("  tls: off")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "enabled: false") {
		t.Errorf("tls: off did not marshal as the block:\n%s", out)
	}
	again, err := d.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := again.Get("Route/web"); r.Route.TLSMode() != "off" {
		t.Errorf("round trip lost tls off: %q", r.Route.TLSMode())
	}
}
