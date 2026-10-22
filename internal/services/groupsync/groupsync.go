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

// Package groupsync applies an SSO provider's group claims to Miabi access at sign-in: explicit
// mappings (group to platform admin, or to a role in a fixed workspace) and structured groups
// "<prefix>:<workspace>:<role>". It only ever changes memberships it governs, so a member added by
// hand keeps their place, and it never leaves a workspace without an owner.
package groupsync

import (
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Workspaces is the membership storage group sync needs (*repositories.WorkspaceRepository).
type Workspaces interface {
	FindByName(name string) (*models.Workspace, error)
	FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error)
	AddMember(m *models.WorkspaceMember) error
	UpdateMemberRole(workspaceID, userID uint, role models.WorkspaceRole) error
	SetMemberManagedBy(workspaceID, userID uint, manager string) error
	RemoveMember(workspaceID, userID uint) error
	CountOwners(workspaceID uint) (int64, error)
	ListManagedBy(userID uint, manager string) ([]models.WorkspaceMember, error)
}

// Users promotes and demotes platform admins (*repositories.UserRepository).
type Users interface {
	Update(u *models.User) error
	CountByRole(role models.SystemRole) (int64, error)
}

// Rule is one explicit mapping.
type Rule struct {
	Group       string
	SystemAdmin bool
	WorkspaceID *uint
	Role        models.WorkspaceRole
}

// Input is one sign-in to reconcile.
type Input struct {
	User *models.User
	// Manager is the provider name recorded on the memberships this sync grants.
	Manager string
	Groups  []string
	Rules   []Rule
	// Prefix enables structured groups; empty ignores them.
	Prefix string
}

// Event reports a change for the audit log.
type Event struct {
	Action      string
	WorkspaceID uint
	Detail      map[string]any
}

// Syncer applies group claims.
type Syncer struct {
	workspaces Workspaces
	users      Users
	// canOwn reports whether a user may own one more workspace; nil allows it.
	canOwn func(userID uint) error
	// record receives every change; nil drops them.
	record func(userID uint, e Event)
}

// New builds a Syncer.
func New(workspaces Workspaces, users Users, canOwn func(userID uint) error, record func(userID uint, e Event)) *Syncer {
	return &Syncer{workspaces: workspaces, users: users, canOwn: canOwn, record: record}
}

func rank(r models.WorkspaceRole) int {
	switch r {
	case models.WorkspaceRoleOwner:
		return 4
	case models.WorkspaceRoleAdmin:
		return 3
	case models.WorkspaceRoleDeveloper:
		return 2
	case models.WorkspaceRoleViewer:
		return 1
	}
	return 0
}

func normalize(groups []string) map[string]bool {
	m := make(map[string]bool, len(groups))
	for _, g := range groups {
		if g = strings.ToLower(strings.TrimSpace(g)); g != "" {
			m[g] = true
		}
	}
	return m
}

// parseStructured reads "<prefix>:<workspace>:<role>"; ok is false for anything else.
func parseStructured(prefix, group string) (workspace string, role models.WorkspaceRole, ok bool) {
	parts := strings.Split(strings.TrimSpace(group), ":")
	if len(parts) != 3 || !strings.EqualFold(parts[0], prefix) || parts[1] == "" {
		return "", "", false
	}
	role = models.WorkspaceRole(strings.ToLower(parts[2]))
	if rank(role) == 0 {
		return "", "", false
	}
	return strings.ToLower(parts[1]), role, true
}

// Reconcile brings the user's governed memberships and admin role in line with their groups.
// Failures are logged, never returned: sign-in must not fail because a membership could not change.
func (s *Syncer) Reconcile(in Input) {
	if in.User == nil || in.Manager == "" {
		return
	}
	groups := normalize(in.Groups)
	desired := map[uint]models.WorkspaceRole{}
	governed := map[uint]bool{} // workspaces an explicit rule governs, whoever granted the membership
	want := func(wsID uint, role models.WorkspaceRole) {
		if cur, ok := desired[wsID]; !ok || rank(role) > rank(cur) {
			desired[wsID] = role
		}
	}
	adminManaged, wantAdmin := false, false
	for _, r := range in.Rules {
		if r.SystemAdmin {
			adminManaged = true
		}
		if r.WorkspaceID != nil {
			governed[*r.WorkspaceID] = true
		}
		if !groups[strings.ToLower(strings.TrimSpace(r.Group))] {
			continue
		}
		if r.SystemAdmin {
			wantAdmin = true
		}
		if r.WorkspaceID != nil && rank(r.Role) > 0 {
			want(*r.WorkspaceID, r.Role)
		}
	}
	if in.Prefix != "" {
		for _, g := range in.Groups {
			name, role, ok := parseStructured(in.Prefix, g)
			if !ok {
				continue
			}
			ws, err := s.workspaces.FindByName(name)
			if err != nil || ws == nil || ws.System {
				continue
			}
			want(ws.ID, role)
		}
	}
	if adminManaged {
		s.reconcileAdmin(in.User, wantAdmin)
	}

	// Grants first, removals after, so a workspace whose owner changes always keeps one.
	for wsID, role := range desired {
		s.grant(in, wsID, role, governed[wsID])
	}
	managed, err := s.workspaces.ListManagedBy(in.User.ID, in.Manager)
	if err != nil {
		logger.Warn("group sync: list managed memberships", "user", in.User.ID, "error", err)
	}
	stale := map[uint]bool{}
	for _, m := range managed {
		if _, ok := desired[m.WorkspaceID]; !ok {
			stale[m.WorkspaceID] = true
		}
	}
	for wsID := range governed {
		if _, ok := desired[wsID]; !ok {
			stale[wsID] = true
		}
	}
	for wsID := range stale {
		s.revoke(in, wsID)
	}
}

