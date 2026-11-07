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
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/versioning"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("pipeline not found")
	ErrRunNotFound  = errors.New("pipeline run not found")
	ErrNameTaken    = errors.New("a pipeline with this name already exists")
	ErrNameRequired = errors.New("name is required")
	ErrInvalidSpec  = errors.New("invalid pipeline spec")
	ErrDisabled     = errors.New("pipeline is disabled")
	ErrUnauthorized = errors.New("invalid webhook signature")
	// Only a repo-owned pipeline's enabled flag can be changed here.
	ErrRepoOwned = errors.New("this pipeline is managed by its repository — edit the file in git and push, or disable the pipeline to build directly")
	// An ordinary outcome of adoption, not a failure.
	ErrNoPipelineInRepo = errors.New("repository carries no pipeline-as-code file")
	// ErrNotGitApp rejects adoption for an app that has no git source to read.
	ErrNotGitApp = errors.New("application does not build from a git repository")
	// Both supply the checkout, so together there is no answer to what the run clones.
	ErrSourceConflict = errors.New("bind a pipeline to an application or to a repository, not both")
	ErrDeployNeedsApp = errors.New("a 'uses: deploy' step needs the pipeline bound to an application")
	// It would build an empty workspace and succeed, which is worse than failing.
	ErrBuildNeedsSource = errors.New("a 'uses: build' step needs the pipeline bound to an application or a repository")
	// Names are workspace-scoped: a pipeline binds only to repositories its own workspace registered.
	ErrRepositoryNotFound      = errors.New("no git repository with that name in this workspace")
	ErrRepositoriesUnavailable = errors.New("git repositories are not available")
	ErrApplicationNotFound     = errors.New("no application with that id in this workspace")
	ErrAdoptionUnavailable     = errors.New("repository pipeline adoption is not available")
	// ErrInvalidVersioning wraps a version request or policy the versioning rules refuse.
	ErrInvalidVersioning = errors.New("invalid version request")
)

// An interface avoids an import cycle: the worker's runner imports this package.
type Enqueuer interface {
	EnqueuePipelineRun(runID, serverID uint) error
}

// Service manages pipeline definitions and triggers runs.
type Service struct {
	// restricted refuses automated runs in a read-only or suspended workspace; nil allows them.
	restricted func(workspaceID uint) error
	repo       *repositories.PipelineRepository
	enqueuer   Enqueuer
	scheduler  Scheduler
	// Both nil-safe: without them a pipeline is manual-only.
	gitRepos *gitrepo.Service
	apps     *repositories.ApplicationRepository
	// versions hands each run its image version; nil leaves every build on run-<number> tags.
	versions *versioning.Service
}

// SetVersions wires version policies, so runs that build reserve a version before they are queued.
func (s *Service) SetVersions(v *versioning.Service) { s.versions = v }

// SetRestriction wires the workspace-restriction check applied to pushed and scheduled runs.
func (s *Service) SetRestriction(f func(workspaceID uint) error) { s.restricted = f }

func NewService(repo *repositories.PipelineRepository, enqueuer Enqueuer) *Service {
	return &Service{repo: repo, enqueuer: enqueuer}
}

// Name is the unique slug handle; DisplayName falls back to Name when blank.
type Input struct {
	Name        string
	DisplayName string
	// Written on update only when SetApplicationID is true; then nil unbinds and non-nil binds.
	ApplicationID    *uint
	SetApplicationID bool
	// Binds to a registered repository for build-and-push without a deploy target; partial-update aware like ApplicationID.
	GitRepositoryID    *uint
	SetGitRepositoryID bool
	// Empty clones the repository's default branch.
	Branch    string
	SetBranch bool
	Spec      string
	// A pointer so an update can leave it unchanged; nil on create means disabled.
	Enabled *bool
	// Set by adoption only; a user-authored pipeline leaves them zero and counts as manual.
	Source       models.PipelineSource
	SourcePath   string
	SourceRef    string
	SourceCommit string
	// Ignored when the repository already has a policy; a `versioning:` block in the spec overrides it.
	Versioning *versioning.Settings
}

