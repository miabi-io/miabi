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

package alerting

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func withServiceAccount() []models.WorkspaceMember {
	return append(members(), models.WorkspaceMember{
		UserID: 4, Role: models.WorkspaceRoleAdmin, User: models.User{ID: 4, Kind: models.UserKindService},
	})
}

// Nobody reads a service account's inbox, so a delivery to it is one nobody sees.
func TestServiceAccountsReceiveNoWorkspaceNotifications(t *testing.T) {
	eng := NewEngine(nil, nil, fakeMembers{m: withServiceAccount()}, fakeNamer{}, nil, NewMemoryCounter(nil))
	got, err := eng.recipients(9, models.WorkspaceRoleDeveloper, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, 4) {
		t.Errorf("alert recipients %v include the service account", got)
	}

	inbox := &capturedInbox{}
	if err := NewWorkspaceNotifier(stubMembers{withServiceAccount()}, inbox, nil).
		NotifyWorkspace(9, models.WorkspaceRoleDeveloper, models.Notification{Title: "Recovery point completed"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range inbox.rows {
		if r.UserID == 4 {
			t.Error("the backup report was delivered to the service account")
		}
	}
	if len(inbox.rows) != 2 {
		t.Errorf("delivered %d rows, want the owner and the developer", len(inbox.rows))
	}
}
