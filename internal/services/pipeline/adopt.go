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

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Longer than interactive inspect since this is a background path, but a hung clone must not wedge a deploy.
const adoptTimeout = 60 * time.Second

// SetGitRepos is optional; unset, AdoptForApp returns ErrAdoptionUnavailable and re-sync uses the stored spec.
func (s *Service) SetGitRepos(g *gitrepo.Service) { s.gitRepos = g }

func (s *Service) SetApps(a *repositories.ApplicationRepository) { s.apps = a }

// Named after the app, not metadata.name, so apps built from forks of one repo don't collide.
func (s *Service) AdoptForApp(ctx context.Context, app *models.Application, userID *uint) (*models.PipelineDefinition, error) {
	if s.gitRepos == nil {
		return nil, ErrAdoptionUnavailable
	}
	if app.SourceType != models.AppSourceGit {
		return nil, ErrNotGitApp
	}
	found, err := s.discoverForApp(ctx, app, "")
	if err != nil {
		return nil, err
	}
	if found.SpecError != "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSpec, found.SpecError)
	}
	if !found.HasPipeline() {
		return nil, ErrNoPipelineInRepo
	}
	enabled := true
	return s.Create(app.WorkspaceID, Input{
		Name:          s.uniqueName(app.WorkspaceID, app.Name),
		DisplayName:   found.Spec.Metadata.Name,
		ApplicationID: &app.ID,
		Spec:          found.Raw,
		Enabled:       &enabled,
		Source:        models.PipelineSourceRepo,
		SourcePath:    found.Path,
		SourceRef:     app.GitRef,
		SourceCommit:  found.Commit,
	})
}

// RepoPipelineForApp routes deploys through CI; only a repo-owned pipeline claims the deploy path, oldest wins.
func (s *Service) RepoPipelineForApp(appID uint) (*models.PipelineDefinition, error) {
	defs, err := s.repo.ListEnabledByApp(appID)
	if err != nil {
		return nil, err
	}
	return pickRepoOwned(defs), nil
}

// Oldest so the deploy path can't change when a second pipeline is bound later.
func pickRepoOwned(defs []models.PipelineDefinition) *models.PipelineDefinition {
	var best *models.PipelineDefinition
	for i := range defs {
		if !defs[i].IsRepoOwned() {
			continue
		}
		if best == nil || defs[i].ID < best.ID {
			best = &defs[i]
		}
	}
	return best
}

// The commit is left unset so the runner resolves the ref's current head.
func (s *Service) TriggerForApp(app *models.Application, trigger string, userID *uint, noCache bool) (*models.PipelineRun, error) {
	def, err := s.RepoPipelineForApp(app.ID)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, ErrNotFound
	}
	return s.Trigger(app.WorkspaceID, def.ID, TriggerInput{
		Trigger: trigger, Branch: app.GitRef, UserID: userID, NoCache: noCache,
	})
}