// Create validates the spec and stores a new pipeline definition.
func (s *Service) Create(workspaceID uint, in Input) (*models.PipelineDefinition, error) {
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	spec, err := ParseSpec([]byte(in.Spec))
	if err != nil {
		return nil, errors.Join(ErrInvalidSpec, err)
	}
	if in.ApplicationID != nil && in.GitRepositoryID != nil {
		return nil, ErrSourceConflict
	}
	// Checked here because ParseSpec sees only YAML; "needs a deploy target" is a fact about the definition.
	if err := validateStepsAgainstBinding(spec, in.ApplicationID, in.GitRepositoryID); err != nil {
		return nil, err
	}
	if err := s.checkBindingsInWorkspace(workspaceID, in.ApplicationID, in.GitRepositoryID); err != nil {
		return nil, err
	}
	if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
		return nil, ErrNameTaken
	}
	source := in.Source
	if source == "" {
		source = models.PipelineSourceManual
	}
	p := &models.PipelineDefinition{
		WorkspaceID: workspaceID, Name: name, DisplayName: displayName, ApplicationID: in.ApplicationID,
		GitRepositoryID: in.GitRepositoryID, Branch: strings.TrimSpace(in.Branch),
		Spec: in.Spec, Enabled: in.Enabled != nil && *in.Enabled, WebhookSecret: declarative.RandAlphaNum(40),
		Source: source, SourcePath: in.SourcePath, SourceRef: in.SourceRef, SourceCommit: in.SourceCommit,
	}
	if in.Versioning != nil {
		if err := versioning.ValidateSettings(in.Versioning); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidVersioning, err)
		}
	}
	if err := s.repo.Create(p); err != nil {
		return nil, err
	}
	s.applySchedule(p)
	s.syncVersioning(p, spec, in.Versioning, true)
	s.decorateRepository(p)
	return p, nil
}

// A `versioning:` block owns the settings; dropping it hands them back to the API.
func (s *Service) syncVersioning(p *models.PipelineDefinition, spec *Spec, settings *versioning.Settings, created bool) {
	if s.versions == nil || spec == nil {
		return
	}
	if spec.Versioning != nil {
		if _, err := s.versions.EnsureForPipeline(p, spec.Versioning, true); err != nil {
			logger.Warn("apply pipeline versioning block failed", "pipeline", p.Name, "error", err)
		}
		return
	}
	s.versions.Unmanage(p)
	if created && spec.HasBuild() {
		if _, err := s.versions.EnsureForPipeline(p, settings, false); err != nil {
			logger.Warn("create version policy failed", "pipeline", p.Name, "error", err)
		}
	}
}

// Update mutates a pipeline definition.
func (s *Service) Update(workspaceID, id uint, in Input) (*models.PipelineDefinition, error) {
	p, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	// A repo-owned pipeline mirrors git, so edits would be reverted; Enabled stays writable as the kill switch.
	if p.IsRepoOwned() && repoOwnedEditRequested(p, in) {
		return nil, ErrRepoOwned
	}
	if in.Spec != "" {
		if _, err := ParseSpec([]byte(in.Spec)); err != nil {
			return nil, errors.Join(ErrInvalidSpec, err)
		}
		p.Spec = in.Spec
	}
	if name := slug.Make(in.Name, ""); name != "" && name != p.Name {
		if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
			return nil, ErrNameTaken
		}
		p.Name = name
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" {
		p.DisplayName = dn
	}
	// Only touch supplied fields, so a spec-only update can't unbind or disable.
	if in.SetApplicationID {
		if err := s.checkBindingsInWorkspace(workspaceID, in.ApplicationID, nil); err != nil {
			return nil, err
		}
		p.ApplicationID = in.ApplicationID
	}
	if in.SetGitRepositoryID {
		if err := s.checkBindingsInWorkspace(workspaceID, nil, in.GitRepositoryID); err != nil {
			return nil, err
		}
		p.GitRepositoryID = in.GitRepositoryID
	}
	if in.SetBranch {
		p.Branch = strings.TrimSpace(in.Branch)
	}
	// Checked against the merged result so a spec-only update adding a deploy step is caught too.
	if p.ApplicationID != nil && p.GitRepositoryID != nil {
		return nil, ErrSourceConflict
	}
	spec, err := ParseSpec([]byte(p.Spec))
	if err != nil {
		return nil, errors.Join(ErrInvalidSpec, err)
	}
	if err := validateStepsAgainstBinding(spec, p.ApplicationID, p.GitRepositoryID); err != nil {
		return nil, err
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	// Backfill a webhook secret for pipelines created before push triggers existed.
	if p.WebhookSecret == "" {
		p.WebhookSecret = declarative.RandAlphaNum(40)
	}
	if err := s.repo.Update(p); err != nil {
		return nil, err
	}
	s.applySchedule(p)
	s.syncVersioning(p, spec, nil, false)
	s.decorateRepository(p)
	return p, nil
}

