// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package release turns the per-application Release — a deployed, rollback-able version — into a promotable
// artifact: a workspace-wide catalog with provenance, approval gates per environment, and promotion
// (re-pointing an application at a release) that the gate guards.
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

// ListPaged returns a page of workspace releases (with app identity) plus the
// total count.
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

// load resolves a release and verifies it belongs to the workspace, returning
// the owning application too.
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

// ApprovalStatus is a release's approval standing across all environments plus
// the raw approval log.
type ApprovalStatus struct {
	Environments []EnvApproval            `json:"environments"`
	Approvals    []models.ReleaseApproval `json:"approvals"`
}

// Approvals returns the approval standing of a release across the workspace's
// environments.
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

// Approve records one approver's decision on promoting a release into an
// environment.
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

// Promote re-points the release's application at it (making it the active
// release), gated by the target environment's required approvals. Driven through
// the existing deploy pipeline.
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
	dep, err := s.appSvc.Rollback(app, rel.ID)
	if err != nil {
		return nil, err
	}
	// Record the promotion as an approval entry so the audit trail shows who
	// promoted the release into the environment.
	_ = s.envs.CreateApproval(&models.ReleaseApproval{
		WorkspaceID: workspaceID, ReleaseID: releaseID, EnvironmentID: &environmentID,
		ApproverID: userID, Approved: true, Comment: "promoted",
	})
	return dep, nil
}
