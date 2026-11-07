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

// Package workspace manages workspaces, memberships, and invitations.
package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type NetworkEnsurer interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
}

// InvitationTTL is how long an invitation remains valid.
const InvitationTTL = 7 * 24 * time.Hour

var (
	ErrNameTaken                = errors.New("workspace name already taken")
	ErrNameInvalid              = errors.New("name must contain only lowercase letters, digits and hyphens")
	ErrNameReserved             = errors.New("that workspace name is reserved")
	ErrSystemNameLocked         = errors.New("the system workspace name cannot be changed")
	ErrLastOwner                = errors.New("cannot remove or demote the last owner")
	ErrAlreadyMember            = errors.New("user is already a member")
	ErrInvitePending            = errors.New("a pending invitation for this email already exists")
	ErrInvalidInvite            = errors.New("invalid or expired invitation")
	ErrInvalidRole              = errors.New("invalid role")
	ErrServiceAccountOwner      = errors.New("a service account cannot own a workspace")
	ErrSystemProtected          = errors.New("the platform system workspace cannot be deleted")
	ErrOutranked                = errors.New("cannot modify a member whose role outranks yours")
	ErrRoleAboveSelf            = errors.New("cannot grant a role more privileged than your own")
	ErrWorkspaceLimitReached    = errors.New("workspace limit reached for this user")
	ErrOrgWorkspaceLimitReached = errors.New("this organization has reached its workspace limit")
	ErrMembershipLimitReached   = errors.New("workspace membership limit reached for this user")
)

type KeyShredder interface {
	ShredWorkspace(workspaceID uint) error
}

// Best-effort: a seed failure never fails workspace creation.
type MiddlewareSeeder interface {
	SeedDefaults(ctx context.Context, workspaceID uint) error
}

type Service struct {
	repo   *repositories.WorkspaceRepository
	users  *repositories.UserRepository
	orgs   Orgs
	nets   NetworkEnsurer
	seeder MiddlewareSeeder
	plans  *repositories.PlanRepository
	quota  *quota.Service
	keys   KeyShredder
	// adopter claims a user's first workspace as their landing workspace.
	adopter                DefaultAdopter
	globalLimit            func() int
	overrideEntitled       func() bool
	memberGlobalLimit      func() int
	memberOverrideEntitled func() bool
	// orgLimitsEntitled reports whether organization-scoped limits are licensed.
	orgLimitsEntitled func() bool
	// creationMode returns the platform workspace creation mode; see SetCreationPolicy.
	creationMode func() string
	consoleURL   func() string
}

func NewService(repo *repositories.WorkspaceRepository, users *repositories.UserRepository, nets NetworkEnsurer) *Service {
	return &Service{repo: repo, users: users, nets: nets}
}

// When unset, the system workspace falls back to the default plan.
func (s *Service) SetPlans(plans *repositories.PlanRepository) { s.plans = plans }

type DefaultAdopter interface {
	AdoptFirstWorkspace(userID, workspaceID uint)
}

// SetDefaultAdopter wires the first-workspace landing adoption (nil-safe).
func (s *Service) SetDefaultAdopter(a DefaultAdopter) { s.adopter = a }

// SetKeyShredder wires crypto-shred of a workspace's keys on delete (nil-safe).
func (s *Service) SetKeyShredder(k KeyShredder) { s.keys = k }

// SetMiddlewareSeeder wires the default-policy seeder used at workspace creation.
func (s *Service) SetMiddlewareSeeder(m MiddlewareSeeder) { s.seeder = m }

type Orgs interface {
	HomeOrganization(u *models.User) uint
	CapReached(orgID uint) bool
	Get(orgID uint) (*models.Organization, error)
}

// Without orgs, a workspace is created with no organization, which resolves to the default one.
func (s *Service) SetOrgs(o Orgs) { s.orgs = o }

// Unlicensed operators can't edit org caps, so enforcing them would strand them; nil means never.
func (s *Service) SetOrgLimitsEntitled(entitled func() bool) { s.orgLimitsEntitled = entitled }