// Fails closed when unwired: the run would otherwise clone with the owner's credential.
func (s *Service) checkBindingsInWorkspace(workspaceID uint, appID, repoID *uint) error {
	if appID != nil {
		if s.apps == nil {
			return ErrApplicationNotFound
		}
		if _, err := s.apps.FindInWorkspace(workspaceID, *appID); err != nil {
			return ErrApplicationNotFound
		}
	}
	if repoID != nil {
		if s.gitRepos == nil {
			return ErrRepositoriesUnavailable
		}
		if _, err := s.gitRepos.Get(workspaceID, *repoID); err != nil {
			return ErrRepositoryNotFound
		}
	}
	return nil
}

// Repeating stored values (a form round-trip) is not an edit; only an actual change is refused.
func repoOwnedEditRequested(p *models.PipelineDefinition, in Input) bool {
	if in.Spec != "" && in.Spec != p.Spec {
		return true
	}
	// Tri-state: the caller supplied application_id at all, and it differs.
	if in.SetApplicationID && !sameAppID(in.ApplicationID, p.ApplicationID) {
		return true
	}
	if in.SetGitRepositoryID && !sameAppID(in.GitRepositoryID, p.GitRepositoryID) {
		return true
	}
	if in.SetBranch && strings.TrimSpace(in.Branch) != p.Branch {
		return true
	}
	if name := slug.Make(in.Name, ""); name != "" && name != p.Name {
		return true
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" && dn != p.DisplayName {
		return true
	}
	return false
}

func sameAppID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Get skips last-run enrichment; client-facing reads should use GetWithLastRun.
func (s *Service) Get(workspaceID, id uint) (*models.PipelineDefinition, error) {
	p, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	s.decorateRepository(p)
	return p, nil
}

func (s *Service) GetWithLastRun(workspaceID, id uint) (*models.PipelineDefinition, error) {
	p, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.attachLastRuns(p)
	return p, nil
}

func (s *Service) List(workspaceID uint) ([]models.PipelineDefinition, error) {
	defs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	s.decorateRepositories(workspaceID, defs)
	return defs, nil
}

