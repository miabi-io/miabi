// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import "testing"

func TestFirstUncoveredName(t *testing.T) {
	verified := []certDomainRow{{Name: "example.com", Verified: true}}
	unverified := []certDomainRow{{Name: "example.com"}}
	banned := []certDomainRow{{Name: "example.com", Verified: true, Banned: true}}

	cases := []struct {
		name    string
		cert    certRow
		domains []certDomainRow
		want    string
	}{
		{"covered by a verified domain", certRow{CommonName: "app.example.com"}, verified, ""},
		{"the domain itself", certRow{CommonName: "example.com"}, verified, ""},
		{"wildcard", certRow{CommonName: "*.example.com"}, verified, ""},
		// The hijack: registered but never proven.
		{"unverified domain", certRow{CommonName: "app.example.com"}, unverified, "app.example.com"},
		{"banned domain", certRow{CommonName: "app.example.com"}, banned, "app.example.com"},
		{"no domains at all", certRow{CommonName: "app.example.com"}, nil, "app.example.com"},
		// A single bad SAN condemns the certificate — it is the name that would be served.
		{"bad SAN", certRow{CommonName: "app.example.com", DNSNames: `["app.example.com","console.other.com"]`}, verified, "console.other.com"},
		{"good SANs", certRow{CommonName: "app.example.com", DNSNames: `["app.example.com","api.example.com"]`}, verified, ""},
		// Unparseable SANs must not silently pass the common name alone.
		{"broken SAN json", certRow{CommonName: "app.example.com", DNSNames: `[oops`}, verified, ""},
		{"empty cert", certRow{}, verified, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstUncoveredName(c.cert, c.domains); got != c.want {
				t.Fatalf("firstUncoveredName = %q, want %q", got, c.want)
			}
		})
	}
}
