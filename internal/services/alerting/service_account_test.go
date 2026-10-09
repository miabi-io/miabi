// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A service account holds a workspace role, but nobody reads its inbox: an alert or a backup report
// sent to it is a delivery nobody sees.
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