func (s *Service) ListPaged(workspaceID uint, limit, offset int) ([]models.PipelineDefinition, int64, error) {
	defs, total, err := s.repo.ListByWorkspacePaged(workspaceID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	ptrs := make([]*models.PipelineDefinition, len(defs))
	for i := range defs {
		ptrs[i] = &defs[i]
	}
	s.attachLastRuns(ptrs...)
	s.decorateRepositories(workspaceID, defs)
	return defs, total, nil
}

// One batch query; an enrichment failure leaves LastRun nil rather than failing the caller.
func (s *Service) attachLastRuns(defs ...*models.PipelineDefinition) {
	if len(defs) == 0 {
		return
	}
	ids := make([]uint, len(defs))
	for i, p := range defs {
		ids[i] = p.ID
	}
	latest, err := s.repo.LatestRunByPipeline(ids)
	if err != nil {
		return
	}
	for _, p := range defs {
		if run, ok := latest[p.ID]; ok {
			p.LastRun = run.Summary()
		}
	}
}

func (s *Service) Delete(workspaceID, id uint) error {
	p, err := s.Get(workspaceID, id)
	if err != nil {
		return err
	}
	if s.scheduler != nil {
		s.scheduler.Unschedule(id)
	}
	if err := s.repo.Delete(workspaceID, id); err != nil {
		return err
	}
	if s.versions != nil {
		s.versions.PipelineDeleted(p)
	}
	return nil
}

// TriggerInput attributes and contextualizes a run.
type TriggerInput struct {
	Trigger string // push | manual | schedule | upstream
	// Reaches steps as $MIABI_BRANCH and selects the ref a repo-owned spec is re-read from.
	Branch        string
	Commit        string
	CommitMessage string
	UserID        *uint
	APIKeyID      *uint
	NoCache       bool
	// Manual and API triggers only; pushes and schedules follow the policy.
	Bump    models.VersionBump
	Version string
}

// Trigger runs on the internal runner unless labels route it to a remote runner.
func (s *Service) Trigger(workspaceID, pipelineID uint, in TriggerInput) (*models.PipelineRun, error) {
	p, err := s.Get(workspaceID, pipelineID)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrDisabled
	}
	// Must sync before step rows are projected from the spec below, or the step list would be stale.
	if p.IsRepoOwned() {
		if _, err := s.SyncFromRepo(context.Background(), p, in.Branch); err != nil {
			return nil, err
		}
		// Pin to the spec's commit: one revision for steps and tree, and no silent default-branch build.
		if in.Commit == "" {
			in.Commit = p.SourceCommit
		}
	}
	// A repository-bound pipeline has no app ref, so the branch comes from the definition.
	if in.Branch == "" && p.GitRepositoryID != nil {
		in.Branch = p.Branch
	}
	if in.Commit == "" {
		s.pinCommit(p, &in)
	}
	spec, err := ParseSpec([]byte(p.Spec))
	if err != nil {
		return nil, errors.Join(ErrInvalidSpec, err)
	}
	run := &models.PipelineRun{
		WorkspaceID: workspaceID, PipelineID: p.ID,
		Status: models.PipelineRunPending, Trigger: in.Trigger,
		Branch: in.Branch, Commit: in.Commit, CommitMessage: in.CommitMessage,
		TriggeredByUserID: in.UserID, TriggeredByKeyID: in.APIKeyID,
		Env: spec.Env, NoCache: in.NoCache,
	}
	if err := s.repo.CreateNumberedRun(run); err != nil {
		return nil, err
	}
	if err := s.reserveVersion(run, p, spec, in); err != nil {
		_ = s.repo.DeleteRun(run.ID)
		return nil, err
	}
	for i, st := range spec.Steps {
		step := &models.PipelineStepRun{
			PipelineRunID: run.ID, Ordinal: i, Name: st.Name,
			Status: models.PipelineRunPending, Image: st.Image, Uses: st.Uses, Run: st.Run,
			Dockerfile: st.Dockerfile, BuildContext: st.Context, BuildArgs: st.BuildArgs,
			NoCache:         st.NoCache() || in.NoCache,
			Platforms:       st.Platforms,
			Env:             st.Env,
			ContinueOnError: st.ContinueOnError,
		}
		if err := s.repo.CreateStep(step); err != nil {
			return nil, err
		}
	}
	if s.enqueuer != nil {
		if err := s.enqueuer.EnqueuePipelineRun(run.ID, 0); err != nil {
			return nil, err
		}
	}
	return run, nil
}

// Reserved before queueing so the version is fixed from the start and concurrent runs never share one.
func (s *Service) reserveVersion(run *models.PipelineRun, p *models.PipelineDefinition, spec *Spec, in TriggerInput) error {
	if s.versions == nil {
		return nil
	}
	// A build-less pipeline is versioned only by its own block, so a policy left from an earlier build step stays idle.
	if !spec.HasBuild() && spec.Versioning == nil {
		if in.Bump != "" || in.Version != "" {
			return fmt.Errorf("%w: this pipeline has no 'versioning:' block and builds no image", ErrInvalidVersioning)
		}
		return nil
	}
	err := s.versions.Reserve(run, p, versioning.Request{Bump: in.Bump, Exact: strings.TrimSpace(in.Version)})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidVersioning, err)
	}
	if run.ImageTag == "" {
		return nil
	}
	return s.repo.UpdateRun(run)
}

// Rerun deliberately reuses the earlier commit: re-run means this again, not whatever landed since.
func (s *Service) Rerun(workspaceID, runID uint, userID *uint, noCache bool) (*models.PipelineRun, error) {
	prev, err := s.GetRun(workspaceID, runID)
	if err != nil {
		return nil, err
	}
	return s.Trigger(workspaceID, prev.PipelineID, TriggerInput{
		Trigger: "rerun", Branch: prev.Branch, Commit: prev.Commit, CommitMessage: prev.CommitMessage,
		UserID: userID, NoCache: noCache,
	})
}

