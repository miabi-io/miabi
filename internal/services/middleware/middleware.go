// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package middleware manages Goma Gateway middlewares owned by workspaces and
// reconciles them into the proxy provider.
package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNameRequired = errors.New("middleware name is required")
	ErrInvalidName  = errors.New("name must be lowercase letters, digits and hyphens (e.g. basic-auth)")
	ErrTypeRequired = errors.New("middleware type is required")
	ErrNameTaken    = errors.New("a middleware with this name already exists")
	ErrNotFound     = errors.New("middleware not found")
	// ErrInvalidRule re-exports the catalog's validation error so handlers can map
	// it to 422 MIDDLEWARE_INVALID_RULE without importing the catalog package.
	ErrInvalidRule = mwcatalog.ErrInvalidRule
)

// WorkspaceSyncer re-renders a workspace's complete proxy config (its routes and
// middlewares) into the gateway. The route service implements it; middlewares are
// written as part of that workspace re-render rather than on their own, so a
// route always sees the up-to-date definition of any middleware it references.
type WorkspaceSyncer interface {
	SyncWorkspaceProxy(ctx context.Context, workspaceID uint) error
}

type Service struct {
	repo   *repositories.MiddlewareRepository
	syncer WorkspaceSyncer
}

func NewService(repo *repositories.MiddlewareRepository, syncer WorkspaceSyncer) *Service {
	return &Service{repo: repo, syncer: syncer}
}

type Input struct {
	// Name is the unique slug handle; it must already be canonical slug form (it
	// becomes the Goma middleware name). DisplayName is the free-text label (falls
	// back to Name when blank).
	Name        string
	DisplayName string
	Type        string
	Paths       []string
	Rule        map[string]interface{}
}

func (s *Service) Create(ctx context.Context, workspaceID uint, in Input) (*models.Middleware, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if !slug.IsValid(name) {
		return nil, ErrInvalidName
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = name
	}
	if strings.TrimSpace(in.Type) == "" {
		return nil, ErrTypeRequired
	}
	if err := mwcatalog.Validate(in.Type, in.Rule); err != nil {
		return nil, err
	}
	taken, err := s.repo.ExistsByName(workspaceID, name)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	rule, err := mwcatalog.EncryptSecrets(in.Type, workspaceID, in.Rule)
	if err != nil {
		return nil, err
	}
	m := &models.Middleware{WorkspaceID: workspaceID, Name: name, DisplayName: displayName, Type: in.Type, Paths: in.Paths, Rule: rule}
	if err := s.repo.Create(m); err != nil {
		return nil, err
	}
	s.sync(ctx, m.WorkspaceID)
	return m, nil
}

func (s *Service) Update(ctx context.Context, workspaceID, id uint, in Input) (*models.Middleware, error) {
	m, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if in.Name != "" {
		name := strings.TrimSpace(in.Name)
		if !slug.IsValid(name) {
			return nil, ErrInvalidName
		}
		m.Name = name
	}
	effectiveType := m.Type
	if in.Type != "" {
		effectiveType = in.Type
	}
	if err := mwcatalog.Validate(effectiveType, in.Rule); err != nil {
		return nil, err
	}
	// Restore any secret the client left redacted ("***") from the stored rule
	// before re-encrypting, so editing other fields never wipes a password.
	merged := mwcatalog.MergeKeptSecrets(effectiveType, in.Rule, m.Rule)
	rule, err := mwcatalog.EncryptSecrets(effectiveType, workspaceID, merged)
	if err != nil {
		return nil, err
	}
	m.Type = effectiveType
	m.Paths = in.Paths
	m.Rule = rule
	if err := s.repo.Update(m); err != nil {
		return nil, err
	}
	s.sync(ctx, m.WorkspaceID)
	return m, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Middleware, error) {
	m, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return m, nil
}

func (s *Service) List(workspaceID uint) ([]models.Middleware, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// SeedDefaults creates the workspace's default policy set (mwcatalog.DefaultSeed)
// in one pass, syncing the proxy once at the end rather than per row. It is a
// no-op when the workspace already owns any middleware, so it is safe to call
// again and never clobbers user edits. Best-effort per row: a seed that fails to
// create (e.g. a name collision from a partial earlier run) is skipped, not fatal.
func (s *Service) SeedDefaults(ctx context.Context, workspaceID uint) error {
	n, err := s.repo.CountByWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // already has policies — don't seed over the user's set
	}
	var created bool
	for _, seed := range mwcatalog.DefaultSeed() {
		if !slug.IsValid(seed.Name) {
			continue // guards a malformed seed definition
		}
		rule, err := mwcatalog.EncryptSecrets(seed.Type, workspaceID, seed.Rule)
		if err != nil {
			continue
		}
		m := &models.Middleware{WorkspaceID: workspaceID, Name: seed.Name, DisplayName: seed.DisplayName, Type: seed.Type, Rule: rule}
		if err := s.repo.Create(m); err != nil {
			continue // e.g. name already taken; skip and keep going
		}
		created = true
	}
	if created {
		s.sync(ctx, workspaceID)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint) error {
	m, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.repo.Delete(m.ID); err != nil {
		return err
	}
	s.sync(ctx, m.WorkspaceID)
	return nil
}

// sync re-renders the whole workspace's proxy config so the middleware change
// (create/update/delete) is published together with the routes that reference it.
func (s *Service) sync(ctx context.Context, workspaceID uint) {
	if s.syncer != nil {
		_ = s.syncer.SyncWorkspaceProxy(ctx, workspaceID)
	}
}
