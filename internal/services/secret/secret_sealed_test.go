// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secret

import (
	"errors"
	"testing"
)

func TestApplySealedCreatesThenOnlyBumpsOnARealChange(t *testing.T) {
	svc := multilineTestService(t)
	sec, err := svc.ApplySealed(1, "db", "pw-1", "fp-a", 1)
	if err != nil || sec.Version != 1 || sec.SealedFP != "fp-a" || sec.SealedKeyVersion != 1 {
		t.Fatalf("create = %+v, %v", sec, err)
	}

	sec, err = svc.ApplySealed(1, "db", "pw-1", "fp-b", 2)
	if err != nil || sec.Version != 1 || sec.SealedFP != "fp-b" || sec.SealedKeyVersion != 2 {
		t.Fatalf("re-seal of the same value must not bump the version: %+v, %v", sec, err)
	}

	sec, err = svc.ApplySealed(1, "db", "pw-2", "fp-c", 2)
	if err != nil || sec.Version != 2 {
		t.Fatalf("a new value must bump the version: %+v, %v", sec, err)
	}
	if got, _ := svc.Reveal(1, sec.ID); got != "pw-2" {
		t.Fatalf("stored value = %q", got)
	}
}

func TestHandSetValueClearsTheSealedMarker(t *testing.T) {
	svc := multilineTestService(t)
	sec, _ := svc.ApplySealed(1, "db", "pw-1", "fp-a", 1)
	sec, err := svc.Update(1, sec.ID, "typed-in-the-ui", "", nil)
	if err != nil || sec.SealedFP != "" || sec.SealedKeyVersion != 0 {
		t.Fatalf("Update = %+v, %v", sec, err)
	}
}

func TestApplySealedRefusesAManagedSecret(t *testing.T) {
	svc := multilineTestService(t)
	if _, err := svc.UpsertOwned(1, "database", 9, "db_url", "postgres://", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplySealed(1, "db_url", "x", "fp", 1); !errors.Is(err, ErrManaged) {
		t.Fatalf("want ErrManaged, got %v", err)
	}
}