func (s *Service) orgLimits(userID uint) *models.Organization {
	if s.orgLimitsEntitled == nil || !s.orgLimitsEntitled() {
		return nil
	}
	return s.homeOrg(userID)
}

func (s *Service) organizationFor(ownerID uint) (orgID uint, defaultCluster *uint) {
	if s.orgs == nil {
		return 0, nil
	}
	u, err := s.users.FindByID(ownerID)
	if err != nil {
		return 0, nil
	}
	orgID = s.orgs.HomeOrganization(u)
	if orgID == 0 {
		return 0, nil
	}
	if org, err := s.orgs.Get(orgID); err == nil {
		defaultCluster = org.DefaultClusterID
	}
	return orgID, defaultCluster
}

func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// -1 means unlimited and 0 none; an organization's own cap takes precedence over the platform value.
func (s *Service) SetLimits(globalLimit func() int, overrideEntitled func() bool) {
	s.globalLimit = globalLimit
	s.overrideEntitled = overrideEntitled
}

// Order: per-user override, org owner cap, org per-user cap, platform default; -1 or lower means unlimited.
func (s *Service) effectiveWorkspaceLimit(userID uint) (limit int, unlimited bool) {
	if s.overrideEntitled != nil && s.overrideEntitled() {
		if u, err := s.users.FindByID(userID); err == nil && u.WorkspaceLimit != nil {
			return bound(*u.WorkspaceLimit)
		}
	}
	if org := s.orgLimits(userID); org != nil {
		if org.OwnerUserID == userID && org.MaxWorkspacesPerOwner != nil && s.overrideEntitled != nil && s.overrideEntitled() {
			return bound(*org.MaxWorkspacesPerOwner)
		}
		if org.MaxWorkspacesPerUser != nil {
			return bound(*org.MaxWorkspacesPerUser)
		}
	}
	if s.globalLimit != nil {
		return bound(s.globalLimit())
	}
	return 0, true
}

// bound reads a stored limit under the shared convention.
func bound(n int) (limit int, unlimited bool) {
	if n < 0 {
		return 0, true
	}
	return n, false
}

func (s *Service) homeOrg(userID uint) *models.Organization {
	if s.orgs == nil || s.users == nil {
		return nil
	}
	u, err := s.users.FindByID(userID)
	if err != nil {
		return nil
	}
	orgID := s.orgs.HomeOrganization(u)
	if orgID == 0 {
		return nil
	}
	org, err := s.orgs.Get(orgID)
	if err != nil {
		return nil
	}
	return org
}

// CanOwnAnother is exported for SSO group sync.
func (s *Service) CanOwnAnother(userID uint) error { return s.canOwnAnother(userID) }

func (s *Service) canOwnAnother(userID uint) error {
	if u, err := s.users.FindByID(userID); err == nil && u.IsService() {
		return ErrServiceAccountOwner
	}
	if s.globalLimit == nil && s.overrideEntitled == nil {
		return nil // limits not wired
	}
	limit, unlimited := s.effectiveWorkspaceLimit(userID)
	if unlimited {
		return nil
	}
	n, err := s.repo.CountOwnedBy(userID)
	if err != nil {
		return nil
	}
	if int(n) >= limit {
		return ErrWorkspaceLimitReached
	}
	return nil
}

func (s *Service) SetMembershipLimits(globalLimit func() int, overrideEntitled func() bool) {
	s.memberGlobalLimit = globalLimit
	s.memberOverrideEntitled = overrideEntitled
}

func (s *Service) effectiveMembershipLimit(userID uint) (limit int, unlimited bool) {
	if s.memberOverrideEntitled != nil && s.memberOverrideEntitled() {
		if u, err := s.users.FindByID(userID); err == nil && u.WorkspaceMembershipLimit != nil {
			return bound(*u.WorkspaceMembershipLimit)
		}
	}
	if org := s.orgLimits(userID); org != nil && org.MaxWorkspaceMembershipsPerUser != nil {
		return bound(*org.MaxWorkspaceMembershipsPerUser)
	}
	if s.memberGlobalLimit != nil {
		return bound(s.memberGlobalLimit())
	}
	return 0, true
}

