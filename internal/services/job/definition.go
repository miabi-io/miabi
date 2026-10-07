// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package job

import (
	"context"
	"errors"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

var (
	ErrDefinitionNotFound = errors.New("job definition not found")
	ErrDefinitionAppMoved = errors.New("a job definition cannot move to another application; delete it and create it again")
)

// DefinitionInput is a declared Job as the apply engine hands it over.
type DefinitionInput struct {
	Name          string
	ApplicationID uint
	Command       []string
	Entrypoint    []string
	Image         string
	RegistryID    *uint
	RunAsUser     string
	TimeoutSecs   int
	RunPolicy     string
	WaitForDeploy bool
	HistoryLimit  int
	BackoffLimit  int
	// SpecHash is the manifest's fingerprint of the run template.
	SpecHash    string
	Metadata    models.Metadata
	Annotations models.Metadata
}

// SaveDefinition creates or updates the named definition and, when run is set,
// starts a run of it. It returns the definition and the run it started, if any.
// An onRelease definition created while its app has an active release and no
// deploy in flight runs against that release, so the first sync isn't a no-op.
func (s *Service) SaveDefinition(ctx context.Context, workspaceID uint, in DefinitionInput, run bool) (*models.JobDefinition, *models.Job, error) {
	if len(in.Command) == 0 {
		return nil, nil, ErrNoCommand
	}
	app, err := s.apps.FindInWorkspace(workspaceID, in.ApplicationID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	runAsUser, err := s.checkRunAsUser(app, in.RunAsUser)
	if err != nil {
		return nil, nil, err
	}
	def, err := s.repo.FindJobDefinitionByName(workspaceID, in.Name)
	created := false
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		def = &models.JobDefinition{WorkspaceID: workspaceID, ApplicationID: in.ApplicationID, Name: in.Name}
		created = true
	case err != nil:
		return nil, nil, err
	case def.ApplicationID != in.ApplicationID:
		return nil, nil, ErrDefinitionAppMoved
	}
	def.Command = in.Command
	def.Entrypoint = in.Entrypoint
	def.Image = in.Image
	def.RegistryID = in.RegistryID
	def.RunAsUser = runAsUser
	def.TimeoutSecs = in.TimeoutSecs
	def.RunPolicy = runPolicy(in.RunPolicy)
	def.WaitForDeploy = in.WaitForDeploy
	def.HistoryLimit = in.HistoryLimit
	def.BackoffLimit = in.BackoffLimit
	def.SpecHash = in.SpecHash
	def.Metadata = in.Metadata
	def.Annotations = in.Annotations
	if created {
		err = s.repo.CreateJobDefinition(def)
	} else {
		err = s.repo.UpdateJobDefinition(def)
	}
	if err != nil {
		return nil, nil, err
	}
	if def.RunPolicy == models.JobRunOnRelease {
		if created && run {
			j, err := s.runOnActiveRelease(ctx, def)
			return def, j, err
		}
		return def, nil, nil
	}
	if !run {
		return def, nil, nil
	}
	j, err := s.runDefinition(ctx, def, models.JobSourceGitOps, nil)
	return def, j, err
}

// RunFailed retries a declared Job's failed run while its backoffLimit allows.
// Only the definition's latest run retries, so a run superseded by a newer one
// (a spec change, a manual run) is left as it ended.
func (s *Service) RunFailed(jobID uint) {
	j, err := s.repo.FindByID(jobID)
	if err != nil || j.JobDefinitionID == nil || j.Status != models.JobFailed {
		return
	}
	def, err := s.repo.FindJobDefinitionInWorkspace(j.WorkspaceID, *j.JobDefinitionID)
	if err != nil || def.LastJobID == nil || *def.LastJobID != j.ID {
		return
	}
	attempt := max(j.Attempt, 1)
	if attempt > def.BackoffLimit {
		return
	}
	if _, err := s.runAttempt(context.Background(), def, j.Source, j.TriggeredByID, attempt+1); err != nil {
		logger.Error("retry job", "definition", def.Name, "attempt", attempt+1, "error", err)
	}
}

// runOnActiveRelease runs a new onRelease definition against the app's active
// release, unless a deploy is in flight: that deploy's activation runs it instead.
func (s *Service) runOnActiveRelease(ctx context.Context, def *models.JobDefinition) (*models.Job, error) {
	if s.deployments != nil {
		if _, err := s.deployments.InProgressByApp(def.ApplicationID); err == nil {
			return nil, nil
		}
	}
	rel, err := s.releases.FindActive(def.ApplicationID)
	if err != nil {
		return nil, nil
	}
	won, err := s.repo.ClaimRelease(def.ID, rel.ID)
	if err != nil || !won {
		return nil, err
	}
	return s.runDefinition(ctx, def, models.JobSourceRelease, nil)
}

// ReleaseActivated runs every onRelease definition of the app against a release
// that just became active. Each release runs a definition once, even when more
// than one process reports the activation.
func (s *Service) ReleaseActivated(appID, releaseID uint) {
	defs, err := s.repo.ListOnReleaseDefinitions(appID)
	if err != nil {
		logger.Error("list onRelease job definitions", "app", appID, "error", err)
		return
	}
	for i := range defs {
		def := &defs[i]
		won, err := s.repo.ClaimRelease(def.ID, releaseID)
		if err != nil || !won {
			continue
		}
		if _, err := s.runDefinition(context.Background(), def, models.JobSourceRelease, nil); err != nil {
			logger.Error("run onRelease job", "definition", def.Name, "app", appID, "release", releaseID, "error", err)
		}
	}
}

// runDefinition starts a run of a definition. A waitForDeploy definition run
// while its app deploys is held until that deploy ends.
func (s *Service) runDefinition(ctx context.Context, def *models.JobDefinition, source string, triggeredBy *uint) (*models.Job, error) {
	return s.runAttempt(ctx, def, source, triggeredBy, 1)
}

func (s *Service) runAttempt(ctx context.Context, def *models.JobDefinition, source string, triggeredBy *uint, attempt int) (*models.Job, error) {
	req := RunRequest{
		Attempt:         attempt,
		Name:            def.Name,
		Command:         def.Command,
		Entrypoint:      def.Entrypoint,
		Image:           def.Image,
		RegistryID:      def.RegistryID,
		RunAsUser:       def.RunAsUser,
		TimeoutSecs:     def.TimeoutSecs,
		Source:          source,
		TriggeredBy:     triggeredBy,
		JobDefinitionID: &def.ID,
	}
	if def.WaitForDeploy && def.RunPolicy == models.JobRunOnChange && s.deployments != nil {
		if d, err := s.deployments.InProgressByApp(def.ApplicationID); err == nil {
			req.WaitDeploymentID = &d.ID
		}
	}
	j, err := s.Run(ctx, def.WorkspaceID, def.ApplicationID, req)
	if err != nil {
		return nil, err
	}
	def.LastJobID = &j.ID
	_ = s.repo.SetDefinitionLastJob(def.ID, j.ID)
	keep := def.HistoryLimit
	if keep <= 0 {
		keep = defaultHistoryLimit
	}
	_ = s.repo.PruneDefinitionHistory(def.ID, keep)
	return j, nil
}

// RunDefinitionNow runs a definition by hand ("Run again"). Its fingerprint is
// unchanged, so the next sync doesn't run it a second time.
func (s *Service) RunDefinitionNow(ctx context.Context, workspaceID, id uint, triggeredBy *uint) (*models.Job, error) {
	def, err := s.repo.FindJobDefinitionInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrDefinitionNotFound
	}
	return s.runDefinition(ctx, def, models.JobSourceManual, triggeredBy)
}

