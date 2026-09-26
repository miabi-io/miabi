// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// invalidateChannel tells every process a policy changed. The TTL below bounds staleness when
// Redis is absent or a message is lost.
const (
	invalidateChannel = "miabi:secpolicy:changed"
	cacheTTL          = 30 * time.Second
)

// Entitlements is the licence check. Satisfied by enterprise.EE.
type Entitlements interface {
	Has(flag string) bool
}

// Workspaces resolves a workspace's plan and organization for scope resolution.
type Workspaces interface {
	FindByID(id uint) (*models.Workspace, error)
}

type cached struct {
	rows []models.SecurityPolicy
	at   time.Time
}

// Service evaluates and stores Security Center policies.
type Service struct {
	repo       *repositories.SecurityPolicyRepository
	workspaces Workspaces
	ee         Entitlements
	enabled    bool
	audit      *audit.Logger
	rdb        *redis.Client

	mu    sync.RWMutex
	cache map[string]cached
}

// NewService builds the policy service. enabled is the MIABI_SECURITY_POLICIES kill switch.
func NewService(repo *repositories.SecurityPolicyRepository, workspaces Workspaces, ee Entitlements, enabled bool) *Service {
	return &Service{repo: repo, workspaces: workspaces, ee: ee, enabled: enabled, cache: map[string]cached{}}
}

// SetAudit forwards every decision and change to the audit log, and from there to the SIEM stream.
func (s *Service) SetAudit(l *audit.Logger) { s.audit = l }

// SetRedis wires cross-process cache invalidation.
func (s *Service) SetRedis(rdb *redis.Client) { s.rdb = rdb }

// Entitled reports whether the edition includes the Security Center.
func (s *Service) Entitled() bool {
	return s != nil && s.ee != nil && s.ee.Has(enterprise.FlagSecurityPolicies)
}

// Enabled reports whether the kill switch leaves stored policies in force.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Active reports whether stored policies are applied at all.
func (s *Service) Active() bool { return s.Enabled() && s.Entitled() }

// Listen drops the cache whenever another process announces a change. Blocks until ctx ends.
func (s *Service) Listen(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	sub := s.rdb.Subscribe(ctx, invalidateChannel)
	defer func() { _ = sub.Close() }()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			s.invalidate(msg.Payload)
		}
	}
}

func (s *Service) invalidate(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "" {
		s.cache = map[string]cached{}
		return
	}
	delete(s.cache, kind)
}

func (s *Service) announce(kind string) {
	s.invalidate(kind)
	if s.rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.rdb.Publish(ctx, invalidateChannel, kind).Err(); err != nil {
		logger.Warn("security policy: announce change", "error", err)
	}
}

func (s *Service) rows(kind string) ([]models.SecurityPolicy, error) {
	s.mu.RLock()
	c, ok := s.cache[kind]
	s.mu.RUnlock()
	if ok && time.Since(c.at) < cacheTTL {
		return c.rows, nil
	}
	rows, err := s.repo.ListByKind(kind)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[kind] = cached{rows: rows, at: time.Now()}
	s.mu.Unlock()
	return rows, nil
}

// effective returns the rule that governs kind for a workspace, or nil when none applies.
func (s *Service) effective(kind string, workspaceID uint) (*models.SecurityPolicy, error) {
	rows, err := s.rows(kind)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var planID, orgID uint
	if workspaceID != 0 && s.workspaces != nil {
		if ws, werr := s.workspaces.FindByID(workspaceID); werr == nil {
			if ws.PlanID != nil {
				planID = *ws.PlanID
			}
			if ws.OrganizationID != nil {
				orgID = *ws.OrganizationID
			}
		}
	}
	return resolve(rows, workspaceID, planID, orgID), nil
}

// resolve picks the most specific rule, falling back to the platform rule when the specific one
// would loosen it without the platform allowing exceptions. Pure, for tests.
func resolve(rows []models.SecurityPolicy, workspaceID, planID, orgID uint) *models.SecurityPolicy {
	find := func(scope string, id uint) *models.SecurityPolicy {
		if scope != ScopePlatform && id == 0 {
			return nil
		}
		for i := range rows {
			if rows[i].ScopeType == scope && rows[i].ScopeID == id {
				return &rows[i]
			}
		}
		return nil
	}
	platform := find(ScopePlatform, 0)
	for _, c := range []struct {
		scope string
		id    uint
	}{{ScopeWorkspace, workspaceID}, {ScopeOrganization, orgID}, {ScopePlan, planID}} {
		p := find(c.scope, c.id)
		if p == nil {
			continue
		}
		if platform != nil && !platform.AllowExceptions && loosens(platform, p) {
			return platform
		}
		return p
	}
	return platform
}

func loosens(base, narrow *models.SecurityPolicy) bool {
	switch base.Kind {
	case KindPorts:
		b, berr := ParsePortsSpec(base.Spec)
		n, nerr := ParsePortsSpec(narrow.Spec)
		if berr != nil || nerr != nil {
			return true
		}
		return portsLoosens(base.Mode, b, narrow.Mode, n)
	}
	return modeRank(narrow.Mode) < modeRank(base.Mode)
}

// SaveInput is a policy write.
type SaveInput struct {
	Kind            string
	ScopeType       string
	ScopeID         uint
	Mode            string
	AllowExceptions bool
	Spec            string
}

// List returns every stored rule.
func (s *Service) List() ([]models.SecurityPolicy, error) { return s.repo.ListAll() }