// DeleteForApp deletes repo-owned pipelines with the app; manual ones are only unbound since a user authored them.
func (s *Service) DeleteForApp(workspaceID, appID uint) error {
	defs, err := s.repo.ListByApp(appID)
	if err != nil {
		return err
	}
	var errs []error
	for i := range defs {
		d := &defs[i]
		if d.IsRepoOwned() {
			if err := s.Delete(workspaceID, d.ID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		d.ApplicationID = nil
		if err := s.repo.Update(d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SyncFromRepo tolerates an unreachable repo (last good spec) but fails on a spec that no longer parses.
func (s *Service) SyncFromRepo(ctx context.Context, p *models.PipelineDefinition, ref string) (bool, error) {
	if !p.IsRepoOwned() || s.gitRepos == nil || p.ApplicationID == nil || s.apps == nil {
		return false, nil
	}
	app, err := s.apps.FindInWorkspace(p.WorkspaceID, *p.ApplicationID)
	if err != nil {
		logger.Warn("pipeline re-sync skipped: bound app not found",
			"pipeline", p.Name, "app", *p.ApplicationID, "error", err)
		return false, nil
	}
	if ref == "" {
		ref = p.SourceRef
	}
	found, err := s.discoverForApp(ctx, app, ref)
	if err != nil {
		logger.Warn("pipeline re-sync could not read the repository; running the last known spec",
			"pipeline", p.Name, "ref", ref, "error", err)
		return false, nil
	}
	if found.SpecError != "" {
		return false, fmt.Errorf("%w: %s", ErrInvalidSpec, found.SpecError)
	}
	if !found.HasPipeline() {
		return false, fmt.Errorf("%w at %s", ErrNoPipelineInRepo, refLabel(ref))
	}
	changed := found.Raw != p.Spec
	p.Spec = found.Raw
	p.SourcePath = found.Path
	p.SourceCommit = found.Commit
	if ref != "" {
		p.SourceRef = ref
	}
	if err := s.repo.Update(p); err != nil {
		return false, err
	}
	s.applySchedule(p) // an edited `on.schedule` takes effect from this run on
	// Only the tracked ref's file sets versioning rules; a feature branch must not rewrite main's policy.
	if spec, err := ParseSpec([]byte(p.Spec)); err == nil && ref == app.GitRef {
		s.syncVersioning(p, spec, nil, false)
	}
	return changed, nil
}

// ref overrides the app's tracked ref (a push webhook supplies the pushed commit).
func (s *Service) discoverForApp(ctx context.Context, app *models.Application, ref string) (*Found, error) {
	url, auth, err := s.gitRepos.CloneURLAuth(app.WorkspaceID, app.GitRepo, app.GitRepositoryID)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = app.GitRef
	}
	ctx, cancel := context.WithTimeout(ctx, adoptTimeout)
	defer cancel()
	return Discover(ctx, url, strings.TrimSpace(ref), auth)
}

// pinCommitTimeout bounds the ls-remote a trigger makes to pin a run; it runs on the request path.
const pinCommitTimeout = 15 * time.Second

// A failed lookup leaves the run unpinned; the runner then clones the default branch.
func (s *Service) pinCommit(p *models.PipelineDefinition, in *TriggerInput) {
	if s.gitRepos == nil {
		return
	}
	var (
		rawURL       string
		credentialID *uint
	)
	switch {
	case p.ApplicationID != nil && s.apps != nil:
		app, err := s.apps.FindInWorkspace(p.WorkspaceID, *p.ApplicationID)
		if err != nil || app.SourceType != models.AppSourceGit {
			return
		}
		if in.Branch == "" {
			in.Branch = app.GitRef
		}
		rawURL, credentialID = app.GitRepo, app.GitRepositoryID
	case p.GitRepositoryID != nil:
		credentialID = p.GitRepositoryID
	default:
		return
	}
	url, auth, err := s.gitRepos.CloneURLAuth(p.WorkspaceID, rawURL, credentialID)
	if err != nil {
		logger.Warn("pipeline run left unpinned: source unresolved", "pipeline", p.Name, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pinCommitTimeout)
	defer cancel()
	commit, err := gitrepo.RemoteCommit(ctx, url, in.Branch, auth)
	if err != nil {
		logger.Warn("pipeline run left unpinned: ref unresolved", "pipeline", p.Name, "ref", in.Branch, "error", err)
		return
	}
	in.Commit = commit
}

// Adoption must not fail on a name collision; a numeric suffix reads better than a random token.
func (s *Service) uniqueName(workspaceID uint, base string) string {
	name := slug.Make(base, "")
	if name == "" {
		name = "pipeline"
	}
	candidate := name
	for i := 2; i < 100; i++ {
		if taken, err := s.repo.ExistsByName(workspaceID, candidate); err != nil || !taken {
			return candidate
		}
		candidate = name + "-" + strconv.Itoa(i)
	}
	return name + "-" + slug.Token(6)
}

func refLabel(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return "the default branch"
	}
	return ref
}
