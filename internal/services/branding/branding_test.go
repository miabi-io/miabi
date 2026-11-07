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

package branding

import (
	"errors"
	"strings"
	"testing"
)

// The sign-in page is shown to unauthenticated visitors, so admin hrefs are the most exposed strings.
func TestLinkURLsMustBeHTTP(t *testing.T) {
	refused := map[string]string{
		"javascript":           "javascript:alert(1)",
		"javascript spaced":    "javascript :alert(1)",
		"data uri":             "data:text/html,<script>alert(1)</script>",
		"protocol relative":    "//evil.example/terms",
		"scheme relative":      "/terms",
		"bare host":            "acme.example",
		"no host":              "https://",
		"file":                 "file:///etc/passwd",
		"vbscript":             "vbscript:msgbox(1)",
		"uppercase javascript": "JavaScript:alert(1)",
	}
	for name, raw := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeLinks([]Link{{Label: "Terms", URL: raw}})
			if !errors.Is(err, ErrUnsupportedScheme) {
				t.Fatalf("NormalizeLinks(%q) error = %v, want ErrUnsupportedScheme", raw, err)
			}
		})
	}

	accepted := []string{
		"https://acme.example/privacy",
		"http://acme.example",
		"HTTPS://acme.example/terms",
		"https://acme.example:8443/legal?x=1#top",
	}
	for _, raw := range accepted {
		t.Run("accepts "+raw, func(t *testing.T) {
			out, err := NormalizeLinks([]Link{{Label: "Terms", URL: raw}})
			if err != nil {
				t.Fatalf("NormalizeLinks(%q) = %v", raw, err)
			}
			if len(out) != 1 || out[0].URL != raw {
				t.Errorf("link not preserved: %+v", out)
			}
		})
	}
}

func TestLinkLabels(t *testing.T) {
	if _, err := NormalizeLinks([]Link{{URL: "https://acme.example"}}); !errors.Is(err, ErrLabelRequired) {
		t.Errorf("a link with no label was accepted: %v", err)
	}
	long := strings.Repeat("x", MaxLabelRunes+1)
	if _, err := NormalizeLinks([]Link{{Label: long, URL: "https://acme.example"}}); !errors.Is(err, ErrLabelTooLong) {
		t.Errorf("an over-long label was accepted: %v", err)
	}
	// Counted in runes, not bytes.
	ok := strings.Repeat("é", MaxLabelRunes)
	if _, err := NormalizeLinks([]Link{{Label: ok, URL: "https://acme.example"}}); err != nil {
		t.Errorf("a %d-rune label was refused: %v", MaxLabelRunes, err)
	}
}

// An empty row is how a form deletes a link, so it is dropped rather than refused.
func TestBlankRowsAreDropped(t *testing.T) {
	out, err := NormalizeLinks([]Link{
		{Label: "Website", URL: "https://acme.example"},
		{Label: "  ", URL: "   "},
		{},
	})
	if err != nil {
		t.Fatalf("NormalizeLinks: %v", err)
	}
	if len(out) != 1 || out[0].Label != "Website" {
		t.Errorf("got %+v, want just the one real link", out)
	}
}

func TestLinkCap(t *testing.T) {
	many := make([]Link, MaxLinks+1)
	for i := range many {
		many[i] = Link{Label: "L", URL: "https://acme.example"}
	}
	if _, err := NormalizeLinks(many); !errors.Is(err, ErrTooManyLinks) {
		t.Fatalf("error = %v, want ErrTooManyLinks", err)
	}
	if _, err := NormalizeLinks(many[:MaxLinks]); err != nil {
		t.Errorf("exactly the cap was refused: %v", err)
	}
}

// Blank is how pre-policy installs read, and they must keep letting accounts choose.
func TestParseAccentPolicy(t *testing.T) {
	for raw, want := range map[string]AccentPolicy{
		"": AccentPolicyDefault, "default": AccentPolicyDefault,
		"enforced": AccentPolicyEnforced, " Enforced ": AccentPolicyEnforced,
	} {
		if got, err := ParseAccentPolicy(raw); err != nil || got != want {
			t.Errorf("ParseAccentPolicy(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if got, err := ParseAccentPolicy("locked"); !errors.Is(err, ErrInvalidAccentPolicy) || got != AccentPolicyDefault {
		t.Errorf("ParseAccentPolicy(%q) = %q, %v; want the default and ErrInvalidAccentPolicy", "locked", got, err)
	}
}
