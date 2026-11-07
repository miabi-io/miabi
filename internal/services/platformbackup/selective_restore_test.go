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

package platformbackup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Restoring the control-plane dump in place overwrites the live DB, including the rows describing the restore.
func TestControlPlaneIsNeverRestorableFromTheDashboard(t *testing.T) {
	ok, reason := restorableFromDashboard(dr.SubjectDatabase, true)
	if ok {
		t.Fatal("the control-plane dump was offered as a dashboard restore")
	}
	if !strings.Contains(reason, "running on") {
		t.Errorf("the reason should say why, not just refuse: %q", reason)
	}

	// The service refuses it too, so the rule does not live only in the UI.
	s := &Service{}
	err := s.restoreOne(context.Background(), &models.PlatformBackupSettings{}, nil,
		&models.PlatformBackup{
			Subject: models.PlatformBackupDatabase,
			Status:  models.BackupCompleted, Filename: "miabi.sql.gz.gpg",
		}, RestoreSelection{})
	if !errors.Is(err, ErrNotRestorableHere) {
		t.Fatalf("err = %v, want ErrNotRestorableHere", err)
	}
}

func TestIdentityEnvelopeIsNotRestorable(t *testing.T) {
	if ok, _ := restorableFromDashboard(dr.SubjectIdentity, true); ok {
		t.Error("the identity envelope was offered as a restore; it carries a key, not data")
	}
}

// Tenant data and platform volumes are the point of the feature.
func TestTenantDataIsRestorable(t *testing.T) {
	for _, subject := range []string{dr.SubjectTenantDatabase, dr.SubjectTenantVolume, dr.SubjectVolume} {
		if ok, reason := restorableFromDashboard(subject, true); !ok {
			t.Errorf("%s should be restorable: %s", subject, reason)
		}
	}
}

// Otherwise the operator would select it, wait, and get a failure that was knowable up front.
func TestMissingArtifactIsNotOffered(t *testing.T) {
	ok, reason := restorableFromDashboard(dr.SubjectTenantDatabase, false)
	if ok {
		t.Fatal("an artifact missing from the bucket was offered")
	}
	if !strings.Contains(reason, "no longer in the bucket") {
		t.Errorf("reason = %q", reason)
	}
}

func TestSuppliedPassphraseDoesNotTouchStoredSettings(t *testing.T) {
	crypto.Init("test-master-key")
	stored := &models.PlatformBackupSettings{BackupPassphraseEnc: ""}

	copied, err := withPassphrase(stored, "the-other-platforms-passphrase")
	if err != nil {
		t.Fatalf("withPassphrase: %v", err)
	}
	if stored.BackupPassphraseEnc != "" {
		t.Error("the stored settings were mutated; a foreign passphrase must not become this platform's")
	}

	s := &Service{}
	got, err := s.passphrase(copied)
	if err != nil {
		t.Fatalf("passphrase: %v", err)
	}
	if got != "the-other-platforms-passphrase" {
		t.Errorf("passphrase = %q; the copy must resolve like a stored one", got)
	}
}

func TestRestoreSelectedRefusesAnEmptySelection(t *testing.T) {
	s := &Service{}
	_, err := s.RestoreSelected(context.Background(), &models.PlatformBackupSet{}, RestoreSelection{})
	if !errors.Is(err, ErrNothingSelected) {
		t.Fatalf("err = %v, want ErrNothingSelected", err)
	}
}

// Only running apps are stopped, so a restore never starts one the operator had stopped.
func TestMountsVolume(t *testing.T) {
	app := &models.Application{Mounts: []models.AppMount{{DockerName: "mb-vol-1-data"}}}
	if !mountsVolume(app, "mb-vol-1-data") {
		t.Error("an app mounting the volume was not matched")
	}
	if mountsVolume(app, "mb-vol-9-other") {
		t.Error("an app was matched to a volume it does not mount")
	}
	if mountsVolume(&models.Application{}, "mb-vol-1-data") {
		t.Error("an app with no mounts was matched")
	}
}
