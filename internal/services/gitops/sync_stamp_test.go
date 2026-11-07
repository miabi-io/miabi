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

package gitops

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// A no-op poll must not stamp LastSyncedAt, or the panel hides that nothing has been applied.
func TestANoOpReconcileIsNotASync(t *testing.T) {
	shipped := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	src := &models.GitSource{
		LastSyncedCommit:  "aaaaaaaaaaaa",
		LastSyncedAuthor:  "jonas",
		LastSyncedSubject: "add the staging app",
		LastSyncedAt:      &shipped,
	}

	// A month of polling later: Git has moved on, but nothing in the manifests changed.
	recordSync(src, commitInfo{Hash: "bbbbbbbbbbbb", Author: "someone", Subject: "fix a typo in the README"}, 0,
		time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC))

	if !src.LastSyncedAt.Equal(shipped) {
		t.Errorf("LastSyncedAt moved to %v on a reconcile that applied nothing", src.LastSyncedAt)
	}
	if src.LastSyncedCommit != "aaaaaaaaaaaa" || src.LastSyncedSubject != "add the staging app" {
		t.Errorf("the synced commit was overwritten by one that changed nothing: %s / %q",
			src.LastSyncedCommit, src.LastSyncedSubject)
	}
}

// ...and it must still move when something really is applied, or the fix would just freeze the field.
func TestAnAppliedChangeIsASync(t *testing.T) {
	old := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	src := &models.GitSource{LastSyncedCommit: "aaaaaaaaaaaa", LastSyncedAt: &old}

	recordSync(src, commitInfo{Hash: "bbbbbbbbbbbb", Author: "jonas", Subject: "scale the api to 3"}, 2, now)

	if !src.LastSyncedAt.Equal(now) {
		t.Errorf("LastSyncedAt = %v, want %v", src.LastSyncedAt, now)
	}
	if src.LastSyncedCommit != "bbbbbbbbbbbb" || src.LastSyncedAuthor != "jonas" ||
		src.LastSyncedSubject != "scale the api to 3" {
		t.Errorf("the applied commit was not recorded: %+v", src)
	}
}

// The whole group moves together, or the detail page shows one commit's sha next to another's message.
func TestSyncStampIsAllOrNothing(t *testing.T) {
	src := &models.GitSource{}
	recordSync(src, commitInfo{Hash: "abc", Author: "jonas", Subject: "first"}, 0, time.Now())
	if src.LastSyncedCommit != "" || src.LastSyncedAuthor != "" || src.LastSyncedSubject != "" || src.LastSyncedAt != nil {
		t.Errorf("a no-op reconcile wrote part of the group: %+v", src)
	}
}

// Last check moves on every reconcile, showing the source still runs while Last sync stands still.
func TestEveryReconcileIsACheck(t *testing.T) {
	src := &models.GitSource{}
	markChecked(src, "abc123")
	if src.LastCheckedAt == nil {
		t.Fatal("LastCheckedAt was not stamped")
	}
	if src.LastCheckedCommit != "abc123" {
		t.Errorf("LastCheckedCommit = %q, want abc123", src.LastCheckedCommit)
	}

	first := *src.LastCheckedAt

	markChecked(src, "")
	if !src.LastCheckedAt.After(first) && !src.LastCheckedAt.Equal(first) {
		t.Error("a failed check did not stamp LastCheckedAt")
	}
	if src.LastCheckedCommit != "abc123" {
		t.Errorf("a failed fetch erased the last known revision: %q", src.LastCheckedCommit)
	}
}