// CanJoinAnother lets SSO/SCIM auto-join paths gate first; fails open on a count error.
func (s *Service) CanJoinAnother(userID uint) error {
	if s.memberGlobalLimit == nil && s.memberOverrideEntitled == nil {
		return nil // limits not wired
	}
	limit, unlimited := s.effectiveMembershipLimit(userID)
	if unlimited {
		return nil
	}
	n, err := s.repo.CountJoinedBy(userID)
	if err != nil {
		return nil
	}
	if int(n) >= limit {
		return ErrMembershipLimitReached
	}
	return nil
}

func (s *Service) checkMemberCapacity(workspaceID uint) error {
	if s.quota == nil {
		return nil
	}
	n, err := s.repo.CountMembers(workspaceID)
	if err != nil {
		return nil // fail open on a count error
	}
	return s.quota.CheckCreate(workspaceID, quota.ResourceMembers, int(n))
}

// The handle is suffixed to be unique and non-reserved, so creation never fails on a collision.
func (s *Service) Create(ownerID uint, displayName, handle, description string) (*models.Workspace, error) {
	if err := s.CanCreate(ownerID); err != nil {
		return nil, err
	}
	if err := s.canOwnAnother(ownerID); err != nil {
		return nil, err
	}
	orgID, orgCluster := s.organizationFor(ownerID)
	// The realm cap applies only with the licence that lets an operator set it.
	if orgID != 0 && s.orgLimitsEntitled != nil && s.orgLimitsEntitled() && s.orgs.CapReached(orgID) {
		return nil, ErrOrgWorkspaceLimitReached
	}
	base := strings.TrimSpace(handle)
	if base == "" {
		base = displayName
	}
	name, err := s.uniqueName(base)
	if err != nil {
		return nil, err
	}
	return s.create(ownerID, name, displayName, description, orgID, orgCluster)
}

// The handle is never suffixed, so a caller retrying after a lost answer can recognise its own workspace.
func (s *Service) CreateForOwner(ownerID uint, displayName, handle string) (*models.Workspace, error) {
	name := slug.Make(handle, "")
	if name == "" {
		return nil, ErrNameInvalid
	}
	if slug.IsReserved(name) {
		return nil, ErrNameReserved
	}
	exists, err := s.repo.ExistsByName(name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrNameTaken
	}
	orgID, orgCluster := s.organizationFor(ownerID)
	return s.create(ownerID, name, displayName, "", orgID, orgCluster)
}

func (s *Service) create(ownerID uint, name, displayName, description string, orgID uint, orgCluster *uint) (*models.Workspace, error) {
	if strings.TrimSpace(displayName) == "" {
		displayName = name
	}
	ws := &models.Workspace{Name: name, DisplayName: displayName, Description: description, OwnerID: ownerID}
	if orgID != 0 {
		ws.OrganizationID = &orgID
		ws.DefaultClusterID = orgCluster
	}
	if err := s.repo.CreateWithOwner(ws); err != nil {
		return nil, err
	}
	// Auto-provision the workspace's default Docker network (best-effort).
	if s.nets != nil {
		if _, err := s.nets.EnsureDefault(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to create default network for workspace", "workspace", ws.ID, "error", err)
		}
	}
	// The system workspace uses EnsureSystem, not this path, so it is never seeded.
	if s.seeder != nil {
		if err := s.seeder.SeedDefaults(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to seed default middlewares for workspace", "workspace", ws.ID, "error", err)
		}
	}

	if s.adopter != nil {
		s.adopter.AdoptFirstWorkspace(ownerID, ws.ID)
	}
	return ws, nil
}

