// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// NetworkEnsurer creates a workspace's default Docker network. Implemented by
// the network service; an interface keeps workspace decoupled from it.
type NetworkEnsurer interface {
	EnsureDefault(ctx context.Context, workspaceID uint) (*models.Network, error)
}

// InvitationTTL is how long an invitation remains valid.
const InvitationTTL = 7 * 24 * time.Hour

var (
	ErrNameTaken              = errors.New("workspace name already taken")
	ErrNameInvalid            = errors.New("name must contain only lowercase letters, digits and hyphens")
	ErrNameReserved           = errors.New("that workspace name is reserved")
	ErrSystemNameLocked       = errors.New("the system workspace name cannot be changed")
	ErrLastOwner              = errors.New("cannot remove or demote the last owner")
	ErrAlreadyMember          = errors.New("user is already a member")
	ErrInvitePending          = errors.New("a pending invitation for this email already exists")
	ErrInvalidInvite          = errors.New("invalid or expired invitation")
	ErrInvalidRole            = errors.New("invalid role")
	ErrSystemProtected        = errors.New("the platform system workspace cannot be deleted")
	ErrOutranked              = errors.New("cannot modify a member whose role outranks yours")
	ErrRoleAboveSelf          = errors.New("cannot grant a role more privileged than your own")
	ErrWorkspaceLimitReached  = errors.New("workspace limit reached for this user")
	ErrMembershipLimitReached = errors.New("workspace membership limit reached for this user")
)

// KeyShredder permanently deletes a workspace's encryption keys (crypto-shred on
// delete). Implemented by the keyring service; injected, nil-safe.
type KeyShredder interface {
	ShredWorkspace(workspaceID uint) error
}

// MiddlewareSeeder creates a new workspace's default policy set. Implemented by
// the middleware service; injected, nil-safe, and best-effort (a seed failure
// never fails workspace creation).
type MiddlewareSeeder interface {
	SeedDefaults(ctx context.Context, workspaceID uint) error
}

type Service struct {
	repo   *repositories.WorkspaceRepository
	users  *repositories.UserRepository
	nets   NetworkEnsurer
	seeder MiddlewareSeeder
	plans  *repositories.PlanRepository
	quota  *quota.Service
	keys   KeyShredder
	// globalLimit returns the platform-wide max_workspaces_per_user (0/negative =
	// unlimited); overrideEntitled reports whether the Enterprise per-user override
	// may be applied. Both nil-safe — unset leaves workspace ownership uncapped.
	globalLimit      func() int
	overrideEntitled func() bool
	// The membership counterparts: how many workspaces a user may JOIN as a
	// non-owner member (global max_workspace_memberships_per_user + the EE
	// per-user override). Nil-safe — unset leaves membership uncapped.
	memberGlobalLimit      func() int
	memberOverrideEntitled func() bool
}

func NewService(repo *repositories.WorkspaceRepository, users *repositories.UserRepository, nets NetworkEnsurer) *Service {
	return &Service{repo: repo, users: users, nets: nets}
}

// SetPlans wires the plan repository so EnsureSystem can pin the platform system
// workspace to the Unlimited plan. Optional: when unset, the system workspace
// falls back to the default plan.
func (s *Service) SetPlans(plans *repositories.PlanRepository) { s.plans = plans }

// SetKeyShredder wires crypto-shred of a workspace's keys on delete (nil-safe).
func (s *Service) SetKeyShredder(k KeyShredder) { s.keys = k }

// SetMiddlewareSeeder wires the default-policy seeder used at workspace creation.
func (s *Service) SetMiddlewareSeeder(m MiddlewareSeeder) { s.seeder = m }

// SetQuota wires the quota service so member invitations/acceptances are gated
// by the workspace's effective max-members limit. Optional: when unset (or with
// enforcement disabled), membership is uncapped.
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetLimits wires per-user workspace-count enforcement. globalLimit returns the
// platform-wide max_workspaces_per_user (0/negative = unlimited); overrideEntitled
// reports whether the Enterprise per-user override (User.WorkspaceLimit) is
// licensed. Nil-safe: leaving this unset means workspace ownership is uncapped.
func (s *Service) SetLimits(globalLimit func() int, overrideEntitled func() bool) {
	s.globalLimit = globalLimit
	s.overrideEntitled = overrideEntitled
}

