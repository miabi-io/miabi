// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// TestConnectionBroke pins down when a runner's uptime clock restarts — the input the offline alert debounces
// its resolve on. Getting this wrong in either direction is a bug you only notice in production: reset too
// eagerly and an alert never clears, too rarely and a flapping runner clears it instantly.
func TestConnectionBroke(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	cases := []struct {
		name string
		r    models.Runner
		want bool
	}{
		{"heartbeat on a live connection", models.Runner{
			Status: models.RunnerStatusOnline, ConnectedSince: at(time.Hour), LastSeenAt: at(20 * time.Second),
		}, false},
		{"draining still counts as connected", models.Runner{
			Status: models.RunnerStatusDraining, ConnectedSince: at(time.Hour), LastSeenAt: at(20 * time.Second),
		}, false},
		{"reconnect after a clean disconnect", models.Runner{
			Status: models.RunnerStatusOffline, ConnectedSince: nil, LastSeenAt: at(5 * time.Minute),
		}, true},
		{"first ever connection", models.Runner{
			Status: models.RunnerStatusOffline,
		}, true},
		{"row says online but heartbeats stopped (control plane was killed)", models.Runner{
			Status: models.RunnerStatusOnline, ConnectedSince: at(time.Hour), LastSeenAt: at(10 * time.Minute),
		}, true},
		{"one late heartbeat is not a break", models.Runner{
			Status: models.RunnerStatusOnline, ConnectedSince: at(time.Hour), LastSeenAt: at(45 * time.Second),
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := connectionBroke(&c.r, now); got != c.want {
				t.Fatalf("connectionBroke = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeLabels(t *testing.T) {
	got := normalizeLabels([]string{"  buildkit ", "arch=amd64", "buildkit", "", "  "})
	// trimmed, de-duplicated, blanks dropped, sorted for an order-independent
	// scheduler subset match.
	want := []string{"arch=amd64", "buildkit"}
	if len(got) != len(want) {
		t.Fatalf("normalizeLabels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normalizeLabels[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if normalizeLabels(nil) != nil || normalizeLabels([]string{" ", ""}) != nil {
		t.Error("all-blank / empty labels should normalize to nil")
	}
}

func TestNormalizeConcurrency(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 1}, {-3, 1}, {1, 1}, {5, 5}} {
		if got := normalizeConcurrency(tc.in); got != tc.want {
			t.Errorf("normalizeConcurrency(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDisplayNameDefaultsToName(t *testing.T) {
	if got := displayName("  ", "web-builder"); got != "web-builder" {
		t.Errorf("blank display name should default to name, got %q", got)
	}
	if got := displayName(" Prod Builder ", "web-builder"); got != "Prod Builder" {
		t.Errorf("display name = %q, want trimmed label", got)
	}
}

func TestTokenGenerateAndHash(t *testing.T) {
	a := generateToken()
	b := generateToken()
	if !strings.HasPrefix(a, tokenPrefix) {
		t.Errorf("token %q missing prefix %q", a, tokenPrefix)
	}
	if a == b {
		t.Error("generateToken must not repeat")
	}
	// Hashing is deterministic (for the constant-time compare on authenticate) and
	// never returns the plaintext.
	if h1, h2 := hashToken(a), hashToken(a); h1 != h2 {
		t.Error("hashToken must be deterministic")
	}
	if hashToken(a) == a {
		t.Error("hashToken must not return the plaintext token")
	}
}

// Authenticate rejects a token without the runner prefix before touching the
// repository, so it is safe to call with no DB wired.
func TestAuthenticateRejectsBadPrefix(t *testing.T) {
	s := NewService(nil)
	if _, err := s.Authenticate("mbn_notarunner"); !errors.Is(err, ErrBadToken) {
		t.Errorf("wrong-prefix token: err = %v, want ErrBadToken", err)
	}
	if _, err := s.Authenticate(""); !errors.Is(err, ErrBadToken) {
		t.Errorf("empty token: err = %v, want ErrBadToken", err)
	}
}
