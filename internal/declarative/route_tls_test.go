// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// The gap this closes: tls: custom could not be declared at all, because the manifest had no way to
// name the certificate and apply refuses a custom route without one.
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

// Either half alone reads as deliberate and behaves as a mistake, so both are refused at parse
// rather than at apply — or worse, silently.
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

// Swapping the certificate a route serves is a real change, and the plan has to say so — otherwise
// rotating a cert through Git would appear to do nothing.
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

// A certificate name has to be resolvable, so it follows the same rule every other cross-resource
// reference does.
func TestCertificateNameIsValidated(t *testing.T) {
	_, err := d.Parse([]byte(routeTLS("  tls: custom\n  certificate: Not A Name")))
	if err == nil || !strings.Contains(err.Error(), "must be a resource name") {
		t.Errorf("error = %v", err)
	}
}
