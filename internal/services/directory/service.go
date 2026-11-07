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

// Package directory turns a successful LDAP/AD bind into a JIT-provisioned Miabi user with group-mapped access.
package directory

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// AuthSourceLDAP marks accounts provisioned/managed by a directory.
const AuthSourceLDAP = models.AuthSourceLDAP

var (
	// ErrAccountDisabled is returned when the matched local account is disabled.
	ErrAccountDisabled = errors.New("account is disabled")
	ErrNoEmail         = errors.New("directory user has no email address")
)

type Service struct {
	ee         enterprise.EE
	users      *repositories.UserRepository
	workspaces *repositories.WorkspaceRepository
	ldap       *repositories.LDAPRepository
}

func NewService(ee enterprise.EE, users *repositories.UserRepository, workspaces *repositories.WorkspaceRepository, ldap *repositories.LDAPRepository) *Service {
	return &Service{ee: ee, users: users, workspaces: workspaces, ldap: ldap}
}

// Login: (user, nil) bound and provisioned; (nil, nil) no directory matched; (nil, err) bind failed or account disabled.
func (s *Service) Login(ctx context.Context, identifier, password string) (*models.User, error) {
	a := s.ee.LDAP()
	if a == nil {
		return nil, nil // Community, or sso_ldap not entitled.
	}
	ident, err := a.Authenticate(ctx, identifier, password)
	if errors.Is(err, enterprise.ErrLDAPNoMatch) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	user, err := s.provision(ident)
	if err != nil {
		return nil, err
	}
	// Best-effort: a mapping failure must never block an otherwise-valid login.
	if err := s.reconcile(user, ident); err != nil {
		logger.Warn("directory: group reconciliation failed", "user", user.ID, "error", err)
	}
	return user, nil
}

// The first platform user becomes admin, mirroring the OAuth/SAML JIT policy.
func (s *Service) provision(ident enterprise.LDAPIdentity) (*models.User, error) {
	// BeforeCreate stores the slugified handle, so a plain lowercase match misses "J.Doe" and collides on insert.
	username := slug.Make(ident.Username, "")
	email := strings.ToLower(strings.TrimSpace(ident.Email))

	// Links across auth sources so local accounts that predate the directory are not duplicated.
	if username != "" {
		if u, err := s.users.FindByUsername(username); err == nil {
			return activeOrErr(u)
		}
	}
	if email != "" {
		if u, err := s.users.FindByEmail(email); err == nil {
			return activeOrErr(u)
		}
	}
	if email == "" {
		return nil, ErrNoEmail
	}
	name := strings.TrimSpace(ident.Name)
	if name == "" {
		if email != "" {
			name = email
		} else {
			name = username
		}
	}
	count, _ := s.users.Count()
	role := models.SystemRoleUser
	if count == 0 {
		role = models.SystemRoleAdmin
	}
	now := time.Now()
	user := &models.User{
		Name:  name,
		Email: email,
		// BeforeCreate trusts a supplied username, so an unchecked collision would fail the sign-in.
		Username:        slug.Available(username, s.users.ExistsByUsername),
		PasswordHash:    unusablePassword(),
		Role:            role,
		Active:          true,
		AuthSource:      AuthSourceLDAP,
		EmailVerifiedAt: &now, // the directory asserts the identity
	}
	if err := s.users.Create(user); err != nil {
		return nil, err
	}
	return user, nil
}

func activeOrErr(u *models.User) (*models.User, error) {
	if !u.Active {
		return nil, ErrAccountDisabled
	}
	return u, nil
}

