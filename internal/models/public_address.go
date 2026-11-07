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

package models

import "strings"

// PublicAddress is where a cluster's DNS records point; the hostname is a CNAME target used only when there is no IP.
type PublicAddress struct {
	IPv4     string `json:"ipv4,omitempty"`
	IPv6     string `json:"ipv6,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// IsZero reports an address with nothing to point a record at.
func (a PublicAddress) IsZero() bool { return a.IPv4 == "" && a.IPv6 == "" && a.Hostname == "" }

// HasIP reports whether address records (A/AAAA) can be written, rather than a CNAME.
func (a PublicAddress) HasIP() bool { return a.IPv4 != "" || a.IPv6 != "" }

// String is the address to tell someone pointing a record by hand.
func (a PublicAddress) String() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{a.IPv4, a.IPv6, a.Hostname} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// AddressRecord is one DNS record that points a host at a public address.
type AddressRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AddressRecords CNAMEs a routed www host to its bare name so the pair cannot drift; apex hosts never get a CNAME.
func (a PublicAddress) AddressRecords(host string, routed []string, apex bool) []AddressRecord {
	host = NormalizeHost(host)
	if host == "" || strings.HasPrefix(host, "*") {
		return nil
	}
	if bare, ok := strings.CutPrefix(host, "www."); ok && a.HasIP() && !apex && strings.Contains(bare, ".") {
		for _, r := range routed {
			if NormalizeHost(r) == bare {
				return []AddressRecord{{Type: "CNAME", Name: host, Value: bare + "."}}
			}
		}
	}
	var recs []AddressRecord
	if a.IPv4 != "" {
		recs = append(recs, AddressRecord{Type: "A", Name: host, Value: a.IPv4})
	}
	if a.IPv6 != "" {
		recs = append(recs, AddressRecord{Type: "AAAA", Name: host, Value: a.IPv6})
	}
	if len(recs) == 0 && a.Hostname != "" && !apex {
		recs = append(recs, AddressRecord{Type: "CNAME", Name: host, Value: strings.TrimSuffix(a.Hostname, ".") + "."})
	}
	return recs
}

// NormalizeHost lowercases a hostname and drops surrounding space and a trailing dot.
func NormalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
