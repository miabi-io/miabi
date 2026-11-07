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

// SourceInput is a full replacement, not a patch: a partial payload could describe two sources at once.
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

// SetSource clears the old source's fields so a stale git_repo or image can't resurface on a switch back.
func (s *Service) SetSource(app *models.Application, in SourceInput) (*SourceChange, error) {
	if in.SourceType != models.AppSourceImage && in.SourceType != models.AppSourceGit {
		return nil, ErrSourceTypeInvalid
	}
	from := app.SourceType
	ch := &SourceChange{From: from, To: in.SourceType, Switched: from != in.SourceType}

	if err := applySourceInput(app, in); err != nil {
		return nil, err
	}

	if err := s.Update(app); err != nil {
		return nil, err
	}

	// A pipeline from the old repo would keep cloning it; deleted after the save so validation can't lose it for nothing.
	if ch.Switched && from == models.AppSourceGit && s.pipelines != nil {
		if err := s.pipelines.DeleteForApp(app.WorkspaceID, app.ID); err != nil {
			// Not fatal: the source really did change, and refusing now would contradict it.
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

// Stale fields would describe a source the app does not use and silently return on a switch back.
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
		// A git app's image comes from the build, so image fields would only be a stale pull target.
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

// ResyncPipeline adopts a pipeline if the app has none, else re-syncs the stored spec.
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