func (s *Syncer) grant(in Input, wsID uint, role models.WorkspaceRole, governed bool) {
	userID := in.User.ID
	member, err := s.workspaces.FindMember(wsID, userID)
	if err != nil {
		member = nil
	}
	if member != nil && member.ManagedBy != in.Manager && !governed {
		return // made by hand: not ours to change
	}
	if role == models.WorkspaceRoleOwner && (member == nil || member.Role != models.WorkspaceRoleOwner) && s.canOwn != nil {
		if err := s.canOwn(userID); err != nil {
			s.emit(userID, Event{Action: "sso.group_sync.owner_downgraded", WorkspaceID: wsID,
				Detail: map[string]any{"provider": in.Manager, "reason": err.Error()}})
			role = models.WorkspaceRoleAdmin
		}
	}
	switch {
	case member == nil:
		if err := s.workspaces.AddMember(&models.WorkspaceMember{WorkspaceID: wsID, UserID: userID, Role: role, ManagedBy: in.Manager}); err != nil {
			logger.Warn("group sync: add member", "workspace", wsID, "user", userID, "error", err)
			return
		}
		s.emit(userID, Event{Action: "sso.group_sync.member_add", WorkspaceID: wsID, Detail: map[string]any{"provider": in.Manager, "role": role}})
	case member.Role != role:
		if member.Role == models.WorkspaceRoleOwner && s.lastOwner(wsID) {
			return
		}
		if err := s.workspaces.UpdateMemberRole(wsID, userID, role); err != nil {
			logger.Warn("group sync: update role", "workspace", wsID, "user", userID, "error", err)
			return
		}
		s.emit(userID, Event{Action: "sso.group_sync.member_role", WorkspaceID: wsID,
			Detail: map[string]any{"provider": in.Manager, "from": member.Role, "to": role}})
	}
	if member != nil && member.ManagedBy != in.Manager && governed {
		_ = s.workspaces.SetMemberManagedBy(wsID, userID, in.Manager)
	}
}

func (s *Syncer) revoke(in Input, wsID uint) {
	member, err := s.workspaces.FindMember(wsID, in.User.ID)
	if err != nil || member == nil {
		return
	}
	if member.Role == models.WorkspaceRoleOwner && s.lastOwner(wsID) {
		return
	}
	if err := s.workspaces.RemoveMember(wsID, in.User.ID); err != nil {
		logger.Warn("group sync: remove member", "workspace", wsID, "user", in.User.ID, "error", err)
		return
	}
	s.emit(in.User.ID, Event{Action: "sso.group_sync.member_remove", WorkspaceID: wsID, Detail: map[string]any{"provider": in.Manager}})
}

func (s *Syncer) lastOwner(wsID uint) bool {
	n, err := s.workspaces.CountOwners(wsID)
	return err != nil || n <= 1
}

func (s *Syncer) reconcileAdmin(u *models.User, want bool) {
	isAdmin := u.Role == models.SystemRoleAdmin
	switch {
	case want && !isAdmin:
		u.Role = models.SystemRoleAdmin
	case !want && isAdmin:
		if n, err := s.users.CountByRole(models.SystemRoleAdmin); err != nil || n <= 1 {
			return
		}
		u.Role = models.SystemRoleUser
	default:
		return
	}
	if err := s.users.Update(u); err != nil {
		logger.Warn("group sync: set platform role", "user", u.ID, "error", err)
		return
	}
	s.emit(u.ID, Event{Action: "sso.group_sync.platform_role", Detail: map[string]any{"role": u.Role}})
}

func (s *Syncer) emit(userID uint, e Event) {
	if s.record != nil {
		s.record(userID, e)
	}
}
