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
package groupsync

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type key struct{ ws, user uint }

type fakeWS struct {
	byName  map[string]*models.Workspace
	members map[key]*models.WorkspaceMember
}

func (f *fakeWS) FindByName(name string) (*models.Workspace, error) {
	if w, ok := f.byName[name]; ok {
		return w, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeWS) FindMember(ws, user uint) (*models.WorkspaceMember, error) {
	if m, ok := f.members[key{ws, user}]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeWS) AddMember(m *models.WorkspaceMember) error {
	f.members[key{m.WorkspaceID, m.UserID}] = m
	return nil
}
func (f *fakeWS) UpdateMemberRole(ws, user uint, role models.WorkspaceRole) error {
	f.members[key{ws, user}].Role = role
	return nil
}
func (f *fakeWS) SetMemberManagedBy(ws, user uint, manager string) error {
	f.members[key{ws, user}].ManagedBy = manager
	return nil
}
func (f *fakeWS) RemoveMember(ws, user uint) error {
	delete(f.members, key{ws, user})
	return nil
}
func (f *fakeWS) CountOwners(ws uint) (int64, error) {
	var n int64
	for k, m := range f.members {
		if k.ws == ws && m.Role == models.WorkspaceRoleOwner {
			n++
		}
	}
	return n, nil
}
func (f *fakeWS) ListManagedBy(user uint, manager string) ([]models.WorkspaceMember, error) {
	var out []models.WorkspaceMember
	for k, m := range f.members {
		if k.user == user && m.ManagedBy == manager {
			out = append(out, *m)
		}
	}
	return out, nil
}

type fakeUsers struct{ admins int64 }

func (f *fakeUsers) Update(*models.User) error { return nil }
func (f *fakeUsers) CountByRole(models.SystemRole) (int64, error) { return f.admins, nil }

const user = uint(10)

func setup() (*Syncer, *fakeWS, *[]Event) {
	ws := &fakeWS{
		byName: map[string]*models.Workspace{
			"acme": {ID: 1, Name: "acme"}, "beta": {ID: 2, Name: "beta"}, "manual": {ID: 3, Name: "manual"},
			"system": {ID: 4, Name: "system", System: true},
		},
		members: map[key]*models.WorkspaceMember{
			{3, user}: {WorkspaceID: 3, UserID: user, Role: models.WorkspaceRoleViewer},
			{1, 99}:   {WorkspaceID: 1, UserID: 99, Role: models.WorkspaceRoleOwner},
		},
	}
	var events []Event
	s := New(ws, &fakeUsers{admins: 2}, nil, func(_ uint, e Event) { events = append(events, e) })
	return s, ws, &events
}

func in(groups ...string) Input {
	return Input{User: &models.User{ID: user}, Manager: "console", Groups: groups, Prefix: "miabi"}
}

func TestStructuredGroupsGrantAndRevoke(t *testing.T) {
	s, ws, _ := setup()
	s.Reconcile(in("miabi:acme:developer", "miabi:beta:admin", "miabi:system:owner", "miabi:nope:admin", "other:acme:owner", "miabi:acme:boss"))
	if m := ws.members[key{1, user}]; m == nil || m.Role != models.WorkspaceRoleDeveloper || m.ManagedBy != "console" {
		t.Fatalf("acme: %+v", m)
	}
	if m := ws.members[key{2, user}]; m == nil || m.Role != models.WorkspaceRoleAdmin {
		t.Fatalf("beta: %+v", m)
	}
	if ws.members[key{4, user}] != nil {
		t.Fatal("the system workspace is never granted by groups")
	}
	s.Reconcile(in("miabi:acme:admin"))
	if m := ws.members[key{1, user}]; m.Role != models.WorkspaceRoleAdmin {
		t.Fatalf("role change: %+v", m)
	}
	if ws.members[key{2, user}] != nil {
		t.Fatal("a managed membership whose group is gone must be removed")
	}
}

func TestManualMembershipUntouched(t *testing.T) {
	s, ws, _ := setup()
	s.Reconcile(in("miabi:manual:owner"))
	if m := ws.members[key{3, user}]; m.Role != models.WorkspaceRoleViewer || m.ManagedBy != "" {
		t.Fatalf("a membership made by hand changed: %+v", m)
	}
	s.Reconcile(in())
	if ws.members[key{3, user}] == nil {
		t.Fatal("a membership made by hand was removed")
	}
}

func TestLastOwnerKept(t *testing.T) {
	s, ws, _ := setup()
	delete(ws.members, key{1, 99})
	s.Reconcile(in("miabi:acme:owner"))
	s.Reconcile(in())
	if m := ws.members[key{1, user}]; m == nil || m.Role != models.WorkspaceRoleOwner {
		t.Fatal("the last owner must never be removed by group sync")
	}
	s.Reconcile(in("miabi:acme:viewer"))
	if m := ws.members[key{1, user}]; m.Role != models.WorkspaceRoleOwner {
		t.Fatal("the last owner must never be demoted by group sync")
	}
}

func TestOwnerLimitGrantsAdmin(t *testing.T) {
	s, ws, events := setup()
	s.canOwn = func(uint) error { return errors.New("owned-workspace limit reached") }
	s.Reconcile(in("miabi:acme:owner"))
	if m := ws.members[key{1, user}]; m == nil || m.Role != models.WorkspaceRoleAdmin {
		t.Fatalf("limit refused owner: %+v", m)
	}
	found := false
	for _, e := range *events {
		found = found || e.Action == "sso.group_sync.owner_downgraded"
	}
	if !found {
		t.Fatal("the downgrade must be recorded")
	}
}

func TestExplicitRules(t *testing.T) {
	s, ws, _ := setup()
	acme := uint(1)
	u := &models.User{ID: user, Role: models.SystemRoleUser}
	rules := []Rule{{Group: "Ops", SystemAdmin: true}, {Group: "devs", WorkspaceID: &acme, Role: models.WorkspaceRoleDeveloper}}
	s.Reconcile(Input{User: u, Manager: "okta", Groups: []string{"ops", "DEVS"}, Rules: rules})
	if u.Role != models.SystemRoleAdmin {
		t.Fatal("a SystemAdmin rule must promote")
	}
	if m := ws.members[key{1, user}]; m == nil || m.Role != models.WorkspaceRoleDeveloper {
		t.Fatalf("explicit workspace rule: %+v", m)
	}
	s.Reconcile(Input{User: u, Manager: "okta", Groups: nil, Rules: rules})
	if u.Role != models.SystemRoleUser || ws.members[key{1, user}] != nil {
		t.Fatal("leaving the groups must demote and remove")
	}
}
