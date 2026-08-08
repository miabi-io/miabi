// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backupsettings

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTarget is what the "Test connection" button runs. This drives it against a REAL store, with the settings
// supplied rather than stored — the same shape the UI sends when an operator tests before saving, which is also
// the path that never touches the repository. Skips unless MIABI_S3_IT_BUCKET is set.
func itInput(t *testing.T) SaveInput {
	t.Helper()
	bucket := os.Getenv("MIABI_S3_IT_BUCKET")
	if bucket == "" {
		t.Skip("set MIABI_S3_IT_BUCKET (and friends) to run the backup-target integration test")
	}
	secret := os.Getenv("MIABI_S3_IT_SECRET_KEY")
	return SaveInput{
		S3Enabled:  true,
		S3Endpoint: os.Getenv("MIABI_S3_IT_ENDPOINT"), S3Bucket: bucket,
		S3Region: os.Getenv("MIABI_S3_IT_REGION"), S3AccessKey: os.Getenv("MIABI_S3_IT_ACCESS_KEY"),
		S3SecretKey:        &secret,
		S3UseSSL:           os.Getenv("MIABI_S3_IT_SSL") == "true",
		S3ForcePathStyle:   os.Getenv("MIABI_S3_IT_PATH_STYLE") == "true",
		DatabaseBackupPath: "miabi-it/backups/databases",
		VolumeBackupPath:   "miabi-it/backups/volumes",
		BundlePath:         "miabi-it/bundles",
	}
}

func TestTargetProbesEveryPrefix_Integration(t *testing.T) {
	// A nil repository is deliberate: with the secret supplied, testing an unsaved
	// target must not need a stored one — which is exactly how the UI uses it.
	svc := NewService(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checks, err := svc.TestTarget(ctx, 1, itInput(t))
	if err != nil {
		t.Fatalf("TestTarget: %v", err)
	}
	if len(checks) != 3 {
		t.Fatalf("expected one check per distinct prefix, got %d: %+v", len(checks), checks)
	}
	for _, c := range checks {
		if !c.OK() {
			t.Errorf("%s: %s", c.Prefix, c.Error)
			continue
		}
		if !c.Removed {
			t.Errorf("%s: written and read back, but the test object could not be removed", c.Prefix)
		}
		if c.Key == "" {
			t.Errorf("%s: the check names no object", c.Prefix)
		}
		t.Logf("%s → wrote, read back and removed %s", c.Prefix, c.Key)
	}
}

// A wrong secret must come back as a per-prefix failure the operator can read,
// not as a pass and not as an internal error.
func TestTargetReportsBadCredentials_Integration(t *testing.T) {
	in := itInput(t)
	wrong := "definitely-not-the-secret-key"
	in.S3SecretKey = &wrong

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checks, err := NewService(nil).TestTarget(ctx, 1, in)
	if err != nil {
		t.Fatalf("TestTarget returned a hard error instead of per-prefix results: %v", err)
	}
	for _, c := range checks {
		if c.OK() {
			t.Fatalf("%s passed with a wrong secret key", c.Prefix)
		}
		t.Logf("%s → %s", c.Prefix, c.Error)
	}
}
