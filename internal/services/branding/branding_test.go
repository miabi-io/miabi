// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"errors"
	"strings"
	"testing"
)

// The sign-in page is shown to unauthenticated visitors, so an admin-supplied href
// is the most exposed string in the product. These are the cases that matter.
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
	// Counted in runes, not bytes: a label of accented characters is not secretly
	// half the length it looks.
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
