// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// This drives a whole recovery point — settings, identity envelope, control-plane dump, platform volume,
// tenant database and volume, finalization and the published manifest — against a real object store,
// because every fault here so far lived in the seams between the pieces. Needs S3/MinIO; skipped without.
func s3ForTest(t *testing.T) config.PlatformBackupConfig {
	t.Helper()
	endpoint := os.Getenv("MIABI_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MIABI_TEST_S3_ENDPOINT (and MIABI_TEST_S3_BUCKET) to run the recovery-point integration test")
	}
	bucket := os.Getenv("MIABI_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "dr-test"
	}
	return config.PlatformBackupConfig{
		S3Endpoint:       endpoint,
		S3Bucket:         bucket,
		S3Region:         "us-east-1",
		S3AccessKey:      envOr("MIABI_TEST_S3_ACCESS_KEY", "testkey"),
		S3SecretKey:      envOr("MIABI_TEST_S3_SECRET_KEY", "testsecret123"),
		S3ForcePathStyle: true,
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// testDB is an in-memory schema holding only what this service touches.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.PlatformBackupSet{}, &models.PlatformBackup{}, &models.PlatformBackupSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// fakeDocker stands in for the helper containers: it reports the output the real
// *-bkup tools print, so artifact-name detection is exercised for real.
type fakeDocker struct {
	docker.Client
	runs []docker.RunSpec
}

func (f *fakeDocker) PullImage(context.Context, string, *docker.RegistryAuth) error { return nil }
func (f *fakeDocker) EnsureNetwork(context.Context, string) (string, error)         { return "net", nil }
func (f *fakeDocker) CreateVolume(_ context.Context, name string, _ map[string]string, _ int64) (docker.Volume, error) {
	return docker.Volume{Name: name}, nil
}

// ListVolumes backs the exclusion check: the platform volume is genuine infra, so
// it must be accepted rather than filtered out.
func (f *fakeDocker) ListVolumes(context.Context) ([]docker.Volume, error) {
	return []docker.Volume{
		{Name: "mb-node-gateway-providers", Labels: map[string]string{docker.LabelManaged: "true"}},
	}, nil
}

func (f *fakeDocker) RunOneShot(_ context.Context, spec docker.RunSpec) (int, string, error) {
	f.runs = append(f.runs, spec)
	encrypted := false
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, "GPG_PASSPHRASE=") {
			encrypted = true
		}
	}
	// Mirror the tools' narration: the plain name first, then the encrypted one
	// they actually upload. Taking the first match here was a real bug.
	switch {
	case strings.Contains(spec.Name, "dbbkup"):
		out := "Dumping database\nBackup file created: miabi_20260731_120000.sql.gz\n"
		if encrypted {
			out += "Encrypting miabi_20260731_120000.sql.gz.gpg\nUploading miabi_20260731_120000.sql.gz.gpg\n"
		} else {
			out += "Uploading miabi_20260731_120000.sql.gz\n"
		}
		return 0, out, nil
	default:
		name := "archive_20260731_120000.tar.gz"
		out := fmt.Sprintf("Creating archive file=%s\nArchive created file=%s\n", name, name)
		if encrypted {
			out += fmt.Sprintf("Encrypting %s.gpg\nUploading archive file=%s.gpg\n", name, name)
		} else {
			out += fmt.Sprintf("Uploading archive file=%s\n", name)
		}
		return 0, out, nil
	}
}

type fakeClients struct{ dc *fakeDocker }

func (f fakeClients) For(uint) (docker.Client, error) { return f.dc, nil }
func (f fakeClients) LocalID() uint                   { return 0 }

type fakeTenants struct{ backedUp []string }

func (f *fakeTenants) ListTenantDatabases() ([]TenantDatabase, error) {
	return []TenantDatabase{{
		WorkspaceID: 1, Workspace: "prod",
		Instance: &models.DatabaseInstance{Engine: models.DBEnginePostgres},
		Database: &models.Database{Name: "appdb"},
	}}, nil
}

func (f *fakeTenants) ListTenantVolumes() ([]TenantVolume, error) {
	return []TenantVolume{{WorkspaceID: 1, Workspace: "prod", Name: "mb-vol-1-data"}}, nil
}

func (f *fakeTenants) BackupTenantDatabase(_ context.Context, td TenantDatabase, dest backup.Destination) (*models.Backup, error) {
	f.backedUp = append(f.backedUp, td.Database.Name)
	name := "appdb_20260731_120000.sql.gz"
	if dest.GPGPassphrase != "" {
		name += ".gpg"
	}
	return &models.Backup{Status: models.BackupCompleted, Filename: name, SizeBytes: 128}, nil
}

func (f *fakeTenants) RestoreTenantDatabase(context.Context, TenantDatabase, backup.Destination, string) error {
	return nil
}

