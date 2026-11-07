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
	if r.Route.Certificate != "shop-wildcard" {
		t.Errorf("certificate = %q", r.Route.Certificate)
	}
}

// Either TLS field alone behaves as a mistake, so both are refused at parse.
func TestTLSFieldsMustAgree(t *testing.T) {
	cases := []struct{ name, spec, want string }{
		{"custom without a certificate", "  tls: custom", "needs certificate"},
		{"certificate on an acme route", "  tls: acme\n  certificate: shop-wildcard", "applies to tls: custom"},
		{"certificate with tls off", "  tls: off\n  certificate: shop-wildcard", "applies to tls: custom"},
		{"provider on a custom route", "  tls: custom\n  certificate: c\n  tlsProvider: internal-ca", "applies to tls: acme"},
		{"provider with tls off", "  tls: off\n  tlsProvider: internal-ca", "applies to tls: acme"},
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