// Save validates and upserts the rule for one kind at one scope. The change is audited with the
// old and new spec, and is itself a security event.
func (s *Service) Save(actorID uint, ip string, in SaveInput) (*models.SecurityPolicy, error) {
	if err := ValidScope(in.ScopeType, in.ScopeID); err != nil {
		return nil, err
	}
	if !ValidMode(in.Mode) {
		return nil, fmt.Errorf("%w: unknown mode %q", ErrInvalidSpec, in.Mode)
	}
	spec, err := normalizeSpec(in.Kind, in.Spec)
	if err != nil {
		return nil, err
	}
	if in.Kind == KindAdminAccess {
		if in.ScopeType != ScopePlatform {
			return nil, fmt.Errorf("%w: admin_access is a platform-wide policy", ErrInvalidSpec)
		}
		if in.Mode == ModeAudit {
			return nil, fmt.Errorf("%w: admin_access has no audit mode; use off or enforce", ErrInvalidSpec)
		}
	}
	if in.ScopeType != ScopePlatform && in.AllowExceptions {
		return nil, fmt.Errorf("%w: only the platform rule can allow exceptions", ErrInvalidSpec)
	}
	next := &models.SecurityPolicy{Kind: in.Kind, ScopeType: in.ScopeType, ScopeID: in.ScopeID,
		Mode: in.Mode, AllowExceptions: in.AllowExceptions, Spec: spec}
	if in.ScopeType != ScopePlatform {
		if base, berr := s.repo.FindScope(in.Kind, ScopePlatform, 0); berr == nil && !base.AllowExceptions && loosens(base, next) {
			return nil, ErrLoosens
		}
	}

	oldSpec, oldMode := "", ""
	row, err := s.repo.FindScope(in.Kind, in.ScopeType, in.ScopeID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = next
	case err != nil:
		return nil, err
	default:
		oldSpec, oldMode = row.Spec, row.Mode
		row.Mode, row.AllowExceptions, row.Spec = next.Mode, next.AllowExceptions, next.Spec
		row.Version++
	}
	row.UpdatedBy = &actorID
	if err := s.repo.Save(row); err != nil {
		return nil, err
	}
	s.announce(in.Kind)
	s.recordChange(actorID, ip, row, "save", map[string]any{
		"old_mode": oldMode, "new_mode": row.Mode, "old_spec": oldSpec, "new_spec": row.Spec, "version": row.Version,
	})
	return row, nil
}

// Delete removes a rule. Deleting the platform rule returns that kind to Community behaviour.
func (s *Service) Delete(actorID uint, ip string, id uint) error {
	row, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.announce(row.Kind)
	s.recordChange(actorID, ip, row, "delete", map[string]any{"old_mode": row.Mode, "old_spec": row.Spec})
	return nil
}

func normalizeSpec(kind, raw string) (string, error) {
	switch kind {
	case KindPorts:
		spec, err := ParsePortsSpec(raw)
		if err != nil {
			return "", err
		}
		return spec.Encode(), nil
	case KindAdminAccess:
		spec, _, err := ParseAdminAccessSpec(raw)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(spec)
		return string(b), nil
	}
	return "", fmt.Errorf("%w: unknown policy kind %q", ErrInvalidSpec, kind)
}

func (s *Service) recordChange(actorID uint, ip string, p *models.SecurityPolicy, op string, meta map[string]any) {
	scope := scopeLabel(p.ScopeType, p.ScopeID)
	s.record(&models.SecurityEvent{
		Kind: KindPolicy, Action: op, Decision: DecisionChange, UserID: &actorID,
		Resource: p.Kind, PolicyID: &p.ID, Scope: scope,
		Reason: fmt.Sprintf("%s policy %s at %s (mode %s)", p.Kind, op, scope, p.Mode),
	})
	if s.audit != nil {
		meta["scope"] = scope
		s.audit.Record(audit.Entry{ActorID: &actorID, Action: "security.policy_" + op,
			TargetType: "security_policy", TargetID: strconv.FormatUint(uint64(p.ID), 10), IP: ip, Metadata: meta})
	}
}

// record persists an event and forwards it to the audit log. Never fails the caller.
func (s *Service) record(e *models.SecurityEvent) {
	if err := s.repo.CreateEvent(e); err != nil {
		logger.Error("security policy: record event", "kind", e.Kind, "error", err)
	}
	if s.audit == nil || e.Decision == DecisionChange {
		return
	}
	meta := map[string]any{"decision": e.Decision, "scope": e.Scope, "reason": e.Reason}
	if e.PolicyID != nil {
		meta["policy_id"] = *e.PolicyID
	}
	s.audit.Record(audit.Entry{ActorID: e.UserID, WorkspaceID: e.WorkspaceID,
		Action: "security." + e.Kind + "_" + e.Decision, TargetType: e.Kind, TargetID: e.Resource, Metadata: meta})
}

// Events lists recorded decisions.
func (s *Service) Events(f repositories.EventFilter) ([]models.SecurityEvent, error) {
	return s.repo.ListEvents(f)
}

// CountSince buckets recent events for the overview.
func (s *Service) CountSince(since time.Time) ([]repositories.EventCount, error) {
	return s.repo.CountEventsSince(since)
}

// PruneEvents drops events older than the retention window.
func (s *Service) PruneEvents(retention time.Duration) (int64, error) {
	return s.repo.PruneEvents(time.Now().Add(-retention))
}
