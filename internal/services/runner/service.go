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

// Package runner manages build runners: dedicated machines that never host apps, dialing in over a tunnel.
package runner

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

var (
	ErrNotFound     = errors.New("runner not found")
	ErrNameRequired = errors.New("runner name is required")
	ErrNameTaken    = errors.New("a runner with that name already exists")
	ErrBadToken     = errors.New("invalid runner token")
)

// Distinct from the node join-token prefix "mbn_" so the two token scopes never collide.
const tokenPrefix = "mbr_"

// Two missed 30s heartbeats count as a broken connection rather than one late beat.
const contactLostAfter = 90 * time.Second

// WorkspaceID and Scope are set by the caller, not bound from the request.
type Input struct {
	Name        string
	DisplayName string
	Labels      []string
	Concurrency int
}

// Service owns runner records and their registration tokens.
type Service struct {
	repo   *repositories.RunnerRepository
	quota  *quota.Service
	leases *repositories.RunnerLeaseRepository
	conn   ConnRegistry
	image  string
}

func NewService(repo *repositories.RunnerRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

func (s *Service) SetImage(image string) { s.image = image }

func (s *Service) Image() string {
	if s.image == "" {
		return "miabi/runner:latest"
	}
	return s.image
}

// ListWorkspace returns a workspace's own runners.
func (s *Service) ListWorkspace(workspaceID uint) ([]models.Runner, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// ListShared returns the platform-shared runner pool (admin view).
func (s *Service) ListShared() ([]models.Runner, error) {
	return s.repo.ListShared()
}

func (s *Service) ListUsable(workspaceID uint) ([]models.Runner, error) {
	return s.schedulable(workspaceID)
}

// UsableShared is the tenant-facing list, so a workspace is never shown a runner its plan refuses.
func (s *Service) UsableShared(workspaceID uint) ([]models.Runner, error) {
	all, err := s.schedulable(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Runner, 0, len(all))
	for _, r := range all {
		if r.WorkspaceID == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// Single candidate-set choke point so a plan can't be enforced in one consumer and forgotten in another.
func (s *Service) schedulable(workspaceID uint) ([]models.Runner, error) {
	includeShared := s.quota.Require(workspaceID, quota.CapPlatformRunners) == nil
	all, err := s.repo.ListSchedulable(workspaceID, includeShared)
	if err != nil || !includeShared {
		return all, err
	}
	binding, bound := s.quota.EffectivePlatformRunners(workspaceID)
	if !bound {
		return all, nil
	}
	return offeredByPlan(all, binding), nil
}

// Owned runners pass untouched: a plan describes only what the platform lends out.
func offeredByPlan(all []models.Runner, binding quota.RunnerBinding) []models.Runner {
	out := make([]models.Runner, 0, len(all))
	for _, r := range all {
		if r.WorkspaceID != nil || binding.Allows(r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// BoundBy lets deleting a plan-named runner be refused rather than silently narrowing that plan's pool.
func (s *Service) BoundBy(id uint) ([]models.Plan, error) {
	r, err := s.repo.FindShared(id)
	if err != nil {
		return nil, err
	}
	return s.repo.BoundBy(r.Name)
}

// GetWorkspace fetches one of a workspace's own runners.
func (s *Service) GetWorkspace(workspaceID, id uint) (*models.Runner, error) {
	m, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// Get fetches a runner of any scope, for callers that filter by owner themselves.
func (s *Service) Get(id uint) (*models.Runner, error) {
	m, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// GetShared fetches one platform-shared runner (admin).
func (s *Service) GetShared(id uint) (*models.Runner, error) {
	m, err := s.repo.FindShared(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// CreateWorkspace returns the one-time registration token; only its hash is stored.
func (s *Service) CreateWorkspace(workspaceID, createdByID uint, in Input) (*models.Runner, string, error) {
	if s.quota.Enabled() {
		n, _ := s.repo.CountByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceRunners, int(n)); err != nil {
			return nil, "", err
		}
	}
	ws := workspaceID
	var by *uint
	if createdByID != 0 {
		by = &createdByID
	}
	return s.create(&ws, models.ScopeWorkspace, by, in)
}

// BuiltinRunnerName is the reserved handle of the co-located built-in runner.
const BuiltinRunnerName = "builtin"

// EnsureBuiltin issues a fresh token each start, invalidating a stale co-located container's token.
func (s *Service) EnsureBuiltin() (*models.Runner, string, error) {
	shared, err := s.repo.ListShared()
	if err != nil {
		return nil, "", err
	}
	for i := range shared {
		if shared[i].Name == BuiltinRunnerName {
			token, terr := s.regenToken(&shared[i])
			return &shared[i], token, terr
		}
	}
	return s.CreateShared(0, Input{Name: BuiltinRunnerName, DisplayName: "Built-in runner", Concurrency: 1})
}

// CreateShared registers a platform-shared runner (admin; nil workspace).
func (s *Service) CreateShared(createdByID uint, in Input) (*models.Runner, string, error) {
	var by *uint
	if createdByID != 0 {
		by = &createdByID
	}
	return s.create(nil, models.ScopeShared, by, in)
}

func (s *Service) create(workspaceID *uint, scope models.RunnerScope, createdByID *uint, in Input) (*models.Runner, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", ErrNameRequired
	}
	handle, err := slug.Unique(name, "runner", func(candidate string) (bool, error) {
		return s.repo.ExistsByName(workspaceID, candidate, 0)
	})
	if err != nil {
		return nil, "", err
	}
	token := generateToken()
	m := &models.Runner{
		Name:        handle,
		DisplayName: displayName(in.DisplayName, name),
		WorkspaceID: workspaceID,
		Scope:       scope,
		Labels:      normalizeLabels(in.Labels),
		Concurrency: normalizeConcurrency(in.Concurrency),
		Status:      models.RunnerStatusOffline,
		Enabled:     true,
		TokenHash:   hashToken(token),
		CreatedByID: createdByID,
	}
	if err := s.repo.Create(m); err != nil {
		return nil, "", err
	}
	return m, token, nil
}

// UpdateWorkspace edits a workspace-owned runner's mutable fields.
func (s *Service) UpdateWorkspace(workspaceID, id uint, in Input) (*models.Runner, error) {
	m, err := s.GetWorkspace(workspaceID, id)
	if err != nil {
		return nil, err
	}
	return s.applyUpdate(m, in)
}

// UpdateShared edits a platform-shared runner (admin).
func (s *Service) UpdateShared(id uint, in Input) (*models.Runner, error) {
	m, err := s.GetShared(id)
	if err != nil {
		return nil, err
	}
	return s.applyUpdate(m, in)
}

func (s *Service) applyUpdate(m *models.Runner, in Input) (*models.Runner, error) {
	if name := strings.TrimSpace(in.Name); name != "" && name != m.Name {
		exists, err := s.repo.ExistsByName(m.WorkspaceID, name, m.ID)
		if err != nil {
			return nil, err
		}
		if exists || !slug.IsValid(name) {
			return nil, ErrNameTaken
		}
		m.Name = name
	}
	if in.DisplayName != "" {
		m.DisplayName = strings.TrimSpace(in.DisplayName)
	}
	if in.Labels != nil {
		m.Labels = normalizeLabels(in.Labels)
	}
	if in.Concurrency > 0 {
		m.Concurrency = normalizeConcurrency(in.Concurrency)
	}
	if err := s.repo.Update(m); err != nil {
		return nil, err
	}
	return m, nil
}

// Scope is enforced by the caller's Get*.
func (s *Service) SetCordoned(m *models.Runner, cordoned bool) error {
	m.Cordoned = cordoned
	return s.repo.Update(m)
}

func (s *Service) SetEnabled(m *models.Runner, enabled bool) error {
	m.Enabled = enabled
	return s.repo.Update(m)
}

// DeleteWorkspace removes a workspace-owned runner.
func (s *Service) DeleteWorkspace(workspaceID, id uint) error {
	if _, err := s.GetWorkspace(workspaceID, id); err != nil {
		return err
	}
	return s.repo.Delete(id)
}

// DeleteShared removes a platform-shared runner (admin).
func (s *Service) DeleteShared(id uint) error {
	if _, err := s.GetShared(id); err != nil {
		return err
	}
	return s.repo.Delete(id)
}

func (s *Service) RegenerateTokenWorkspace(workspaceID, id uint) (string, error) {
	m, err := s.GetWorkspace(workspaceID, id)
	if err != nil {
		return "", err
	}
	return s.regenToken(m)
}

// RegenerateTokenShared issues a fresh registration token for a shared runner.
func (s *Service) RegenerateTokenShared(id uint) (string, error) {
	m, err := s.GetShared(id)
	if err != nil {
		return "", err
	}
	return s.regenToken(m)
}

func (s *Service) regenToken(m *models.Runner) (string, error) {
	token := generateToken()
	m.TokenHash = hashToken(token)
	if err := s.repo.Update(m); err != nil {
		return "", err
	}
	return token, nil
}

// A stale last-seen counts as a break: a control plane killed mid-connection never runs MarkDisconnected.
func connectionBroke(m *models.Runner, now time.Time) bool {
	return m.ConnectedSince == nil ||
		m.Status == models.RunnerStatusOffline ||
		m.LastSeenAt == nil ||
		now.Sub(*m.LastSeenAt) > contactLostAfter
}

// MarkConnected is best-effort (a missing runner is ignored) so it can run on every heartbeat.
func (s *Service) MarkConnected(id uint, f models.RunnerFacts) {
	m, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	now := time.Now()
	if f.RemoteIP != "" {
		m.RemoteIP = f.RemoteIP
	}

	if connectionBroke(m, now) {
		m.ConnectedSince = &now
	}

	if m.Status != models.RunnerStatusDraining {
		m.Status = models.RunnerStatusOnline
	}
	m.LastSeenAt = &now
	if f.OS != "" {
		m.OS = f.OS
	}
	if f.Arch != "" {
		m.Arch = f.Arch
	}
	if f.Version != "" {
		m.Version = f.Version
	}
	// Replaced, not merged: a runner downgraded to a release without a feature must stop getting its jobs.
	m.Features = f.Features
	_ = s.repo.Update(m)
}

// MarkDisconnected flips a runner offline when its tunnel drops. Best-effort.
func (s *Service) MarkDisconnected(id uint) {
	m, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	m.Status = models.RunnerStatusOffline
	m.ConnectedSince = nil // uptime ends here; the next connection starts a new one
	_ = s.repo.Update(m)
}

func (s *Service) Authenticate(token string) (*models.Runner, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return nil, ErrBadToken
	}
	m, err := s.repo.FindByTokenHash(hashToken(token))
	if err != nil {
		return nil, ErrBadToken
	}
	if subtle.ConstantTimeCompare([]byte(m.TokenHash), []byte(hashToken(token))) != 1 {
		return nil, ErrBadToken
	}
	if !m.Enabled {
		return nil, ErrBadToken
	}
	return m, nil
}

func displayName(display, name string) string {
	if d := strings.TrimSpace(display); d != "" {
		return d
	}
	return name
}

// Sorted and de-duplicated so the scheduler's subset match is order-independent and stable.
func normalizeLabels(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizeConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		logger.Error("runner token generation failed", "error", err)
	}
	return tokenPrefix + hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