// effectiveWorkspaceLimit resolves how many workspaces userID may own: the
// Enterprise per-user override when set and entitled, else the platform global.
// Returns (limit, unlimited). Override convention: -1 = unlimited, 0 = none,
// N = N. Global: <= 0 = unlimited (legacy), N = N.
func (s *Service) effectiveWorkspaceLimit(userID uint) (limit int, unlimited bool) {
	if s.overrideEntitled != nil && s.overrideEntitled() {
		if u, err := s.users.FindByID(userID); err == nil && u.WorkspaceLimit != nil {
			if *u.WorkspaceLimit < 0 {
				return 0, true
			}
			return *u.WorkspaceLimit, false
		}
	}
	g := 0
	if s.globalLimit != nil {
		g = s.globalLimit()
	}
	if g <= 0 {
		return 0, true
	}
	return g, false
}

// canOwnAnother returns ErrWorkspaceLimitReached when userID is already at their
// effective workspace-ownership limit. No-op when limits are unwired or the
// effective limit is unlimited. Fails open on a count error (mirrors
// checkMemberCapacity), so a transient DB hiccup never blocks legitimate work.
func (s *Service) canOwnAnother(userID uint) error {
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

// SetMembershipLimits wires per-user membership-count enforcement — the join
// counterpart of SetLimits. globalLimit returns max_workspace_memberships_per_user
// (0/negative = unlimited); overrideEntitled reports whether the Enterprise
// per-user override (User.WorkspaceMembershipLimit) is licensed. Nil-safe.
func (s *Service) SetMembershipLimits(globalLimit func() int, overrideEntitled func() bool) {
	s.memberGlobalLimit = globalLimit
	s.memberOverrideEntitled = overrideEntitled
}

// effectiveMembershipLimit resolves how many workspaces userID may join as a
// non-owner member: the Enterprise per-user override when set and entitled, else
// the platform global. Mirrors effectiveWorkspaceLimit.
func (s *Service) effectiveMembershipLimit(userID uint) (limit int, unlimited bool) {
	if s.memberOverrideEntitled != nil && s.memberOverrideEntitled() {
		if u, err := s.users.FindByID(userID); err == nil && u.WorkspaceMembershipLimit != nil {
			if *u.WorkspaceMembershipLimit < 0 {
				return 0, true
			}
			return *u.WorkspaceMembershipLimit, false
		}
	}
	g := 0
	if s.memberGlobalLimit != nil {
		g = s.memberGlobalLimit()
	}
	if g <= 0 {
		return 0, true
	}
	return g, false
}

// CanJoinAnother returns ErrMembershipLimitReached when userID is already at
// their effective membership limit (joining one more workspace as a non-owner
// member). Public so auto-join paths (SSO/SCIM) can gate before adding a member.
// No-op when limits are unwired or unlimited; fails open on a count error.
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

// checkMemberCapacity returns a QuotaError when the workspace is already at its
// effective max-members limit. No-op when quota is unset or enforcement is off.
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

// Create makes a new workspace owned by ownerID. displayName is the free-text
// label; handle is the desired unique name (the URL/docker handle) — when blank
// it is derived from displayName. The handle is always made unique and
// non-reserved by suffixing, so creation never fails on a collision.
func (s *Service) Create(ownerID uint, displayName, handle, description string) (*models.Workspace, error) {
	if err := s.canOwnAnother(ownerID); err != nil {
		return nil, err
	}
	base := strings.TrimSpace(handle)
	if base == "" {
		base = displayName
	}
	name, err := s.uniqueName(base)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = name
	}
	ws := &models.Workspace{Name: name, DisplayName: displayName, Description: description, OwnerID: ownerID}
	if err := s.repo.CreateWithOwner(ws); err != nil {
		return nil, err
	}
	// Auto-provision the workspace's default Docker network (best-effort).
	if s.nets != nil {
		if _, err := s.nets.EnsureDefault(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to create default network for workspace", "workspace", ws.ID, "error", err)
		}
	}
	// Seed the workspace's default security policies (best-effort). The system
	// workspace uses EnsureSystem, not this path, so it is never seeded.
	if s.seeder != nil {
		if err := s.seeder.SeedDefaults(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to seed default middlewares for workspace", "workspace", ws.ID, "error", err)
		}
	}
	return ws, nil
}

