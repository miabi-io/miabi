// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package application

import (
	"context"
	"errors"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrSourceTypeInvalid is returned for a source that is neither image nor git.
var ErrSourceTypeInvalid = errors.New("source_type must be \"image\" or \"git\"")

// SourceInput is a complete replacement of an application's source. It is deliberately not a patch:
// switching image <-> git changes which fields are even meaningful, so a partial payload would
// leave the app describing two sources at once.
type SourceInput struct {
	SourceType models.AppSourceType

	// Image source.
	Image      string
	Tag        string
	RegistryID *uint

	// Git source.
	GitRepo         string
	GitRef          string
	GitRepositoryID *uint
	BuildMethod     models.AppBuildMethod
	Builder         string
	Buildpacks      []string
	BuildEnv        map[string]string
}

// SourceChange reports what SetSource did, so the caller can tell the operator what else moved —
// none of it is guessable from the app record afterwards.
type SourceChange struct {
	From models.AppSourceType `json:"from"`
	To   models.AppSourceType `json:"to"`
	// Switched is false when only the details changed (a new tag, a different branch).
	Switched bool `json:"switched"`
	// PipelineRemoved is true when leaving git dropped a repo-owned pipeline.
	PipelineRemoved bool `json:"pipeline_removed"`
	// RedeployRequired is true when the running container no longer matches the source.
	RedeployRequired bool `json:"redeploy_required"`
}

// SetSource replaces where an application's image comes from, including switching between a
// prebuilt image and a Git build. Before this existed the only way to change it was to delete the
// app and recreate it, which threw away its domains, env, volumes and history.
//
// The fields of the source being left are CLEARED rather than kept. An app that still carried a
// git_repo after moving to an image would deploy correctly and read as though it built from that
// repo — and the stale value would come back to life the moment anyone switched it back.
func (s *Service) SetSource(app *models.Application, in SourceInput) (*SourceChange, error) {
	if in.SourceType != models.AppSourceImage && in.SourceType != models.AppSourceGit {
		return nil, ErrSourceTypeInvalid
	}
	from := app.SourceType
	ch := &SourceChange{From: from, To: in.SourceType, Switched: from != in.SourceType}

	if err := applySourceInput(app, in); err != nil {
		return nil, err
	}

	// Update re-runs every source-sensitive validation (git URL present, build config legal for the
	// source, builder allowed by plan) and persists.
	if err := s.Update(app); err != nil {
		return nil, err
	}

	// Leaving git orphans any pipeline adopted from the old repository: it is bound to this app and
	// would keep cloning a repo the app no longer builds from. Dropping it is the only coherent
	// outcome, and it happens after the save so a validation failure cannot destroy it for nothing.
	if ch.Switched && from == models.AppSourceGit && s.pipelines != nil {
		if err := s.pipelines.DeleteForApp(app.WorkspaceID, app.ID); err != nil {
			// Not fatal: the source really did change, and refusing now would leave the app
			// describing a source its pipeline contradicts. Report it and move on.
			logger.Warn("could not remove the repository pipeline after a source change",
				"app", app.Name, "workspace", app.WorkspaceID, "error", err)
		} else {
			ch.PipelineRemoved = true
		}
	}

	// The running container was built from the old source, so it no longer matches the app.
	if changed, err := s.MarkRedeployRequired(app); err == nil {
		ch.RedeployRequired = changed
	}

	s.emit(app, models.EventSettingsUpdated, sourceChangeMessage(ch, app))
	return ch, nil
}

// applySourceInput writes the new source onto the app and clears the fields belonging to the source
// it is leaving. Split out from SetSource because this is the part with teeth: leaving a stale
// git_repo or image behind produces an app that deploys correctly while describing a source it does
// not use, and the stale value silently comes back the moment anyone switches it back.
func applySourceInput(app *models.Application, in SourceInput) error {
	switch in.SourceType {
	case models.AppSourceImage:
		if strings.TrimSpace(in.Image) == "" {
			return ErrImageRequired
		}
		app.Image, app.Tag = normalizeImageTag(in.Image, in.Tag)
		app.RegistryID = in.RegistryID
		// Git fields, including build config, mean nothing for a pulled image.
		app.GitRepo, app.GitRef, app.GitRepositoryID = "", "", nil
		app.BuildMethod, app.Builder, app.Buildpacks, app.BuildEnv = "", "", nil, nil

	case models.AppSourceGit:
		if strings.TrimSpace(in.GitRepo) == "" && in.GitRepositoryID == nil {
			return ErrGitRepoRequired
		}
		app.GitRepo = strings.TrimSpace(in.GitRepo)
		app.GitRef = strings.TrimSpace(in.GitRef)
		app.GitRepositoryID = in.GitRepositoryID
		app.BuildMethod = in.BuildMethod
		app.Builder = in.Builder
		app.Buildpacks = in.Buildpacks
		app.BuildEnv = in.BuildEnv
		// A git app's image is produced by the build and pushed to the internal registry, so the
		// image-source fields would only ever be a stale pull target.
		app.Image, app.Tag, app.RegistryID = "", "", nil

	default:
		return ErrSourceTypeInvalid
	}
	app.SourceType = in.SourceType
	return nil
}

func sourceChangeMessage(ch *SourceChange, app *models.Application) string {
	if !ch.Switched {
		if ch.To == models.AppSourceGit {
			return "Git source updated"
		}
		return "Image source updated"
	}
	if ch.To == models.AppSourceGit {
		return "Source switched from image to git (" + app.GitRepo + ")"
	}
	return "Source switched from git to image (" + app.Image + ")"
}

// ResyncPipeline re-reads the repository's pipeline-as-code and brings the app's pipeline in line
// with it: adopting one when the app has none (the file was added after the app was created, or the
// app only just became a git app), and re-syncing the stored spec when it already has one.
//
// It reports the pipeline it ended up with, and whether the stored spec actually changed.
func (s *Service) ResyncPipeline(ctx context.Context, app *models.Application, userID *uint) (*models.PipelineDefinition, bool, error) {
	if s.pipelines == nil {
		return nil, false, ErrPipelinesUnavailable
	}
	if app.SourceType != models.AppSourceGit {
		return nil, false, ErrNotGitApp
	}
	if !s.repoPipelinesEnabled() {
		return nil, false, ErrRepoPipelinesDisabled
	}

	existing, err := s.pipelines.RepoPipelineForApp(app.ID)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		def, err := s.pipelines.AdoptForApp(ctx, app, userID)
		if err != nil {
			return nil, false, err
		}
		s.emitSeverity(app, models.EventPipelineAdopted, models.SeverityInfo,
			"Adopted the pipeline from "+def.SourcePath+" — deploys run through it")
		return def, true, nil
	}

	changed, err := s.pipelines.SyncFromRepo(ctx, existing, "")
	if err != nil {
		return nil, false, err
	}
	msg := "Pipeline re-synced from " + existing.SourcePath + " — no change"
	if changed {
		msg = "Pipeline updated from " + existing.SourcePath
	}
	s.emitSeverity(app, models.EventPipelineAdopted, models.SeverityInfo, msg)
	return existing, changed, nil
}
