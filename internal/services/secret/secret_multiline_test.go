// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secret

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// certificatePEM is the shape that prompted this test: several lines, a trailing
// newline, and content that must survive byte for byte or the certificate it
// carries is unusable.
const certificatePEM = `-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJAKZ0Z0Z0Z0Z0MA0GCSqGSIb3DQEBCwUAMBQxEjAQBgNVBAMMCWxv
Y2FsaG9zdDAeFw0yNjAxMDEwMDAwMDBaFw0yNzAxMDEwMDAwMDBaMBQxEjAQBgNV
BAMMCWxvY2FsaG9zdDBcMA0GCSqGSIb3DQEBAQUAA0sAMEgCQQDZ8Z8Z8Z8Z8Z8Z
-----END CERTIFICATE-----
`

func multilineTestService(t *testing.T) *Service {
	t.Helper()
	// A DSN per test: the shared cache would otherwise carry one test's table,
	// and its rows, into the next.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The Secret model's UIDModel carries a Postgres `gen_random_uuid()` default
	// that sqlite's AutoMigrate can't parse, so create a compatible table by hand.
	if err := db.Exec(`CREATE TABLE secrets (
		uid text, id integer PRIMARY KEY AUTOINCREMENT, workspace_id integer,
		name text, display_name text, value_enc text, description text,
		version integer DEFAULT 1, updated_by_id integer, managed integer DEFAULT 0,
		owner_kind text, owner_id integer, metadata text, created_at datetime, updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return NewService(repositories.NewSecretRepository(db))
}

// A certificate stored and read back must be the same bytes: line breaks are
// part of the value, not formatting.
func TestMultilineValueRoundTrips(t *testing.T) {
	svc := multilineTestService(t)

	created, err := svc.Create(1, "tls_cert", certificatePEM, "server certificate", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	revealed, err := svc.Reveal(1, created.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if revealed != certificatePEM {
		t.Errorf("value changed in storage:\n got %q\nwant %q", revealed, certificatePEM)
	}
	if got, want := strings.Count(revealed, "\n"), strings.Count(certificatePEM, "\n"); got != want {
		t.Errorf("newlines = %d, want %d", got, want)
	}
	if !strings.HasSuffix(revealed, "\n") {
		t.Error("the trailing newline was dropped")
	}
}

// Rotating to a new certificate keeps the same guarantee.
func TestMultilineValueSurvivesRotation(t *testing.T) {
	svc := multilineTestService(t)

	created, err := svc.Create(1, "tls_cert_rotated", "placeholder", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(1, created.ID, certificatePEM, "", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	revealed, err := svc.Reveal(1, created.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if revealed != certificatePEM {
		t.Errorf("value changed on rotation:\n got %q\nwant %q", revealed, certificatePEM)
	}
}

// A value that is only whitespace means "keep the stored value" on update, and
// must not be mistaken for a rotation to blank.
func TestBlankUpdateKeepsStoredValue(t *testing.T) {
	svc := multilineTestService(t)

	created, err := svc.Create(1, "tls_cert_kept", certificatePEM, "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(1, created.ID, "  \n ", "updated description", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	revealed, err := svc.Reveal(1, created.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if revealed != certificatePEM {
		t.Errorf("a blank update changed the stored value:\n got %q", revealed)
	}
}

// A certificate referenced from an env var must reach the container as the
// certificate, not as its first line. ResolveAll is the substitution every
// deploy and job goes through.
func TestMultilineValueResolvesIntoEnv(t *testing.T) {
	svc := multilineTestService(t)

	if _, err := svc.Create(1, "tls_cert", certificatePEM, "", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	env := []string{"TLS_CERT=" + Ref("tls_cert"), "PLAIN=value"}
	resolved, err := svc.ResolveAll(1, env)
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}

	want := "TLS_CERT=" + certificatePEM
	if resolved[0] != want {
		t.Errorf("resolved env changed the value:\n got %q\nwant %q", resolved[0], want)
	}
	if resolved[1] != "PLAIN=value" {
		t.Errorf("unrelated entry = %q", resolved[1])
	}
}

// A secret holding regex replacement syntax must survive substitution literally:
// the resolver uses ReplaceAllStringFunc precisely so "$1" in a value is not
// expanded into a capture group.
func TestSecretValueWithDollarIsLiteral(t *testing.T) {
	svc := multilineTestService(t)

	const tricky = "p$1ss$${word}$0"
	if _, err := svc.Create(1, "tricky", tricky, "", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved, err := svc.ResolveAll(1, []string{"P=" + Ref("tricky")})
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	if resolved[0] != "P="+tricky {
		t.Errorf("value was expanded: got %q, want %q", resolved[0], "P="+tricky)
	}
}
