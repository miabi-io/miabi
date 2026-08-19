// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// adoptTimeout bounds the probe clone made while adopting or re-syncing. It is generous relative to the
// interactive inspect endpoint because these calls sit on a background path (app creation, a run about to
// start), but it still has to end — a hung clone must not wedge a deploy.
const adoptTimeout = 60 * time.Second

// SetGitRepos wires git credential resolution, which repository adoption and per-run re-sync both need to
// clone. Left unset — in tests, or deployments with adoption disabled — AdoptForApp reports
// ErrAdoptionUnavailable and re-sync is skipped in favour of the stored spec.
func (s *Service) SetGitRepos(g *gitrepo.Service) { s.gitRepos = g }

// SetApps wires app lookup, which re-sync needs to resolve a repo-owned
// pipeline's clone source from the app it is bound to.
func (s *Service) SetApps(a *repositories.ApplicationRepository) { s.apps = a }

// AdoptForApp probes an app's repository for a pipeline-as-code document and, when it finds one, creates a
// repo-owned pipeline bound to that app. The definition is named after the app rather than the document's
// metadata.name, because two apps built from forks of the same repo would otherwise collide.
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

// RepoPipelineForApp returns the enabled, repo-owned pipeline bound to an app, or nil when it has none. It
// is what makes a deploy of a pipeline-carrying app route through CI instead of building directly. An app
// may be bound to several pipelines; only a repo-owned one claims the deploy path, and the oldest wins.
func (s *Service) RepoPipelineForApp(appID uint) (*models.PipelineDefinition, error) {
	defs, err := s.repo.ListEnabledByApp(appID)
	if err != nil {
		return nil, err
	}
	return pickRepoOwned(defs), nil
}

// pickRepoOwned returns the oldest repo-owned definition in defs, or nil. Oldest
// rather than newest so the app's deploy path can't change under it when a
// second pipeline is bound later.
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

// TriggerForApp starts a run of an app's repo-owned pipeline on the app's
// tracked ref. The commit is left unset: the runner resolves the ref's current
// head, which is what a user asking to deploy means by "the latest".
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

// DeleteForApp removes every repo-owned pipeline bound to an app. Called when the app is deleted: a
// repo-owned pipeline exists only to serve its app, and left behind it would be an un-runnable definition
// with a dangling binding. Manual pipelines are unbound instead — a user authored those.
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

// SyncFromRepo re-reads a repo-owned pipeline's spec from its repository at ref and stores it, so a run
// always executes the document the commit carries, reporting whether it changed. An unreachable repository
// is not fatal — the last known-good spec is used — but a spec that no longer parses IS.
func (s *Service) SyncFromRepo(ctx context.Context, p *models.PipelineDefinition, ref string) (bool, error) {
	if !p.IsRepoOwned() || s.gitRepos == nil || p.ApplicationID == nil || s.apps == nil {
		return false, nil
	}
	app, err := s.apps.FindByID(*p.ApplicationID)
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
	return changed, nil
}

// discoverForApp resolves an app's clone URL + credential and probes it. ref
// overrides the app's tracked ref (a push webhook supplies the pushed commit).
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

// uniqueName returns base, or the first free base-2, base-3, … suffix. Adoption
// must not fail because the workspace already has a pipeline by that name, and a
// numeric suffix reads better in the UI than a random token.
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
