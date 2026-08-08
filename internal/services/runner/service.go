// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package runner manages build/pipeline runner records: registration, scope (workspace-owned vs
// platform-shared), labels, concurrency and reachability. Runners are dedicated build machines that never
// host apps, dialing in over an outbound tunnel with a tightly-scoped registration token.
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

// tokenPrefix marks Miabi runner registration tokens (distinct from the node
// join-token prefix "mbn_" so the two token scopes never collide).
const tokenPrefix = "mbr_"

// contactLostAfter is the gap in a runner's heartbeat (30s) that counts as the
// connection having broken, rather than one late beat. Two missed beats.
const contactLostAfter = 90 * time.Second

// Input is the mutable set of fields a create/update accepts. Scope-specific
// fields (WorkspaceID, Scope) are set by the caller, not bound from the request.
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

// SetQuota wires the plan/quota enforcer (nil-safe; gates MaxRunners on create
// and the platform-runners capability on shared-pool use).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetImage records the configured miabi-runner image (MIABI_RUNNER_IMAGE) so the
// panel's generated `docker run` enrollment command uses the operator's image.
func (s *Service) SetImage(image string) { s.image = image }

// Image returns the configured runner image, falling back to the current default
// when unset, so the enrollment command is always complete.
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

// ListUsable returns the runners a workspace's jobs may target: its own runners
// plus, when its plan grants the platform-runners capability, the shared pool.
func (s *Service) ListUsable(workspaceID uint) ([]models.Runner, error) {
	includeShared := s.quota.Require(workspaceID, quota.CapPlatformRunners) == nil
	return s.repo.ListSchedulable(workspaceID, includeShared)
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

// CreateWorkspace registers a workspace-owned runner and returns the one-time
// registration token (shown once; only its hash is stored). Quota-checked
// against the workspace's MaxRunners.
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

// EnsureBuiltin finds or creates the platform-shared built-in runner and issues it a fresh registration token.
// The co-located container gets a new token each start, so a stale container's token is invalidated. Returns
// the runner and its one-time token.
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

// SetCordoned holds a runner out of scheduling (or releases it) without
// disconnecting it. scope is enforced by the caller's Get*.
func (s *Service) SetCordoned(m *models.Runner, cordoned bool) error {
	m.Cordoned = cordoned
	return s.repo.Update(m)
}

// SetEnabled toggles a runner's on/off switch (a disabled runner never receives
// jobs even while connected).
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

// RegenerateTokenWorkspace issues a fresh registration token for a
// workspace-owned runner, invalidating the old one.
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

// connectionBroke reports whether a runner's stored state shows the previous connection genuinely ended,
// deciding when ConnectedSince restarts. A stale last-seen counts as a break even when the row says online:
// a control plane killed mid-connection never runs MarkDisconnected, so Status alone would claim otherwise.
func connectionBroke(m *models.Runner, now time.Time) bool {
	return m.ConnectedSince == nil ||
		m.Status == models.RunnerStatusOffline ||
		m.LastSeenAt == nil ||
		now.Sub(*m.LastSeenAt) > contactLostAfter
}

// MarkConnected records that a runner's tunnel is live: online status, a fresh last-seen, and its
// self-reported platform facts. Best-effort — a missing runner is ignored — so the connection manager can
// call it on connect and on each heartbeat without handling errors.
func (s *Service) MarkConnected(id uint, os, arch, version, remoteIP string) {
	m, err := s.repo.FindByID(id)
	if err != nil {
		return
	}
	now := time.Now()
	if remoteIP != "" {
		m.RemoteIP = remoteIP
	}

	if connectionBroke(m, now) {
		m.ConnectedSince = &now
	}

	if m.Status != models.RunnerStatusDraining {
		m.Status = models.RunnerStatusOnline
	}
	m.LastSeenAt = &now
	if os != "" {
		m.OS = os
	}
	if arch != "" {
		m.Arch = arch
	}
	if version != "" {
		m.Version = version
	}
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

// Authenticate resolves a runner from a presented registration token,
// constant-time comparing the hash. Used by the runner-gateway endpoint when a
// runner dials in. A disabled runner is rejected.
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

// normalizeLabels trims, drops blanks, de-duplicates and sorts labels so the
// scheduler's subset match is order-independent and stable.
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
