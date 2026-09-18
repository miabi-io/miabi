// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import "testing"

func TestAdvancedClaimsRouting(t *testing.T) {
	cases := map[string]bool{
		"":                                  false,
		"rewrite: /v2":                      false,
		"methods: [GET]\nrewrite: /v2":      false,
		"hosts: [victim.example.com]":       true,
		"path: /api/v1/auth":                true,
		"priority: 9999":                    true,
		"name: mb-ws2-other":                true,
		"enabled: true":                     true,
		"target: http://elsewhere":          true,
		"backends:\n  - endpoint: http://x": true,
		"Hosts: [victim.example.com]":       true,
		"hosts: [unterminated":              true,
	}
	for cfg, want := range cases {
		if got := advancedClaimsRouting(cfg); got != want {
			t.Errorf("advancedClaimsRouting(%q) = %v, want %v", cfg, got, want)
		}
	}
}

func TestAdvancedRowHasHost(t *testing.T) {
	cases := map[string]bool{
		``:                       false,
		`[]`:                     false,
		`null`:                   false,
		`[""]`:                   false,
		`["app.example.com"]`:    true,
		`["","app.example.com"]`: true,
	}
	for hosts, want := range cases {
		if got := advancedRowHasHost(hosts); got != want {
			t.Errorf("advancedRowHasHost(%q) = %v, want %v", hosts, got, want)
		}
	}
}