func (s *Service) GetRun(workspaceID, id uint) (*models.PipelineRun, error) {
	run, err := s.repo.FindRun(workspaceID, id)
	if err != nil {
		return nil, ErrRunNotFound
	}
	run.Deployment, _ = s.repo.RunDeployment(workspaceID, run.ID)
	return run, nil
}

func (s *Service) ListRuns(workspaceID, pipelineID uint, limit int) ([]models.PipelineRun, error) {
	return s.repo.ListRuns(workspaceID, pipelineID, limit)
}

// ListRunsPaged returns a page of a pipeline's runs plus the total count.
func (s *Service) ListRunsPaged(workspaceID, pipelineID uint, limit, offset int) ([]models.PipelineRun, int64, error) {
	return s.repo.ListRunsPaged(workspaceID, pipelineID, limit, offset)
}

// IDByUID resolves a pipeline's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }

// IDByRef resolves a pipeline in a workspace from its id, uid or name.
func (s *Service) IDByRef(workspaceID uint, ref string) (uint, error) {
	return s.repo.IDByRef(workspaceID, ref)
}

func validateStepsAgainstBinding(spec *Spec, appID, repoID *uint) error {
	if spec == nil {
		return nil
	}
	for _, st := range spec.Steps {
		switch st.Uses {
		case UsesDeploy:
			if appID == nil {
				return fmt.Errorf("step %q: %w", st.Name, ErrDeployNeedsApp)
			}
		case UsesBuild:
			if appID == nil && repoID == nil {
				return fmt.Errorf("step %q: %w", st.Name, ErrBuildNeedsSource)
			}
		}
	}
	return nil
}

func (s *Service) RepositoryIDByName(workspaceID uint, name string) (uint, error) {

	if s == nil || s.gitRepos == nil {
		return 0, ErrRepositoriesUnavailable
	}
	g, err := s.gitRepos.GetByName(workspaceID, name)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrRepositoryNotFound, name)
	}
	return g.ID, nil
}

// decorateRepository fills the bound repository's and application's names.
func (s *Service) decorateRepository(p *models.PipelineDefinition) {
	if p == nil {
		return
	}
	if p.ApplicationID != nil && s.apps != nil {
		if names, err := s.apps.NamesByID(p.WorkspaceID, []uint{*p.ApplicationID}); err == nil {
			p.ApplicationName = names[*p.ApplicationID]
		}
	}
	if p.GitRepositoryID == nil || s.gitRepos == nil {
		return
	}
	if g, err := s.gitRepos.Get(p.WorkspaceID, *p.GitRepositoryID); err == nil {
		p.GitRepository = g.Name
	}
}

func (s *Service) decorateRepositories(workspaceID uint, defs []models.PipelineDefinition) {
	s.decorateApplications(workspaceID, defs)
	if s.gitRepos == nil {
		return
	}
	var bound bool
	for i := range defs {
		if defs[i].GitRepositoryID != nil {
			bound = true
			break
		}
	}
	if !bound {
		return
	}
	repos, err := s.gitRepos.List(workspaceID)
	if err != nil {
		return
	}
	byID := make(map[uint]string, len(repos))
	for i := range repos {
		byID[repos[i].ID] = repos[i].Name
	}
	for i := range defs {
		if defs[i].GitRepositoryID != nil {
			defs[i].GitRepository = byID[*defs[i].GitRepositoryID]
		}
	}
}

func (s *Service) decorateApplications(workspaceID uint, defs []models.PipelineDefinition) {
	if s.apps == nil {
		return
	}
	var ids []uint
	for i := range defs {
		if defs[i].ApplicationID != nil {
			ids = append(ids, *defs[i].ApplicationID)
		}
	}
	names, err := s.apps.NamesByID(workspaceID, ids)
	if err != nil {
		return
	}
	for i := range defs {
		if defs[i].ApplicationID != nil {
			defs[i].ApplicationName = names[*defs[i].ApplicationID]
		}
	}
}
