// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newKeyService(t *testing.T) (*auth.APIKeyService, *repositories.APIKeyRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.APIKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := repositories.NewAPIKeyRepository(db)
	return auth.NewAPIKeyService(repo), repo
}

func TestMintBothCredentials(t *testing.T) {
	svc, _ := newKeyService(t)
	m := NewCredentialMinter(svc, true)
	appID := uint(128)
	deadline := time.Now().Add(30 * time.Minute)

	creds, err := m.Mint(7 /*user*/, 42 /*ws*/, &appID, 57 /*run*/, deadline)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if creds.RegistryToken == "" || creds.JobToken == "" || creds.RegistryUser != registryUsername {
		t.Fatalf("creds incomplete: %+v", creds)
	}
	if len(creds.Secrets()) != 2 {
		t.Errorf("Secrets() = %v, want 2 values to mask", creds.Secrets())
	}

	// The registry token authenticates and carries exactly its ephemeral, app-
	// bound, registry read+write identity (read is required: a docker push HEADs
	// blobs to skip existing layers, and those checks need a read scope).
	key, err := svc.Verify(creds.RegistryToken)
	if err != nil {
		t.Fatalf("registry token should verify: %v", err)
	}
	if !key.Ephemeral || key.ApplicationID == nil || *key.ApplicationID != appID {
		t.Errorf("registry key not app-bound ephemeral: %+v", key)
	}
	if !key.HasScope(models.ScopeRegistryWrite) || !key.HasScope(models.ScopeRegistryRead) || key.HasScope(models.ScopeDeploy) {
		t.Errorf("registry key scopes = %v", key.Scopes)
	}
	// The job token carries deploy (and is app-bound too).
	jobKey, err := svc.Verify(creds.JobToken)
	if err != nil || !jobKey.HasScope(models.ScopeDeploy) {
		t.Errorf("job token should verify with deploy scope: %v / %+v", err, jobKey)
	}
}

func TestMintJobTokenDisabled(t *testing.T) {
	svc, _ := newKeyService(t)
	creds, err := NewCredentialMinter(svc, false).Mint(7, 42, nil, 58, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if creds.JobToken != "" {
		t.Errorf("job token should be withheld when disabled")
	}
	if creds.RegistryToken == "" || len(creds.Secrets()) != 1 {
		t.Errorf("registry credential still expected: %+v", creds)
	}
}

func TestRevokeKillsCredentials(t *testing.T) {
	svc, _ := newKeyService(t)
	m := NewCredentialMinter(svc, true)
	creds, err := m.Mint(7, 42, nil, 59, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	m.Revoke(creds)
	if _, err := svc.Verify(creds.RegistryToken); err == nil {
		t.Error("registry token must be invalid after Revoke")
	}
	if _, err := svc.Verify(creds.JobToken); err == nil {
		t.Error("job token must be invalid after Revoke")
	}
}

// Ephemeral job keys never appear in a user's API-keys list, and the orphan
// sweep removes expired ones.
func TestEphemeralKeysHiddenAndSwept(t *testing.T) {
	svc, repo := newKeyService(t)
	// One ordinary user key, plus a run's ephemeral pair.
	if _, _, err := svc.Create(7, ptrWS(42), "personal", nil, []string{models.ScopeRead}, nil); err != nil {
		t.Fatalf("create user key: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := NewCredentialMinter(svc, true).Mint(7, 42, nil, 60, past); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	list, err := repo.ListByUser(7)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 1 || list[0].Name != "personal" {
		t.Fatalf("ListByUser should hide ephemeral keys, got %d: %+v", len(list), list)
	}
	n, err := svc.SweepExpiredEphemeral(time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Errorf("sweep deleted %d, want 2 expired ephemeral keys", n)
	}
}

func ptrWS(u uint) *uint { return &u }

func TestSourceURLSecretsMasksTheCloneToken(t *testing.T) {
	got := sourceURLSecrets("https://x-access-token:ghp_a%2Fb@github.com/acme/app.git")
	want := map[string]bool{"ghp_a/b": true, "ghp_a%2Fb": true}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Fatalf("unexpected secret %q in %q", s, got)
		}
	}
	line := redact("remote: https://x-access-token:ghp_a%2Fb@github.com/acme/app.git", got)
	if strings.Contains(line, "ghp_") {
		t.Fatalf("token survived redaction: %q", line)
	}
	for _, u := range []string{"", "https://github.com/acme/app.git", "https://user@github.com/a.git", "::"} {
		if s := sourceURLSecrets(u); s != nil {
			t.Fatalf("%q: got %q, want none", u, s)
		}
	}
}
