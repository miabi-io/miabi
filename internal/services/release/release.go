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

// Package release turns per-app releases into promotable artifacts with provenance and per-environment approval gates.
package release

import (
	"errors"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound        = errors.New("release not found")
	ErrEnvNotFound     = errors.New("environment not found")
	ErrApprovalsNeeded = errors.New("release lacks the approvals this environment requires")
)

// Service exposes the workspace release catalog, approvals, and promotion.
type Service struct {
	releases *repositories.ReleaseRepository
	apps     *repositories.ApplicationRepository
	envs     *repositories.EnvironmentRepository
	appSvc   *application.Service
}

// NewService wires the release service.
func NewService(releases *repositories.ReleaseRepository, apps *repositories.ApplicationRepository, envs *repositories.EnvironmentRepository, appSvc *application.Service) *Service {
	return &Service{releases: releases, apps: apps, envs: envs, appSvc: appSvc}
}

// View is a release enriched with its application's identity for the catalog.
type View struct {
	models.Release
	ApplicationName        string `json:"application_name"`         // unique slug handle
	ApplicationDisplayName string `json:"application_display_name"` // free-text label
}

// List returns every release in the workspace, newest first, with app identity.
func (s *Service) List(workspaceID uint) ([]View, error) {
	releases, err := s.releases.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return s.enrich(workspaceID, releases)
}

func (s *Service) ListPaged(workspaceID uint, limit, offset int) ([]View, int64, error) {
	releases, total, err := s.releases.ListByWorkspacePaged(workspaceID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	views, err := s.enrich(workspaceID, releases)
	if err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

func (s *Service) enrich(workspaceID uint, releases []models.Release) ([]View, error) {
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	name := map[uint]string{}
	displayName := map[uint]string{}
	for i := range apps {
		name[apps[i].ID] = apps[i].Name
		displayName[apps[i].ID] = apps[i].DisplayName
	}
	out := make([]View, 0, len(releases))
	for i := range releases {
		out = append(out, View{Release: releases[i], ApplicationName: name[releases[i].ApplicationID], ApplicationDisplayName: displayName[releases[i].ApplicationID]})
	}
	return out, nil
}

func (s *Service) load(workspaceID, releaseID uint) (*models.Release, *models.Application, error) {
	rel, err := s.releases.FindByID(releaseID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	app, err := s.apps.FindByID(rel.ApplicationID)
	if err != nil || app.WorkspaceID != workspaceID {
		return nil, nil, ErrNotFound
	}
	return rel, app, nil
}

// EnvApproval reports a release's approval standing for one environment.
type EnvApproval struct {
	EnvironmentID     uint   `json:"environment_id"`
	EnvironmentName   string `json:"environment_name"`
	RequiredApprovals int    `json:"required_approvals"`
	Approvals         int    `json:"approvals"`
	Satisfied         bool   `json:"satisfied"`
}

type ApprovalStatus struct {
	Environments []EnvApproval            `json:"environments"`
	Approvals    []models.ReleaseApproval `json:"approvals"`
}

func (s *Service) Approvals(workspaceID, releaseID uint) (*ApprovalStatus, error) {
	if _, _, err := s.load(workspaceID, releaseID); err != nil {
		return nil, err
	}
	envs, err := s.envs.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	status := &ApprovalStatus{}
	for i := range envs {
		envID := envs[i].ID
		count, _ := s.envs.CountApprovals(releaseID, &envID)
		status.Environments = append(status.Environments, EnvApproval{
			EnvironmentID: envID, EnvironmentName: envs[i].Name,
			RequiredApprovals: envs[i].RequiredApprovals, Approvals: int(count),
			Satisfied: int(count) >= envs[i].RequiredApprovals,
		})
	}
	approvals, err := s.envs.ListApprovals(releaseID)
	if err != nil {
		return nil, err
	}
	status.Approvals = approvals
	return status, nil
}

func (s *Service) Approve(workspaceID, releaseID uint, environmentID *uint, approverID uint, approved bool, comment string) error {
	if _, _, err := s.load(workspaceID, releaseID); err != nil {
		return err
	}
	if environmentID != nil {
		if _, err := s.envs.FindInWorkspace(workspaceID, *environmentID); err != nil {
			return ErrEnvNotFound
		}
	}
	return s.envs.CreateApproval(&models.ReleaseApproval{
		WorkspaceID: workspaceID, ReleaseID: releaseID, EnvironmentID: environmentID,
		ApproverID: approverID, Approved: approved, Comment: comment,
	})
}

// Promote makes the release its app's active one, gated by the target environment's required approvals.
func (s *Service) Promote(workspaceID, releaseID, environmentID, userID uint) (*models.Deployment, error) {
	rel, app, err := s.load(workspaceID, releaseID)
	if err != nil {
		return nil, err
	}
	env, err := s.envs.FindInWorkspace(workspaceID, environmentID)
	if err != nil {
		return nil, ErrEnvNotFound
	}
	if env.RequiredApprovals > 0 {
		count, _ := s.envs.CountApprovals(releaseID, &environmentID)
		if int(count) < env.RequiredApprovals {
			return nil, ErrApprovalsNeeded
		}
	}
	dep, err := s.appSvc.Rollback(app, rel.ID, &userID)
	if err != nil {
		return nil, err
	}
	// Recorded as an approval so the audit trail shows who promoted the release.
	_ = s.envs.CreateApproval(&models.ReleaseApproval{
		WorkspaceID: workspaceID, ReleaseID: releaseID, EnvironmentID: &environmentID,
		ApproverID: userID, Approved: true, Comment: "promoted",
	})
	return dep, nil
}
