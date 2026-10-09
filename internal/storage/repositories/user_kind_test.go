// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Service accounts are not people: they must not decide who is the last admin, receive platform alerts,
// or make a sign-up look like it is not the first.
func TestServiceAccountsAreNotCountedAsPeople(t *testing.T) {
	r := NewUserRepository(newUserOwnerDB(t))
	ws := uint(1)
	admin := &models.User{Username: "admin", Email: "admin@example.com", Role: models.SystemRoleAdmin, Active: true, Kind: models.UserKindHuman}
	// Promoted before promotion was refused: the role column still says admin.
	sa := &models.User{Username: "sa-ci", Email: "sa-ci@" + models.ServiceAccountEmailDomain, Role: models.SystemRoleAdmin,
		Active: true, Kind: models.UserKindService, ServiceWorkspaceID: &ws}
	for _, u := range []*models.User{admin, sa} {
		if err := r.Create(u); err != nil {
			t.Fatal(err)
		}
	}

	if n, _ := r.Count(); n != 1 {
		t.Errorf("Count = %d, want 1 person", n)
	}
	if n, _ := r.CountByRole(models.SystemRoleAdmin); n != 1 {
		t.Errorf("admins = %d, want 1: a service account would let the last human admin be demoted", n)
	}
	if ids, _ := r.ListAdminIDs(); !slices.Equal(ids, []uint{admin.ID}) {
		t.Errorf("admin alert recipients = %v, want only the human admin %d", ids, admin.ID)
	}

	for _, tc := range []struct {
		kind string
		want []uint
	}{
		{"", []uint{sa.ID, admin.ID}},
		{models.UserKindHuman, []uint{admin.ID}},
		{models.UserKindService, []uint{sa.ID}},
	} {
		users, total, err := r.List("", tc.kind, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []uint
		for _, u := range users {
			got = append(got, u.ID)
		}
		slices.Sort(got)
		want := slices.Clone(tc.want)
		slices.Sort(want)
		if !slices.Equal(got, want) || total != int64(len(want)) {
			t.Errorf("List(kind=%q) = %v (total %d), want %v", tc.kind, got, total, want)
		}
	}
}
