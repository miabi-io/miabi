// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"strconv"
	"testing"
)

func TestIDByRefResolvesIDUIDAndName(t *testing.T) {
	s, _ := scopeTestService(t)
	web, err := s.Create(1, Input{Name: "web", Spec: commandOnlySpec})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(2, Input{Name: "elsewhere", Spec: commandOnlySpec})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{strconv.FormatUint(uint64(web.ID), 10), web.UID, "web", " WEB "} {
		got, err := s.IDByRef(1, ref)
		if err != nil || got != web.ID {
			t.Errorf("IDByRef(1, %q) = (%d, %v), want %d", ref, got, err, web.ID)
		}
	}
	for _, ref := range []string{"missing", "elsewhere", other.UID, strconv.FormatUint(uint64(other.ID), 10), ""} {
		if got, err := s.IDByRef(1, ref); err == nil {
			t.Errorf("IDByRef(1, %q) = %d, want not found", ref, got)
		}
	}
}

func TestIDByRefFallsBackToAnAllDigitName(t *testing.T) {
	s, _ := scopeTestService(t)
	p, err := s.Create(1, Input{Name: "2026", Spec: commandOnlySpec})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.IDByRef(1, "2026")
	if err != nil || got != p.ID {
		t.Fatalf("IDByRef(1, %q) = (%d, %v), want %d", "2026", got, err, p.ID)
	}
}
