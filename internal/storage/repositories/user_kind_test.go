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

package repositories

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Service accounts must not decide the last admin, receive platform alerts, or affect first-sign-up detection.
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
