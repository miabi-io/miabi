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

package environment

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("environment not found")
	ErrNameTaken    = errors.New("an environment with this name already exists")
	ErrNameRequired = errors.New("name is required")
)

// Service manages environments.
type Service struct {
	repo *repositories.EnvironmentRepository
}

// NewService wires the environment service.
func NewService(repo *repositories.EnvironmentRepository) *Service {
	return &Service{repo: repo}
}

type Input struct {
	Name              string
	DisplayName       string
	Description       string
	Rank              int
	RequiredApprovals int
	GitSourceID       *uint
}

func (in *Input) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	if in.RequiredApprovals < 0 {
		in.RequiredApprovals = 0
	}
}

func (s *Service) Create(workspaceID uint, in Input) (*models.Environment, error) {
	in.normalize()
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
		return nil, ErrNameTaken
	}
	env := &models.Environment{
		WorkspaceID: workspaceID, Name: name, DisplayName: displayName, Description: in.Description,
		Rank: in.Rank, RequiredApprovals: in.RequiredApprovals, GitSourceID: in.GitSourceID,
	}
	if err := s.repo.Create(env); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.Environment, error) {
	env, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	in.normalize()
	if name := slug.Make(in.Name, ""); name != "" && name != env.Name {
		if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
			return nil, ErrNameTaken
		}
		env.Name = name
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" {
		env.DisplayName = dn
	}
	env.Description = in.Description
	env.Rank = in.Rank
	env.RequiredApprovals = in.RequiredApprovals
	env.GitSourceID = in.GitSourceID
	if err := s.repo.Update(env); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Environment, error) {
	env, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return env, nil
}

func (s *Service) List(workspaceID uint) ([]models.Environment, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) Delete(workspaceID, id uint) error {
	if _, err := s.Get(workspaceID, id); err != nil {
		return err
	}
	return s.repo.Delete(workspaceID, id)
}