func (s *Service) Get(id uint) (*models.Workspace, error) { return s.repo.FindByID(id) }
func (s *Service) ListForUser(userID uint) ([]models.WorkspaceWithRole, error) {
	return s.repo.ListForUser(userID)
}

func (s *Service) Update(ws *models.Workspace) error { return s.repo.Update(ws) }

// SetName validates and applies a new handle (the workspace name) to ws in
// memory (the caller persists via Update). The value is normalized to the
// canonical handle form; it must be valid, non-reserved, and unique. The
// built-in system workspace's name is immutable. A no-op when unchanged.
//
// Unlike Create, this does not auto-suffix: a rename to a taken or reserved
// handle is an error, so the caller gets exactly the handle they asked for.
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
	// Crypto-shred: drop the workspace's encryption keys so its ciphertext is
	// unrecoverable (defence in depth alongside the row deletes). Best-effort.
	if s.keys != nil {
		if err := s.keys.ShredWorkspace(id); err != nil {
			logger.Warn("crypto-shred: failed to delete workspace keys", "workspace", id, "error", err)
		}
	}
	return nil
}

// SystemName is the handle of the built-in platform system workspace. It is a
// reserved handle (see the slug package), claimable only by EnsureSystem.
const SystemName = "system"

// EnsureSystem creates the built-in platform system workspace if it does not yet
// exist, owned by ownerID (the platform admin). It is privileged (host-port
// bindings auto-approve) and flagged System so it cannot be deleted. Idempotent:
// returns the existing workspace when already present.
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
	// Pin the platform workspace to the Unlimited plan so platform-managed
	// infrastructure (e.g. per-node gateways) is never constrained by tenant
	// quotas when plan enforcement is enabled. Existing installs are backfilled by
	// the "assign_unlimited_plan_to_system_workspace" upgrade step.
	s.pinUnlimitedPlan(ws)
	if s.nets != nil {
		if _, err := s.nets.EnsureDefault(context.Background(), ws.ID); err != nil {
			logger.Warn("failed to create default network for system workspace", "workspace", ws.ID, "error", err)
		}
	}
	logger.Info("platform system workspace ready", "workspace", ws.ID, "name", ws.Name)
	return ws, nil
}

// pinUnlimitedPlan assigns the Unlimited plan to ws. Non-fatal: a missing plan
// repo or catalog leaves the workspace on the default plan (logged).
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

// RemoveMember removes a member, refusing to remove the last owner or a member
// who outranks the caller.
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

	// If the email belongs to an existing member, reject.
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

// PendingInvitation is an invitation enriched with the context the invited user
// needs to act on it — they are not yet a member and can't read the workspace.
type PendingInvitation struct {
	ID            uint                 `json:"id"`
	WorkspaceID   uint                 `json:"workspace_id"`
	WorkspaceName string               `json:"workspace_name"`
	Role          models.WorkspaceRole `json:"role"`
	InvitedByName string               `json:"invited_by_name"`
	ExpiresAt     time.Time            `json:"expires_at"`
	CreatedAt     time.Time            `json:"created_at"`
}

// ListInvitationsForEmail returns the pending invitations addressed to an email,
// enriched with the workspace name and inviter for display.
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

// AcceptByID consumes an invitation by id, provided it is addressed to the
// user's own email. Used by the in-app pending-invitations list, where the raw
// token (delivered by email) is not available.
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

// acceptInvitation validates a pending invitation and adds the user as a member.
func (s *Service) acceptInvitation(inv *models.WorkspaceInvitation, userID uint) (*models.Workspace, error) {
	if inv.Status != models.InvitationStatusPending || time.Now().After(inv.ExpiresAt) {
		return nil, ErrInvalidInvite
	}
	if _, err := s.repo.FindMember(inv.WorkspaceID, userID); err == nil {
		return nil, ErrAlreadyMember
	}
	// Re-check capacity at the moment the seat is actually consumed.
	if err := s.checkMemberCapacity(inv.WorkspaceID); err != nil {
		return nil, err
	}
	// An owner-role invitation grants ownership (counts against the owned limit);
	// any other role is a non-owner join (counts against the membership limit).
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

// uniqueName derives a valid, non-reserved, unique workspace handle from base,
// suffixing (-1, -2, …) on collision. Shares the grammar and reserved-word set
// with usernames via the slug package.
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
