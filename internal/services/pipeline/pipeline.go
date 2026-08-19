// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
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
	// ErrRepoOwned rejects an in-place edit of a pipeline the repository owns.
	// Only its enabled flag can be changed here.
	ErrRepoOwned = errors.New("this pipeline is managed by its repository — edit the file in git and push, or disable the pipeline to build directly")
	// ErrNoPipelineInRepo reports a repository that carries no pipeline-as-code
	// document. It is an ordinary outcome of adoption, not a failure.
	ErrNoPipelineInRepo = errors.New("repository carries no pipeline-as-code file")
	// ErrNotGitApp rejects adoption for an app that has no git source to read.
	ErrNotGitApp = errors.New("application does not build from a git repository")
	// ErrAdoptionUnavailable reports that repository adoption is not wired up
	// (no git credential service), so nothing can be discovered.
	ErrAdoptionUnavailable = errors.New("repository pipeline adoption is not available")
)

// Enqueuer hands a created run to the background worker. It is an interface so
// the pipeline service does not import the worker package (avoiding a cycle:
// the worker's runner imports this package).
type Enqueuer interface {
	EnqueuePipelineRun(runID, serverID uint) error
}

// Service manages pipeline definitions and triggers runs.
type Service struct {
	repo      *repositories.PipelineRepository
	enqueuer  Enqueuer
	scheduler Scheduler
	// gitRepos and apps back repository-owned pipelines: resolving an app's clone
	// URL + credential so a spec can be discovered at adoption and re-read at each
	// run. Both nil-safe — without them a pipeline is manual-only.
	gitRepos *gitrepo.Service
	apps     *repositories.ApplicationRepository
}

func NewService(repo *repositories.PipelineRepository, enqueuer Enqueuer) *Service {
	return &Service{repo: repo, enqueuer: enqueuer}
}

// Input is the create/update payload for a pipeline definition. Name is the
// desired unique slug handle; DisplayName is the free-text label (falls back to
// Name when blank).
type Input struct {
	Name        string
	DisplayName string
	// ApplicationID is the deploy-target app. On update it is written only when
	// SetApplicationID is true (partial-update aware): a nil pointer then unbinds,
	// a non-nil binds; when SetApplicationID is false the binding is left as-is.
	ApplicationID    *uint
	SetApplicationID bool
	Spec             string
	// Enabled is a pointer so an update can leave it unchanged (nil). On create,
	// nil is treated as disabled (the zero value), matching the prior behavior.
	Enabled *bool
	// Source and the Source* fields mark a definition adopted from a repository's
	// pipeline-as-code file. They are set by adoption, not by the public API — a
	// user-authored pipeline leaves them zero and is treated as manual.
	Source       models.PipelineSource
	SourcePath   string
	SourceRef    string
	SourceCommit string
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
	if _, err := ParseSpec([]byte(in.Spec)); err != nil {
		return nil, errors.Join(ErrInvalidSpec, err)
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
		Spec: in.Spec, Enabled: in.Enabled != nil && *in.Enabled, WebhookSecret: declarative.RandAlphaNum(40),
		Source: source, SourcePath: in.SourcePath, SourceRef: in.SourceRef, SourceCommit: in.SourceCommit,
	}
	if err := s.repo.Create(p); err != nil {
		return nil, err
	}
	s.applySchedule(p)
	return p, nil
}