func newTestService(t *testing.T, env config.PlatformBackupConfig) (*Service, *fakeDocker) {
	t.Helper()
	db := testDB(t)
	dc := &fakeDocker{}
	svc := NewService(
		repositories.NewPlatformBackupRepository(db),
		repositories.NewPlatformBackupSetRepository(db),
		repositories.NewPlatformBackupSettingsRepository(db),
		fakeClients{dc: dc},
		DBConn{Host: "miabi-postgres", Port: 5432, Name: "miabi", User: "miabi", Password: "pw"},
		"miabi",
	)
	svc.SetEnv(env)
	svc.SetFingerprinter(crypto.DeriveToken)
	svc.SetKeyFingerprinter(crypto.DeriveTokenFrom)
	svc.SetTenantSource(&fakeTenants{})
	svc.SetIdentitySource(func() (*dr.Identity, error) {
		return &dr.Identity{
			InstallID:     "mbi_test",
			MiabiVersion:  "1.0.0",
			EncryptionKey: "the-master-key",
			JWTSecret:     "jwt",
			Domain:        "miabi.example.com",
			CreatedAt:     time.Unix(1_760_000_000, 0).UTC(),
		}, nil
	})
	return svc, dc
}

// The whole flow, encrypted: every artifact completes, the set finalizes, and the
// manifest and identity envelope are readable from the bucket afterwards.
func TestRecoveryPointEndToEnd(t *testing.T) {
	env := s3ForTest(t)
	env.Passphrase = "correct-horse-9!"
	env.RootPath = fmt.Sprintf("it-%d", time.Now().UnixNano())

	svc, dc := newTestService(t, env)
	if _, err := svc.SaveSettings(SaveInput{
		S3Enabled: true, IncludeTenantData: true, Volumes: []string{"mb-node-gateway-providers"},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	set, err := svc.CreateSet(context.Background(), "manual")
	if err != nil {
		t.Fatalf("CreateSet: %v", err)
	}

	if set.Status != models.BackupCompleted {
		t.Fatalf("set status = %s, error = %q", set.Status, set.Error)
	}
	if !set.Encrypted || !set.IdentitySealed {
		t.Errorf("expected an encrypted, identity-sealed set: %+v", set)
	}

	// identity + control-plane database + platform volume + tenant db + tenant volume
	if len(set.Items) != 5 {
		t.Fatalf("got %d artifacts, want 5: %+v", len(set.Items), set.Items)
	}
	seen := map[models.PlatformBackupSubject]bool{}
	for _, it := range set.Items {
		if it.Status != models.BackupCompleted {
			t.Errorf("artifact %s failed: %s", it.Subject, it.Error)
		}
		if it.Filename == "" {
			t.Errorf("artifact %s completed with no filename", it.Subject)
		}
		if it.Subject != models.PlatformBackupIdentity && !strings.HasSuffix(it.Filename, ".gpg") {
			t.Errorf("artifact %s is not the encrypted name: %s", it.Subject, it.Filename)
		}
		seen[it.Subject] = true
	}
	for _, want := range []models.PlatformBackupSubject{
		models.PlatformBackupIdentity, models.PlatformBackupDatabase, models.PlatformBackupVolume,
		models.PlatformBackupTenantDatabase, models.PlatformBackupTenantVolume,
	} {
		if !seen[want] {
			t.Errorf("no %s artifact in the recovery point", want)
		}
	}

	// The helpers must have been told to encrypt.
	for _, run := range dc.runs {
		var hasPass bool
		for _, kv := range run.Env {
			if strings.HasPrefix(kv, "GPG_PASSPHRASE=") {
				hasPass = true
			}
		}
		if !hasPass {
			t.Errorf("helper %q ran without GPG_PASSPHRASE", run.Name)
		}
	}

	// Everything above is Miabi's own record. What matters is the bucket.
	store, err := blob.New(blob.Config{
		Endpoint: env.S3Endpoint, Bucket: env.S3Bucket, Region: env.S3Region,
		AccessKey: env.S3AccessKey, SecretKey: env.S3SecretKey, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	ctx := context.Background()

	body, err := store.GetBytes(ctx, dr.ManifestObject(env.RootPath, set.Ref))
	if err != nil {
		t.Fatalf("the recovery point published no manifest: %v", err)
	}
	man, err := dr.DecodeManifest(body)
	if err != nil {
		t.Fatalf("the published manifest does not decode: %v", err)
	}
	if man.Ref != set.Ref || man.Schema != dr.ManifestSchema {
		t.Errorf("manifest ref/schema = %s/%d", man.Ref, man.Schema)
	}
	if len(man.Artifacts) != 5 {
		t.Errorf("manifest lists %d artifacts, want 5", len(man.Artifacts))
	}
	if man.DatabaseArtifact() == nil {
		t.Error("manifest has no control-plane dump")
	}
	if len(man.TenantArtifacts()) != 2 {
		t.Errorf("manifest lists %d tenant artifacts, want 2", len(man.TenantArtifacts()))
	}

	sealed, err := store.GetBytes(ctx, dr.IdentityObject(env.RootPath, set.Ref))
	if err != nil {
		t.Fatalf("the identity envelope is not in the bucket: %v", err)
	}
	identity, err := dr.Open(sealed, env.Passphrase)
	if err != nil {
		t.Fatalf("the identity envelope does not open with the configured passphrase: %v", err)
	}
	if identity.EncryptionKey != "the-master-key" {
		t.Errorf("identity carries the wrong key: %q", identity.EncryptionKey)
	}
	// The fingerprint on the set must match the key inside the envelope, or a
	// restore would refuse it.
	if got := crypto.DeriveTokenFrom(identity.EncryptionKey, models.KEKFingerprintLabel); got != set.KEKFingerprint {
		t.Errorf("KEK fingerprint mismatch: envelope %q vs set %q", got, set.KEKFingerprint)
	}

	// Verify, scoped to what this test really uploads. The dump and the archives are produced by a fake helper
	// that writes nothing, so their absence from the bucket is expected here; the identity envelope and the
	// manifest are written by Miabi itself and are the parts worth asserting.
	rep, err := svc.VerifySet(ctx, set, env.Passphrase)
	if err != nil {
		t.Fatalf("VerifySet: %v", err)
	}
	if !rep.IdentityOpened {
		t.Errorf("verify could not open the identity envelope: %+v", rep.Findings)
	}
	if !rep.KEKMatches {
		t.Error("verify says the envelope's key does not match the set — a restore would refuse this recovery point")
	}
}

// The same flow with no passphrase anywhere: a recovery point must still be
// produced, unencrypted and without an envelope, rather than failing.
func TestRecoveryPointWithoutPassphrase(t *testing.T) {
	env := s3ForTest(t)
	env.RootPath = fmt.Sprintf("it-nopass-%d", time.Now().UnixNano())

	svc, _ := newTestService(t, env)
	if _, err := svc.SaveSettings(SaveInput{S3Enabled: true, IncludeTenantData: true}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	set, err := svc.CreateSet(context.Background(), "manual")
	if err != nil {
		t.Fatalf("CreateSet without a passphrase: %v", err)
	}
	if set.Status != models.BackupCompleted {
		t.Fatalf("set status = %s, error = %q", set.Status, set.Error)
	}
	if set.Encrypted || set.IdentitySealed {
		t.Errorf("no passphrase, yet the set claims encrypted=%v sealed=%v", set.Encrypted, set.IdentitySealed)
	}
	for _, it := range set.Items {
		if it.Subject == models.PlatformBackupIdentity {
			t.Error("an identity envelope was produced with no passphrase to seal it")
		}
		if it.Status != models.BackupCompleted {
			t.Errorf("artifact %s failed: %s", it.Subject, it.Error)
		}
		if strings.HasSuffix(it.Filename, ".gpg") {
			t.Errorf("artifact %s claims to be encrypted: %s", it.Subject, it.Filename)
		}
	}

	// It still publishes a manifest — that is what makes it discoverable.
	store, _ := blob.New(blob.Config{
		Endpoint: env.S3Endpoint, Bucket: env.S3Bucket, Region: env.S3Region,
		AccessKey: env.S3AccessKey, SecretKey: env.S3SecretKey, ForcePathStyle: true,
	})
	if _, err := store.GetBytes(context.Background(), dr.ManifestObject(env.RootPath, set.Ref)); err != nil {
		t.Fatalf("no manifest published for an unencrypted recovery point: %v", err)
	}
}

// A stored "encrypt" with the passphrase since removed must degrade to an
// unencrypted recovery point, not to no recovery point at all.
func TestEncryptionDegradesWhenThePassphraseIsGone(t *testing.T) {
	env := s3ForTest(t)
	env.RootPath = fmt.Sprintf("it-degrade-%d", time.Now().UnixNano())

	svc, _ := newTestService(t, env)
	// The stored row is written directly: SaveSettings refuses this combination,
	// which is the point — it is reachable only as leftover state from a
	// passphrase that has since been removed.
	if _, err := svc.SaveSettings(SaveInput{S3Enabled: true}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	raw, err := svc.rawSettings()
	if err != nil {
		t.Fatal(err)
	}
	raw.EncryptBackups = true // the passphrase that justified this is gone
	if err := svc.settings.Upsert(raw); err != nil {
		t.Fatal(err)
	}

	set, err := svc.CreateSet(context.Background(), "manual")
	if err != nil {
		t.Fatalf("CreateSet degraded: %v", err)
	}
	if set.Status != models.BackupCompleted {
		t.Fatalf("set status = %s, error = %q", set.Status, set.Error)
	}
	if set.Encrypted {
		t.Error("the set claims to be encrypted with no passphrase available")
	}
}

// Discovery reads the BUCKET, not this platform's database — which is what makes it usable after a
// rebuild, when the database knows only the recovery points its own dump contained. This takes a recovery
// point, forgets it locally, and checks it is still findable and adoptable.
func TestDiscoverAndImportFromTheBucket(t *testing.T) {
	env := s3ForTest(t)
	env.Passphrase = "correct-horse-9!"
	env.RootPath = fmt.Sprintf("it-disc-%d", time.Now().UnixNano())

	svc, _ := newTestService(t, env)
	if _, err := svc.SaveSettings(SaveInput{S3Enabled: true, IncludeTenantData: true}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	set, err := svc.CreateSet(context.Background(), "manual")
	if err != nil {
		t.Fatalf("CreateSet: %v", err)
	}
	if set.Status != models.BackupCompleted {
		t.Fatalf("set status = %s: %s", set.Status, set.Error)
	}

	ctx := context.Background()

	// The fake helper reports artifact names without uploading anything, so the
	// objects are placed here — discovery checks the bucket, and that check is
	// half of what it reports. One artifact is deliberately left absent.
	store, err := blob.New(blob.Config{
		Endpoint: env.S3Endpoint, Bucket: env.S3Bucket, Region: env.S3Region,
		AccessKey: env.S3AccessKey, SecretKey: env.S3SecretKey, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var absent string
	for i := range set.Items {
		it := &set.Items[i]
		if it.Subject == models.PlatformBackupIdentity || it.Filename == "" {
			continue
		}
		if it.Subject == models.PlatformBackupTenantVolume && absent == "" {
			absent = it.Filename // leave this one out of the bucket
			continue
		}
		if err := store.Put(ctx, objectKey(it), []byte("artifact")); err != nil {
			t.Fatal(err)
		}
	}

	found, err := svc.DiscoverSets(ctx)
	if err != nil {
		t.Fatalf("DiscoverSets: %v", err)
	}
	var got *DiscoveredSet
	for i := range found {
		if found[i].Ref == set.Ref {
			got = &found[i]
		}
	}
	if got == nil {
		t.Fatalf("the recovery point just taken was not discovered: %+v", found)
	}
	if !got.Known || got.SetID != set.ID {
		t.Errorf("a recovery point in this platform's own database was not marked known: %+v", got)
	}
	if got.Foreign {
		t.Error("this platform's own recovery point was marked foreign")
	}

	// The control-plane dump and the identity envelope must never be offered.
	var offeredControlPlane, offeredIdentity, restorable int
	for _, a := range got.Artifacts {
		switch a.Subject {
		case "database":
			if a.Restorable {
				offeredControlPlane++
			}
		case "identity":
			if a.Restorable {
				offeredIdentity++
			}
		}
		if a.Restorable {
			restorable++
		}
	}
	if offeredControlPlane > 0 || offeredIdentity > 0 {
		t.Errorf("dashboard offered %d control-plane and %d identity artifacts", offeredControlPlane, offeredIdentity)
	}
	if restorable == 0 {
		t.Error("nothing was offered as restorable; tenant data should be")
	}

	// The artifact left out of the bucket must be reported absent and not
	// offered: selecting it would fail after a wait, for a reason knowable now.
	for _, a := range got.Artifacts {
		if a.File != absent {
			continue
		}
		if a.Present {
			t.Errorf("%s is not in the bucket but was reported present", a.File)
		}
		if a.Restorable {
			t.Errorf("%s is missing from the bucket but was offered as restorable", a.File)
		}
	}

	// Now forget it locally — a rebuilt platform's position — and import it back.
	if err := svc.sets.Delete(set.ID); err != nil {
		t.Fatal(err)
	}
	again, err := svc.DiscoverSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range again {
		if d.Ref == set.Ref && d.Known {
			t.Error("a forgotten recovery point still reports as known")
		}
	}

	imported, err := svc.ImportSet(ctx, set.Ref)
	if err != nil {
		t.Fatalf("ImportSet: %v", err)
	}
	if imported.Ref != set.Ref || len(imported.Items) == 0 {
		t.Fatalf("import produced nothing usable: %+v", imported)
	}
	if imported.KEKFingerprint != set.KEKFingerprint {
		t.Error("the imported recovery point lost its key fingerprint; a restore could not check it")
	}

	// Import is idempotent — an operator clicking twice must not get two rows.
	twice, err := svc.ImportSet(ctx, set.Ref)
	if err != nil {
		t.Fatalf("second ImportSet: %v", err)
	}
	if twice.ID != imported.ID {
		t.Errorf("import created a second row: %d then %d", imported.ID, twice.ID)
	}
}
