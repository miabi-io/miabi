// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
)

func TestValidRunAsUserInheritance(t *testing.T) {
	s := &Service{quota: &quota.Service{}} // no mandate
	app := &models.Application{WorkspaceID: 1, RunAsUser: "1000:1000"}

	// Unset on the request: the run inherits the app's account, snapshotted so the
	// job's history shows what actually ran.
	if got, err := s.validRunAsUser(app, ""); err != nil || got != "1000:1000" {
		t.Errorf("blank should inherit the app's, got %q (%v)", got, err)
	}
	// An explicit choice wins over the app's.
	if got, err := s.validRunAsUser(app, "2000"); err != nil || got != "2000" {
		t.Errorf("request should override the app's, got %q (%v)", got, err)
	}
	// Neither set: the image's user (or the profile's UID) applies at run time.
	if got, err := s.validRunAsUser(&models.Application{WorkspaceID: 1}, ""); err != nil || got != "" {
		t.Errorf("nothing set should stay blank, got %q (%v)", got, err)
	}
}

func TestValidRunAsUserUnderMandate(t *testing.T) {
	q := &quota.Service{}
	q.SetForceNonRoot(true)
	s := &Service{quota: q}
	app := &models.Application{WorkspaceID: 1}

	// A job cannot be the way back to root where a deploy could not go.
	for _, v := range []string{"0", "root", "appuser"} {
		if _, err := s.validRunAsUser(app, v); !errors.Is(err, models.ErrRunAsUserRoot) {
			t.Errorf("%q must be refused for a job under a non-root mandate, got %v", v, err)
		}
	}
	if got, err := s.validRunAsUser(app, "1000"); err != nil || got != "1000" {
		t.Errorf("a non-root uid should be accepted, got %q (%v)", got, err)
	}

	// checkRunAsUser is the schedule's variant: blank stays blank, so a CronJob
	// inherits the app's account when each run fires rather than pinning it now.
	appWithUser := &models.Application{WorkspaceID: 1, RunAsUser: "1000"}
	if got, err := s.checkRunAsUser(appWithUser, ""); err != nil || got != "" {
		t.Errorf("a schedule should not snapshot the app's user, got %q (%v)", got, err)
	}
}