// Update mutates a pipeline definition.
func (s *Service) Update(workspaceID, id uint, in Input) (*models.PipelineDefinition, error) {
	p, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	// A repo-owned pipeline is a mirror of a file in git: the spec, name and binding are all derived, so an edit
	// here is reverted by the next re-sync and unbinding would orphan the definition. Refuse the edit and say
	// where it belongs. Enabled stays writable on purpose — it is the kill switch when the repo's pipeline breaks.
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
	// Partial update: only touch the app binding / enabled flag when the caller
	// actually supplied them, so a spec-only update can't unbind or disable.
	if in.SetApplicationID {
		p.ApplicationID = in.ApplicationID
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
	return p, nil
}

// repoOwnedEditRequested reports whether an update asks to change something a repository-owned pipeline
// derives rather than owns. A request that only repeats the stored values (a UI form round-tripping
// unchanged fields) is not an edit, so it is allowed through — only an actual change is refused.
func repoOwnedEditRequested(p *models.PipelineDefinition, in Input) bool {
	if in.Spec != "" && in.Spec != p.Spec {
		return true
	}
	// Tri-state: the caller supplied application_id at all, and it differs.
	if in.SetApplicationID && !sameAppID(in.ApplicationID, p.ApplicationID) {
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

// Get loads a pipeline definition without last-run enrichment. Used by internal
// callers (Update/Delete/Trigger existence checks); client-facing reads that
// want the at-a-glance status should use GetWithLastRun.
func (s *Service) Get(workspaceID, id uint) (*models.PipelineDefinition, error) {
	p, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// GetWithLastRun loads a pipeline and attaches its most recent run summary, for
// client-facing reads (the runs page header, etc.).
func (s *Service) GetWithLastRun(workspaceID, id uint) (*models.PipelineDefinition, error) {
	p, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.attachLastRuns(p)
	return p, nil
}

func (s *Service) List(workspaceID uint) ([]models.PipelineDefinition, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// ListPaged returns a page of pipeline definitions plus the total count, each
// enriched with its most recent run.
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
	return defs, total, nil
}

// attachLastRuns fills each definition's LastRun with its newest run, resolved
// in a single batch query. Best-effort: an enrichment failure leaves LastRun nil
// rather than failing the caller.
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
	if _, err := s.Get(workspaceID, id); err != nil {
		return err
	}
	if s.scheduler != nil {
		s.scheduler.Unschedule(id)
	}
	return s.repo.Delete(workspaceID, id)
}

// TriggerInput attributes and contextualizes a run.
type TriggerInput struct {
	Trigger string // push | manual | schedule | upstream
	// Branch is the ref being built. It reaches steps as $MIABI_BRANCH and, for a
	// repo-owned pipeline, selects the ref the spec is re-read from.
	Branch        string
	Commit        string
	CommitMessage string
	UserID        *uint
	APIKeyID      *uint
	NoCache       bool
}

// Trigger creates a PipelineRun (with its step rows) and enqueues it. The run
// executes on the internal runner unless routed to a remote runner by labels.
func (s *Service) Trigger(workspaceID, pipelineID uint, in TriggerInput) (*models.PipelineRun, error) {
	p, err := s.Get(workspaceID, pipelineID)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrDisabled
	}
	// A repo-owned pipeline re-reads its document before every run, so the steps this run executes are the ones
	// the ref actually carries. This has to happen here rather than in the worker: the run's step rows are
	// projected from the spec a few lines down, and refreshing after that would leave a stale step list.
	if p.IsRepoOwned() {
		if _, err := s.SyncFromRepo(context.Background(), p, in.Branch); err != nil {
			return nil, err
		}
		// Pin the run to the commit the spec was just read at, for two reasons: the steps and the tree the runner
		// builds then come from one revision, and the runner checks out by commit — with none it clones the
		// repository's default branch, which for an app tracking any other branch would silently build the wrong code.
		if in.Commit == "" {
			in.Commit = p.SourceCommit
		}
	}
	spec, err := ParseSpec([]byte(p.Spec))
	if err != nil {
		return nil, errors.Join(ErrInvalidSpec, err)
	}
	number, err := s.repo.NextRunNumber(p.ID)
	if err != nil {
		return nil, err
	}
	run := &models.PipelineRun{
		WorkspaceID: workspaceID, PipelineID: p.ID, Number: number,
		Status: models.PipelineRunPending, Trigger: in.Trigger,
		Branch: in.Branch, Commit: in.Commit, CommitMessage: in.CommitMessage,
		TriggeredByUserID: in.UserID, TriggeredByKeyID: in.APIKeyID,
		Env: spec.Env, NoCache: in.NoCache,
	}
	if err := s.repo.CreateRun(run); err != nil {
		return nil, err
	}
	for i, st := range spec.Steps {
		step := &models.PipelineStepRun{
			PipelineRunID: run.ID, Ordinal: i, Name: st.Name,
			Status: models.PipelineRunPending, Image: st.Image, Uses: st.Uses, Run: st.Run,
			Dockerfile: st.Dockerfile, BuildContext: st.Context, BuildArgs: st.BuildArgs,
			NoCache:         st.NoCache() || in.NoCache,
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

func (s *Service) GetRun(workspaceID, id uint) (*models.PipelineRun, error) {
	run, err := s.repo.FindRun(workspaceID, id)
	if err != nil {
		return nil, ErrRunNotFound
	}
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