// Only workspaces a mapping references count as directory-managed, so manual grants elsewhere are untouched.
func (s *Service) reconcile(user *models.User, ident enterprise.LDAPIdentity) error {
	if user.AuthSource != AuthSourceLDAP {
		return nil
	}
	cfg, err := s.ldap.FindByName(ident.Provider)
	if err != nil || cfg == nil || len(cfg.Mappings) == 0 {
		return err
	}
	groups := normalizeGroups(ident.Groups)

	wantAdmin := false
	adminManaged := false
	desired := map[uint]models.WorkspaceRole{} // workspace → highest granted role
	managed := map[uint]bool{}                 // workspaces the directory governs
	for _, m := range cfg.Mappings {
		if m.SystemAdmin {
			adminManaged = true
		}
		if m.WorkspaceID != nil {
			managed[*m.WorkspaceID] = true
		}
		if !groupMatches(groups, m.GroupDN) {
			continue
		}
		if m.SystemAdmin {
			wantAdmin = true
		}
		if m.WorkspaceID != nil && m.WorkspaceRole.Valid() {
			if cur, ok := desired[*m.WorkspaceID]; !ok || roleRank(m.WorkspaceRole) > roleRank(cur) {
				desired[*m.WorkspaceID] = m.WorkspaceRole
			}
		}
	}

	// Only when a mapping governs admin, so a config that doesn't manage it never demotes a manual admin.
	if adminManaged {
		s.reconcileAdmin(user, wantAdmin)
	}

	for wsID := range managed {
		role, want := desired[wsID]
		member, merr := s.workspaces.FindMember(wsID, user.ID)
		switch {
		case want && merr != nil:
			if err := s.workspaces.AddMember(&models.WorkspaceMember{WorkspaceID: wsID, UserID: user.ID, Role: role}); err != nil {
				logger.Warn("directory: add member failed", "workspace", wsID, "user", user.ID, "error", err)
			}
		case want && member != nil && member.Role != role:
			if err := s.workspaces.UpdateMemberRole(wsID, user.ID, role); err != nil {
				logger.Warn("directory: update member role failed", "workspace", wsID, "user", user.ID, "error", err)
			}
		case !want && member != nil:
			if err := s.workspaces.RemoveMember(wsID, user.ID); err != nil {
				logger.Warn("directory: remove member failed", "workspace", wsID, "user", user.ID, "error", err)
			}
		}
	}
	return nil
}

// reconcileAdmin never demotes the last remaining admin.
func (s *Service) reconcileAdmin(user *models.User, wantAdmin bool) {
	isAdmin := user.Role == models.SystemRoleAdmin
	switch {
	case wantAdmin && !isAdmin:
		user.Role = models.SystemRoleAdmin
		if err := s.users.Update(user); err != nil {
			logger.Warn("directory: promote admin failed", "user", user.ID, "error", err)
		}
	case !wantAdmin && isAdmin:
		if n, _ := s.users.CountByRole(models.SystemRoleAdmin); n > 1 {
			user.Role = models.SystemRoleUser
			if err := s.users.Update(user); err != nil {
				logger.Warn("directory: demote admin failed", "user", user.ID, "error", err)
			}
		}
	}
}

func normalizeGroups(groups []string) map[string]bool {
	m := make(map[string]bool, len(groups))
	for _, g := range groups {
		if g = strings.ToLower(strings.TrimSpace(g)); g != "" {
			m[g] = true
		}
	}
	return m
}

func groupMatches(userGroups map[string]bool, mappingGroup string) bool {
	g := strings.ToLower(strings.TrimSpace(mappingGroup))
	if g == "" {
		return false
	}
	if userGroups[g] {
		return true
	}
	if !strings.Contains(g, "=") { // bare CN → compare against each group's CN
		for dn := range userGroups {
			if cnOf(dn) == g {
				return true
			}
		}
	}
	return false
}

// cnOf extracts the CN value from the first RDN of a DN ("cn=admins,ou=..." → "admins").
func cnOf(dn string) string {
	first := dn
	if i := strings.IndexByte(dn, ','); i >= 0 {
		first = dn[:i]
	}
	if i := strings.IndexByte(first, '='); i >= 0 {
		return strings.TrimSpace(first[i+1:])
	}
	return strings.TrimSpace(first)
}

func roleRank(r models.WorkspaceRole) int {
	switch r {
	case models.WorkspaceRoleOwner:
		return 4
	case models.WorkspaceRoleAdmin:
		return 3
	case models.WorkspaceRoleDeveloper:
		return 2
	case models.WorkspaceRoleViewer:
		return 1
	default:
		return 0
	}
}

// A random hash ensures directory accounts can never sign in with a local password.
func unusablePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	hash, _ := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	return string(hash)
}
