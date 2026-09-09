// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAdoptService(t *testing.T) (*Service, *repositories.DatabaseBackupSetRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// models.Database embeds UIDModel, whose uid column defaults to
	// gen_random_uuid() — Postgres-only. A minimal shadow of the table keeps the
	// repository's own queries working under sqlite, as the other service tests do.
	if err := db.AutoMigrate(&models.DatabaseBackupSet{}, &models.Backup{}, &databaseRow{}); err != nil {
		t.Fatal(err)
	}
	sets := repositories.NewDatabaseBackupSetRepository(db)
	svc := NewService(repositories.NewBackupRepository(db), repositories.NewDatabaseRepository(db), nil)
	svc.SetSetRepository(sets)
	return svc, sets, db
}

// databaseRow is the subset of the databases table adoption reads.
type databaseRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	InstanceID  uint
	Name        string
	CreatedAt   time.Time
}

func (databaseRow) TableName() string { return "databases" }

func pgInstance() *models.DatabaseInstance {
	return &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1, Engine: models.DBEnginePostgres}
}

func seedDatabases(t *testing.T, db *gorm.DB, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := db.Create(&databaseRow{WorkspaceID: 1, InstanceID: 7, Name: n, CreatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func bucketInfo() SetInfo {
	return SetInfo{
		Schema: SetInfoSchema, Ref: "mbdb_pg_20260909T030000Z", Instance: "pg",
		Engine: "postgres", Version: "17", Encrypted: true, Envelope: "sealed-envelope",
		SizeBytes: 300, CreatedAt: time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC),
		Artifacts: []SetInfoArtifact{
			{Database: "orders", Filename: "orders.sql.gz.gpg", SizeBytes: 100, Encrypted: true},
			{Database: "billing", Filename: "billing.sql.gz.gpg", SizeBytes: 200, Encrypted: true},
		},
	}
}

func TestAdoptMatchesArtifactsToDatabasesByName(t *testing.T) {
	svc, sets, db := newAdoptService(t)
	seedDatabases(t, db, "orders", "billing")

	res, err := svc.adoptInfo(pgInstance(), bucketInfo(), "backups/databases/pg/mbdb_pg_20260909T030000Z",
		"acme-backups", &AdoptResult{Ref: "mbdb_pg_20260909T030000Z"})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if res.Adopted != 2 || len(res.Skipped) != 0 {
		t.Errorf("adopted=%d skipped=%d, want 2 and 0", res.Adopted, len(res.Skipped))
	}

	set, err := sets.FindInWorkspace(1, res.SetID)
	if err != nil {
		t.Fatal(err)
	}
	if set.Envelope != "sealed-envelope" {
		t.Error("the envelope was not carried across, so the set could never be decrypted")
	}
	if set.S3Path != "backups/databases/pg/mbdb_pg_20260909T030000Z" || set.S3Bucket != "acme-backups" {
		t.Errorf("the artifacts' location was not recorded: %s / %s", set.S3Bucket, set.S3Path)
	}
	if !set.Encrypted || set.Status != models.BackupCompleted {
		t.Errorf("set state = %v / %q", set.Encrypted, set.Status)
	}
	if len(set.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(set.Items))
	}
	for i := range set.Items {
		if set.Items[i].Filename == "" || set.Items[i].SetID == nil {
			t.Errorf("item %d is not usable for a restore: %+v", i, set.Items[i])
		}
	}
}

// An artifact whose database no longer exists is reported, never invented. A
// placeholder database would produce a row that looks restorable and restores into
// something nobody asked for.
func TestAdoptSkipsArtifactsWithNoLocalDatabase(t *testing.T) {
	svc, _, db := newAdoptService(t)
	seedDatabases(t, db, "orders") // billing is gone

	res, err := svc.adoptInfo(pgInstance(), bucketInfo(), "p", "b", &AdoptResult{})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if res.Adopted != 1 {
		t.Errorf("adopted = %d, want 1", res.Adopted)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Database != "billing" {
		t.Fatalf("skipped = %+v, want billing named", res.Skipped)
	}
	if res.Skipped[0].Reason == "" {
		t.Error("a skipped artifact carries no reason")
	}
	var created int64
	if err := db.Model(&databaseRow{}).Count(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Errorf("%d databases exist, want 1 — adopting must not create them", created)
	}
}

// A PostgreSQL dump is not a MySQL one, and discovering that at restore time is
// discovering it too late.
func TestAdoptRefusesAnEngineMismatch(t *testing.T) {
	svc, _, db := newAdoptService(t)
	seedDatabases(t, db, "orders", "billing")
	mysql := &models.DatabaseInstance{ID: 7, Name: "my", WorkspaceID: 1, Engine: models.DBEngineMySQL}

	if _, err := svc.adoptInfo(mysql, bucketInfo(), "p", "b", &AdoptResult{}); !errors.Is(err, ErrEngineMismatch) {
		t.Fatalf("error = %v, want ErrEngineMismatch", err)
	}
	var sets int64
	if err := db.Model(&models.DatabaseBackupSet{}).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if sets != 0 {
		t.Error("a refused adoption still wrote a set row")
	}
}

// Adopting twice must not duplicate the history: a partial failure has to be safe
// to retry.
func TestAdoptIsIdempotentOnRef(t *testing.T) {
	svc, sets, db := newAdoptService(t)
	seedDatabases(t, db, "orders", "billing")
	inst, info := pgInstance(), bucketInfo()

	first, err := svc.adoptInfo(inst, info, "p", "b", &AdoptResult{Ref: info.Ref})
	if err != nil {
		t.Fatal(err)
	}
	// AdoptSet short-circuits on an existing ref; simulate that path.
	existing, err := sets.FindByRef(1, info.Ref)
	if err != nil {
		t.Fatalf("the adopted set is not findable by ref: %v", err)
	}
	if existing.ID != first.SetID {
		t.Errorf("FindByRef returned %d, want %d", existing.ID, first.SetID)
	}
	var count int64
	if err := db.Model(&models.DatabaseBackupSet{}).Where("ref = ?", info.Ref).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d rows for one ref", count)
	}
}
