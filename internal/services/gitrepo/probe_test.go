// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitrepo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func statusOf(t *testing.T, svc *Service, id uint) *models.GitRepository {
	t.Helper()
	g, err := svc.Get(1, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return g
}

// Adding a repository probes it, so the list can say whether the credential
// actually works without every page load re-probing every remote.
func TestCreateRecordsConnectionStatus(t *testing.T) {
	svc, _ := newUpdateService(t)
	svc.SetDialer(func(context.Context, *models.GitRepository) error { return nil })

	g, err := svc.Create(1, Input{Name: "ok-repo", URL: "https://github.com/acme/ok"})
	if err != nil {
		t.Fatal(err)
	}
	stored := statusOf(t, svc, g.ID)
	if stored.ConnectionStatus != models.GitConnectionOK {
		t.Errorf("status = %q, want ok", stored.ConnectionStatus)
	}
	if stored.ConnectionCheckedAt == nil {
		t.Error("checked-at not recorded")
	}
	if stored.ConnectionError != "" {
		t.Errorf("a successful check left an error: %q", stored.ConnectionError)
	}
}

// A failed probe must not fail the create. The credential is already stored, and
// refusing to save would leave the user with nothing — and no record of what they
// typed. The status carries the bad news instead.
func TestCreateSucceedsWhenTheProbeFails(t *testing.T) {
	svc, _ := newUpdateService(t)
	svc.SetDialer(func(context.Context, *models.GitRepository) error {
		return errors.New("authentication required")
	})

	g, err := svc.Create(1, Input{Name: "bad-repo", URL: "https://github.com/acme/private"})
	if err != nil {
		t.Fatalf("create failed because the probe did: %v", err)
	}
	stored := statusOf(t, svc, g.ID)
	if stored.ConnectionStatus != models.GitConnectionFailed {
		t.Errorf("status = %q, want failed", stored.ConnectionStatus)
	}
	if !strings.Contains(stored.ConnectionError, "authentication failed") {
		t.Errorf("reason = %q, want the credential-rejected explanation", stored.ConnectionError)
	}
}

// A relabel must not cost a round trip to the provider, nor disturb a status
// that is still accurate.
func TestUpdateDoesNotProbeOnARelabel(t *testing.T) {
	svc, _ := newUpdateService(t)
	probes := 0
	svc.SetDialer(func(context.Context, *models.GitRepository) error {
		probes++
		return nil
	})

	g, err := svc.Create(1, Input{Name: "repo", URL: "https://github.com/acme/repo"})
	if err != nil {
		t.Fatal(err)
	}
	after := probes

	if _, err := svc.Update(1, g.ID, Input{DisplayName: "A Nicer Label"}); err != nil {
		t.Fatal(err)
	}
	if probes != after {
		t.Errorf("a display-name change probed the remote (%d extra)", probes-after)
	}
}

// Changing the URL or the credential invalidates what the last check proved, so
// it must be re-checked.
func TestUpdateProbesWhenTheTargetChanges(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"url", Input{URL: "https://github.com/acme/elsewhere"}},
		{"auth type", Input{URL: "https://github.com/acme/repo", AuthType: models.GitAuthToken, Secret: "ghp_x"}},
		{"username", Input{URL: "https://github.com/acme/repo", Username: "someone"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newUpdateService(t)
			probes := 0
			svc.SetDialer(func(context.Context, *models.GitRepository) error {
				probes++
				return nil
			})
			g, err := svc.Create(1, Input{Name: "repo-" + strings.ReplaceAll(c.name, " ", "-"), URL: "https://github.com/acme/repo"})
			if err != nil {
				t.Fatal(err)
			}
			after := probes
			if _, err := svc.Update(1, g.ID, c.in); err != nil {
				t.Fatal(err)
			}
			if probes == after {
				t.Errorf("changing the %s did not re-check the connection", c.name)
			}
		})
	}
}

// An explicit test records its result too, so the icon in the list reflects the
// most recent check whoever ran it.
func TestTestConnectionPersistsTheResult(t *testing.T) {
	svc, _ := newUpdateService(t)
	svc.SetDialer(func(context.Context, *models.GitRepository) error { return nil })
	g, err := svc.Create(1, Input{Name: "repo", URL: "https://github.com/acme/repo"})
	if err != nil {
		t.Fatal(err)
	}

	// It now fails; the recorded status must follow.
	svc.SetDialer(func(context.Context, *models.GitRepository) error {
		return errors.New("repository not found")
	})
	if err := svc.TestConnection(context.Background(), 1, g.ID); err == nil {
		t.Fatal("expected the failing check to return an error")
	}
	if s := statusOf(t, svc, g.ID); s.ConnectionStatus != models.GitConnectionFailed {
		t.Errorf("status = %q, want failed", s.ConnectionStatus)
	}

	// And back again — a recovered credential must clear the recorded reason.
	svc.SetDialer(func(context.Context, *models.GitRepository) error { return nil })
	if err := svc.TestConnection(context.Background(), 1, g.ID); err != nil {
		t.Fatalf("recovered check failed: %v", err)
	}
	stored := statusOf(t, svc, g.ID)
	if stored.ConnectionStatus != models.GitConnectionOK {
		t.Errorf("status = %q, want ok", stored.ConnectionStatus)
	}
	if stored.ConnectionError != "" {
		t.Errorf("a recovered check left the old reason behind: %q", stored.ConnectionError)
	}
}

// The two failures a user must tell apart: fix the URL, or fix the token.
func TestConnectionReasonIsActionable(t *testing.T) {
	cases := []struct{ err, want string }{
		{"authentication required", "authentication failed"},
		{"authorization failed", "authentication failed"},
		{"repository not found", "repository not found"},
		{"dial tcp: lookup nope", "could not reach the host"},
		{"no such host", "could not reach the host"},
	}
	for _, c := range cases {
		if got := connectionReason(errors.New(c.err)); !strings.Contains(got, c.want) {
			t.Errorf("connectionReason(%q) = %q, want it to mention %q", c.err, got, c.want)
		}
	}
	// A timeout is its own case: the host may be fine and simply unreachable here.
	if got := connectionReason(context.DeadlineExceeded); !strings.Contains(got, "timed out") {
		t.Errorf("a deadline should read as a timeout, got %q", got)
	}
	// Anything unrecognised is passed through rather than swallowed.
	if got := connectionReason(errors.New("something odd happened")); got != "something odd happened" {
		t.Errorf("an unknown error was not passed through: %q", got)
	}
}