// GetDefinitionByName loads a definition by its workspace-unique name.
func (s *Service) GetDefinitionByName(workspaceID uint, name string) (*models.JobDefinition, error) {
	def, err := s.repo.FindJobDefinitionByName(workspaceID, name)
	if err != nil {
		return nil, ErrDefinitionNotFound
	}
	return def, nil
}

// ListDefinitions returns the workspace's definitions (optionally one app's),
// with their app name and last run status.
func (s *Service) ListDefinitions(workspaceID, appID uint) ([]models.JobDefinition, error) {
	list, err := s.repo.ListJobDefinitions(workspaceID, appID)
	if err != nil {
		return nil, err
	}
	var last []uint
	for i := range list {
		if list[i].LastJobID != nil {
			last = append(last, *list[i].LastJobID)
		}
	}
	status, _ := s.repo.StatusOf(last)
	names := s.appNames(workspaceID)
	for i := range list {
		list[i].AppName = names[list[i].ApplicationID]
		if list[i].LastJobID != nil {
			list[i].LastStatus = status[*list[i].LastJobID]
		}
	}
	return list, nil
}

// LastRunStatus returns the state of a definition's latest run, or "" when it
// has never run.
func (s *Service) LastRunStatus(def *models.JobDefinition) models.JobStatus {
	if def.LastJobID == nil {
		return ""
	}
	status, _ := s.repo.StatusOf([]uint{*def.LastJobID})
	return status[*def.LastJobID]
}

// DeleteDefinition removes a definition, cancels its active runs, and keeps its
// finished runs as history.
func (s *Service) DeleteDefinition(ctx context.Context, workspaceID, id uint) error {
	def, err := s.repo.FindJobDefinitionInWorkspace(workspaceID, id)
	if err != nil {
		return ErrDefinitionNotFound
	}
	active, _ := s.repo.ActiveByDefinition(def.ID)
	for i := range active {
		_ = s.cancelJob(ctx, &active[i])
	}
	return s.repo.DeleteJobDefinition(def.ID)
}

func runPolicy(p string) string {
	switch p {
	case models.JobRunOnce, models.JobRunOnRelease:
		return p
	default:
		return models.JobRunOnChange
	}
}
