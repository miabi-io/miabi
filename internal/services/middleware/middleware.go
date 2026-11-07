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

// Package middleware manages workspace-owned Goma Gateway middlewares and reconciles them into the proxy provider.
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
	// Re-exported so handlers can map it to 422 without importing the catalog package.
	ErrInvalidRule = mwcatalog.ErrInvalidRule
)

// WorkspaceSyncer re-renders routes and middlewares together, so a route always sees the current middleware definition.
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
	// Name must already be canonical slug form (it becomes the Goma middleware name); DisplayName falls back to Name.
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
	in.Rule = mwcatalog.DropRetired(in.Type, in.Rule)
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
	in.Rule = mwcatalog.DropRetired(effectiveType, in.Rule)
	if err := mwcatalog.Validate(effectiveType, in.Rule); err != nil {
		return nil, err
	}
	// Restore redacted ("***") secrets before re-encrypting, so editing other fields never wipes a password.
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

// GetByName resolves a middleware by name, since numeric ids are not portable across installs.
func (s *Service) GetByName(workspaceID uint, name string) (*models.Middleware, error) {
	list, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) List(workspaceID uint) ([]models.Middleware, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// SeedDefaults creates the default policy set once; a no-op if the workspace owns any middleware, so user edits survive.
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

// Re-render the whole workspace so the middleware change is published together with the routes that reference it.
func (s *Service) sync(ctx context.Context, workspaceID uint) {
	if s.syncer != nil {
		_ = s.syncer.SyncWorkspaceProxy(ctx, workspaceID)
	}
}
