// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestNormalizeAndValidate(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"Example.COM", "example.com", true},
		{"  shop.example.com. ", "shop.example.com", true},
		{"*.example.com", "example.com", true}, // wildcard prefix stripped
		{"a.b.co", "a.b.co", true},
		{"nodot", "nodot", false},
		{"bad_underscore.com", "bad_underscore.com", false},
		{"", "", false},
	}
	for _, c := range cases {
		in := Input{Name: c.in}
		in.normalize()
		if in.Name != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, in.Name, c.want)
		}
		if c.want != "" && validName(in.Name) != c.valid {
			t.Errorf("validName(%q) = %v, want %v", in.Name, validName(in.Name), c.valid)
		}
	}
}

func TestNormalizeDefaultsTLS(t *testing.T) {
	in := Input{Name: "example.com"}
	in.normalize()
	if in.TLSMode != models.DomainTLSACME {
		t.Errorf("default TLS = %q, want acme", in.TLSMode)
	}
	in = Input{Name: "example.com", TLSMode: models.DomainTLSCustom}
	in.normalize()
	if in.TLSMode != models.DomainTLSCustom {
		t.Errorf("custom TLS not preserved: %q", in.TLSMode)
	}
}

func TestChallenge(t *testing.T) {
	d := &models.Domain{Name: "example.com", VerificationToken: "abc123"}
	if got := d.ChallengeHost(); got != "_miabi-challenge.example.com" {
		t.Errorf("ChallengeHost = %q", got)
	}
	if got := d.ChallengeValue(); got != "miabi-verification=abc123" {
		t.Errorf("ChallengeValue = %q", got)
	}
}

func TestDomainsOverlap(t *testing.T) {
	dom := func(name string, wildcard bool) *models.Domain {
		return &models.Domain{Name: name, Wildcard: wildcard}
	}
	cases := []struct {
		name string
		a, b *models.Domain
		want bool
	}{
		{"exact match", dom("example.com", false), dom("example.com", false), true},
		{"case-insensitive exact", dom("Example.COM", false), dom("example.com", false), true},
		{"unrelated names", dom("example.com", false), dom("other.com", false), false},
		{"a wildcard covers b subdomain", dom("example.com", true), dom("app.example.com", false), true},
		{"b wildcard covers a subdomain", dom("app.example.com", false), dom("example.com", true), true},
		{"no wildcard, subdomain relation", dom("example.com", false), dom("app.example.com", false), false},
		{"wildcard but sibling, not subdomain", dom("example.com", true), dom("example.org", false), false},
		{"wildcard does not cover parent's parent", dom("a.example.com", true), dom("example.com", false), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := domainsOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("domainsOverlap(%q wc=%v, %q wc=%v) = %v, want %v",
					tt.a.Name, tt.a.Wildcard, tt.b.Name, tt.b.Wildcard, got, tt.want)
			}
		})
	}
}

// TestReverifyDecisionProofDecays pins the existing drift behaviour for a DNS-proven
// domain: consecutive misses accumulate and the proof is revoked at the threshold.
func TestReverifyDecisionProofDecays(t *testing.T) {
	misses := 0
	for i := 1; i < verifyMissThreshold; i++ {
		out := reverifyDecision(models.VerifiedViaDNS, misses, i > 1, false)
		if out.Unverify {
			t.Fatalf("miss %d un-verified early (threshold %d)", i, verifyMissThreshold)
		}
		if !out.Failed || !out.Write {
			t.Fatalf("miss %d = %+v, want a recorded failure", i, out)
		}
		misses = out.Misses
	}
	out := reverifyDecision(models.VerifiedViaDNS, misses, true, false)
	if !out.Unverify {
		t.Fatalf("miss %d = %+v, want the proof revoked", verifyMissThreshold, out)
	}
}

// TestReverifyDecisionWaiverSurvives is the regression for the reported bug: an admin
// override used to be re-checked like a proof, so the drift cron un-verified it a few
// runs later and took its routes offline. A waiver must never be revoked, however many
// times the TXT it never had fails to resolve.
func TestReverifyDecisionWaiverSurvives(t *testing.T) {
	misses := 0
	for i := 1; i <= verifyMissThreshold*3; i++ {
		out := reverifyDecision(models.VerifiedViaAdmin, misses, i > 1, false)
		if out.Unverify {
			t.Fatalf("admin waiver revoked after %d misses; a waiver has no proof to lose", i)
		}
		if !out.Failed {
			t.Fatalf("miss %d = %+v, want the absent proof still recorded", i, out)
		}
		misses = out.Misses
	}
	if misses != verifyMissThreshold*3 {
		t.Fatalf("misses = %d, want them still counted for display", misses)
	}
}

// TestReverifyDecisionWaiverGraduates covers the promotion path: once an overridden
// domain can prove itself, it stops being an override nobody remembers granting.
func TestReverifyDecisionWaiverGraduates(t *testing.T) {
	out := reverifyDecision(models.VerifiedViaAdmin, 4, true, true)
	if !out.Promote || !out.Write {
		t.Fatalf("a resolving waiver = %+v, want Promote and Write", out)
	}
	if out.Misses != 0 || out.Failed {
		t.Fatalf("a resolving waiver = %+v, want the failure state cleared", out)
	}
}

// TestReverifyDecisionCleanProofSkipsWrite keeps the steady state free of database
// writes: a proven domain that still resolves changes nothing.
func TestReverifyDecisionCleanProofSkipsWrite(t *testing.T) {
	if out := reverifyDecision(models.VerifiedViaDNS, 0, false, true); out.Write {
		t.Fatalf("a clean re-check = %+v, want no write", out)
	}
	if out := reverifyDecision(models.VerifiedViaDNS, 2, true, true); !out.Write {
		t.Fatal("a recovered domain must clear its failure state")
	}
}
