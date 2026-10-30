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

package dnsprovider

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
)

// relName must reduce both a Miabi FQDN and a provider's already-relative name to
// the same label, so conflict detection compares like for like.
func TestRelName(t *testing.T) {
	cases := []struct{ name, zone, want string }{
		{"_miabi-challenge.example.com", "example.com", "_miabi-challenge"},
		{"_miabi-challenge.example.com.", "example.com.", "_miabi-challenge"},
		{"_miabi-challenge", "example.com", "_miabi-challenge"}, // already relative
		{"example.com", "example.com", "@"},                     // zone apex
		{"app.sub.example.com", "example.com", "app.sub"},
	}
	for _, tc := range cases {
		if got := relName(tc.name, tc.zone); got != tc.want {
			t.Errorf("relName(%q, %q) = %q, want %q", tc.name, tc.zone, got, tc.want)
		}
	}
}

func recordSet(recs []dns.Record) string {
	parts := make([]string, 0, len(recs))
	for _, r := range recs {
		parts = append(parts, r.Type+" "+r.Name+" "+r.Value)
	}
	return strings.Join(parts, "; ")
}

// addressRecords writes an A and an AAAA for each IP family, falls back to a CNAME to the hostname, never puts a
// CNAME at the zone apex, and aliases a www host to its bare name when that name is routed too.
func TestAddressRecords(t *testing.T) {
	dual := models.PublicAddress{IPv4: "203.0.113.1", IPv6: "2001:db8::1", Hostname: "gw.example.net"}
	cases := []struct {
		name   string
		host   string
		routed []string
		target models.PublicAddress
		want   string
	}{
		{"ipv4 only", "blog.example.com", nil, models.PublicAddress{IPv4: "203.0.113.1"}, "A blog.example.com 203.0.113.1"},
		{"ipv6 only", "blog.example.com", nil, models.PublicAddress{IPv6: "2001:db8::1"}, "AAAA blog.example.com 2001:db8::1"},
		{"dual stack", "blog.example.com", nil, dual, "A blog.example.com 203.0.113.1; AAAA blog.example.com 2001:db8::1"},
		{"hostname only", "blog.example.com", nil, models.PublicAddress{Hostname: "gw.example.net"}, "CNAME blog.example.com gw.example.net."},
		{"hostname only at apex", "example.com", nil, models.PublicAddress{Hostname: "gw.example.net"}, ""},
		{"no target", "blog.example.com", nil, models.PublicAddress{}, ""},
		{"www beside its apex", "www.example.com", []string{"example.com", "www.example.com"}, dual, "CNAME www.example.com example.com."},
		{"www beside a subdomain", "www.shop.example.com", []string{"Shop.Example.com."}, dual, "CNAME www.shop.example.com shop.example.com."},
		{"www alone", "www.example.com", []string{"www.example.com"}, dual, "A www.example.com 203.0.113.1; AAAA www.example.com 2001:db8::1"},
		{"www with a hostname only", "www.example.com", []string{"example.com"}, models.PublicAddress{Hostname: "gw.example.net"}, "CNAME www.example.com gw.example.net."},
		{"apex www is a zone", "www.com", []string{"com"}, dual, "A www.com 203.0.113.1; AAAA www.com 2001:db8::1"},
		{"www that is the zone apex", "www.example.com", []string{"example.com"}, dual, "A www.example.com 203.0.113.1; AAAA www.example.com 2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zone := "example.com"
			if tc.name == "www that is the zone apex" {
				zone = "www.example.com"
			}
			if got := recordSet(addressRecords(tc.host, zone, tc.routed, tc.target)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCnameClash(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		clash      bool
	}{
		{"A", "CNAME", true}, {"CNAME", "AAAA", true}, {"TXT", "CNAME", false}, {"A", "AAAA", false}, {"CNAME", "CNAME", false},
	} {
		if got := cnameClash(tc.have, tc.want); got != tc.clash {
			t.Errorf("cnameClash(%s, %s) = %v, want %v", tc.have, tc.want, got, tc.clash)
		}
	}
}