func (s *Service) Get(id uint) (*models.Workspace, error) { return s.repo.FindByID(id) }
func (s *Service) ListForUser(userID uint) ([]models.WorkspaceWithRole, error) {
	return s.repo.ListForUser(userID)
}

func (s *Service) Update(ws *models.Workspace) error { return s.repo.Update(ws) }

// Unlike Create it doesn't auto-suffix, so a rename onto a taken handle is an error.
func (s *Service) SetName(ws *models.Workspace, newName string) error {
	name := slug.Make(newName, "")
	if name == "" {
		return ErrNameInvalid
	}
	if name == ws.Name {
		return nil
	}
	if ws.System {
		return ErrSystemNameLocked
	}
	if slug.IsReserved(name) {
		return ErrNameReserved
	}
	exists, err := s.repo.ExistsByName(name)
	if err != nil {
		return err
	}
	if exists {
		return ErrNameTaken
	}
	ws.Name = name
	return nil
}

// Delete removes a workspace, refusing to delete the built-in system workspace.
func (s *Service) Delete(id uint) error {
	ws, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if ws.System {
		return ErrSystemProtected
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	// Defence in depth alongside the row deletes: shredded keys leave ciphertext unrecoverable.
	if s.keys != nil {
		if err := s.keys.ShredWorkspace(id); err != nil {
			logger.Warn("crypto-shred: failed to delete workspace keys", "workspace", id, "error", err)
		}
	}
	return nil
}

// Reserved handle, claimable only by EnsureSystem.
const SystemName = "system"

// EnsureSystem's workspace is privileged (host ports auto-approve) and flagged System so it can't be deleted.
func (s *Service) EnsureSystem(ownerID uint) (*models.Workspace, error) {
	if ws, err := s.repo.FindSystem(); err == nil {
		return ws, nil
	}
	name := SystemName
	if exists, err := s.repo.ExistsByName(name); err != nil {
		return nil, err
	} else if exists {
		if name, err = s.uniqueName("Miabi System"); err != nil {
			return nil, err
		}
	}
	ws := &models.Workspace{
		Name:        name,
		DisplayName: "Miabi System",
		Description: "Built-in workspace for platform-managed apps (e.g. per-node gateways). Managed by platform admins.",
		OwnerID:     ownerID,
		Privileged:  true,
		System:      true,
	}
	if err := s.repo.CreateWithOwner(ws); err != nil {
		return nil, err
	}
	// Platform infrastructure must never be constrained by tenant quotas; old installs are backfilled by an upgrade step.
	s.pinUnlimitedPlan(ws)
	if s.nets != nil {
		if _, err := s.nets.EnsureDefault(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to create default network for system workspace", "workspace", ws.ID, "error", err)
		}
	}
	logger.Info("platform system workspace ready", "workspace", ws.ID, "name", ws.Name)
	return ws, nil
}

func (s *Service) pinUnlimitedPlan(ws *models.Workspace) {
	if s.plans == nil {
		return
	}
	plan, err := s.plans.FindByName(models.UnlimitedPlanName)
	if err != nil {
		logger.Warn("unlimited plan not found; system workspace left on default plan", "workspace", ws.ID, "error", err)
		return
	}
	if err := s.plans.AssignToWorkspace(ws.ID, &plan.ID); err != nil {
		logger.Warn("failed to pin unlimited plan to system workspace", "workspace", ws.ID, "error", err)
		return
	}
	ws.PlanID = &plan.ID
}

func (s *Service) ListMembers(workspaceID uint) ([]models.WorkspaceMember, error) {
	return s.repo.ListMembers(workspaceID)
}

const SystemActor = models.WorkspaceRoleOwner

func (s *Service) guardRank(workspaceID uint, actor models.WorkspaceRole, targetID uint, newRole *models.WorkspaceRole) error {
	if !actor.Valid() {
		return ErrOutranked
	}
	target, err := s.repo.FindMember(workspaceID, targetID)
	if err != nil {
		return err
	}
	if target.Role.Rank() > actor.Rank() {
		return ErrOutranked
	}
	if newRole != nil && newRole.Rank() > actor.Rank() {
		return ErrRoleAboveSelf
	}
	return nil
}

func (s *Service) UpdateMemberRole(workspaceID uint, actor models.WorkspaceRole, userID uint, role models.WorkspaceRole) error {
	if !role.Valid() {
		return ErrInvalidRole
	}
	if err := s.guardRank(workspaceID, actor, userID, &role); err != nil {
		return err
	}
	if role != models.WorkspaceRoleOwner {
		if err := s.guardLastOwner(workspaceID, userID); err != nil {
			return err
		}
	} else if member, err := s.repo.FindMember(workspaceID, userID); err != nil ||
		member.Role != models.WorkspaceRoleOwner {
		if err := s.canOwnAnother(userID); err != nil {
			return err
		}
	}
	return s.repo.UpdateMemberRole(workspaceID, userID, role)
}

func (s *Service) AssignCustomRole(workspaceID uint, actor models.WorkspaceRole, userID, customRoleID uint, baseRole models.WorkspaceRole) error {
	if !baseRole.Valid() {
		return ErrInvalidRole
	}
	if err := s.guardRank(workspaceID, actor, userID, &baseRole); err != nil {
		return err
	}
	if baseRole != models.WorkspaceRoleOwner {
		if err := s.guardLastOwner(workspaceID, userID); err != nil {
			return err
		}
	} else if member, err := s.repo.FindMember(workspaceID, userID); err != nil ||
		member.Role != models.WorkspaceRoleOwner {
		if err := s.canOwnAnother(userID); err != nil {
			return err
		}
	}
	return s.repo.SetMemberCustomRole(workspaceID, userID, customRoleID, baseRole)
}

func (s *Service) RemoveMember(workspaceID uint, actor models.WorkspaceRole, userID uint) error {
	if err := s.guardRank(workspaceID, actor, userID, nil); err != nil {
		return err
	}
	if err := s.guardLastOwner(workspaceID, userID); err != nil {
		return err
	}
	return s.repo.RemoveMember(workspaceID, userID)
}

func (s *Service) guardLastOwner(workspaceID, userID uint) error {
	member, err := s.repo.FindMember(workspaceID, userID)
	if err != nil {
		return err
	}
	if member.Role != models.WorkspaceRoleOwner {
		return nil
	}
	owners, err := s.repo.CountOwners(workspaceID)
	if err != nil {
		return err
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

// Invite creates a pending invitation and returns the raw token to deliver.
func (s *Service) Invite(workspaceID uint, actor models.WorkspaceRole, inviterID uint, email string, role models.WorkspaceRole) (string, *models.WorkspaceInvitation, error) {
	if !role.Valid() {
		return "", nil, ErrInvalidRole
	}
	if !actor.Valid() || role.Rank() > actor.Rank() {
		return "", nil, ErrRoleAboveSelf
	}
	email = strings.ToLower(strings.TrimSpace(email))

	if u, err := s.users.FindByEmail(email); err == nil {
		if _, err := s.repo.FindMember(workspaceID, u.ID); err == nil {
			return "", nil, ErrAlreadyMember
		}
	}

	// Reject a duplicate: one pending invitation per email per workspace.
	if exists, err := s.repo.PendingInvitationExists(workspaceID, email); err == nil && exists {
		return "", nil, ErrInvitePending
	}

	// Don't invite into a workspace that has reached its member limit.
	if err := s.checkMemberCapacity(workspaceID); err != nil {
		return "", nil, err
	}

	raw, hash := generateToken()
	inv := &models.WorkspaceInvitation{
		WorkspaceID: workspaceID,
		Email:       email,
		Role:        role,
		TokenHash:   hash,
		Status:      models.InvitationStatusPending,
		InvitedBy:   inviterID,
		ExpiresAt:   time.Now().Add(InvitationTTL),
	}
	if err := s.repo.CreateInvitation(inv); err != nil {
		return "", nil, err
	}
	return raw, inv, nil
}

func (s *Service) ListInvitations(workspaceID uint) ([]models.WorkspaceInvitation, error) {
	return s.repo.ListInvitations(workspaceID)
}

// The invitee isn't yet a member and can't read the workspace, so the context travels here.
type PendingInvitation struct {
	ID            uint                 `json:"id"`
	WorkspaceID   uint                 `json:"workspace_id"`
	WorkspaceName string               `json:"workspace_name"`
	Role          models.WorkspaceRole `json:"role"`
	InvitedByName string               `json:"invited_by_name"`
	ExpiresAt     time.Time            `json:"expires_at"`
	CreatedAt     time.Time            `json:"created_at"`
}

func (s *Service) ListInvitationsForEmail(email string) ([]PendingInvitation, error) {
	invs, err := s.repo.ListInvitationsByEmail(email)
	if err != nil {
		return nil, err
	}
	out := make([]PendingInvitation, 0, len(invs))
	for _, inv := range invs {
		p := PendingInvitation{
			ID: inv.ID, WorkspaceID: inv.WorkspaceID, Role: inv.Role,
			ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
		}
		if ws, err := s.repo.FindByID(inv.WorkspaceID); err == nil {
			p.WorkspaceName = ws.DisplayName
		}
		if u, err := s.users.FindByID(inv.InvitedBy); err == nil {
			p.InvitedByName = u.Name
		}
		out = append(out, p)
	}
	return out, nil
}

// Accept consumes an invitation token and adds the user as a member.
func (s *Service) Accept(rawToken string, userID uint) (*models.Workspace, error) {
	inv, err := s.repo.FindInvitationByHash(hashToken(rawToken))
	if err != nil {
		return nil, ErrInvalidInvite
	}
	return s.acceptInvitation(inv, userID)
}

// Used by the in-app list, where the emailed raw token isn't available.
func (s *Service) AcceptByID(invitationID, userID uint) (*models.Workspace, error) {
	user, err := s.users.FindByID(userID)
	if err != nil {
		return nil, ErrInvalidInvite
	}
	inv, err := s.repo.FindInvitationByID(invitationID)
	if err != nil {
		return nil, ErrInvalidInvite
	}
	if !strings.EqualFold(strings.TrimSpace(inv.Email), strings.TrimSpace(user.Email)) {
		return nil, ErrInvalidInvite
	}
	return s.acceptInvitation(inv, userID)
}

func (s *Service) acceptInvitation(inv *models.WorkspaceInvitation, userID uint) (*models.Workspace, error) {
	if inv.Status != models.InvitationStatusPending || time.Now().After(inv.ExpiresAt) {
		return nil, ErrInvalidInvite
	}
	if _, err := s.repo.FindMember(inv.WorkspaceID, userID); err == nil {
		return nil, ErrAlreadyMember
	}
	if err := s.checkMemberCapacity(inv.WorkspaceID); err != nil {
		return nil, err
	}
	// An owner invitation counts against the owned limit; any other role against the membership limit.
	if inv.Role == models.WorkspaceRoleOwner {
		if err := s.canOwnAnother(userID); err != nil {
			return nil, err
		}
	} else if err := s.CanJoinAnother(userID); err != nil {
		return nil, err
	}
	if err := s.repo.AddMember(&models.WorkspaceMember{
		WorkspaceID: inv.WorkspaceID, UserID: userID, Role: inv.Role,
	}); err != nil {
		return nil, err
	}
	inv.Status = models.InvitationStatusAccepted
	if err := s.repo.UpdateInvitation(inv); err != nil {
		return nil, err
	}
	return s.repo.FindByID(inv.WorkspaceID)
}

func (s *Service) uniqueName(base string) (string, error) {
	return slug.UniqueAvailable(base, "workspace", s.repo.ExistsByName)
}

func generateToken() (raw, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
