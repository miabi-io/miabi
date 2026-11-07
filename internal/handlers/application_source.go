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

package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// A PUT, not a PATCH field: switching image <-> git changes which fields mean anything.
type SetSourceRequest struct {
	WorkspaceID string `path:"workspaceID"`
	AppID       string `path:"appID"`
	Body        struct {
		// SourceType is required: "image" pulls a prebuilt image, "git" builds from a repository.
		SourceType string `json:"source_type" enum:"image,git" required:"true"`

		// Image source. Image is required when source_type is "image".
		Image      string `json:"image"`
		Tag        string `json:"tag"`
		RegistryID *uint  `json:"registry_id"`

		// Git source. One of git_repo or git_repository_id is required when source_type is "git".
		GitRepo         string            `json:"git_repo"`
		GitRef          string            `json:"git_ref"`
		GitRepositoryID *uint             `json:"git_repository_id"`
		BuildMethod     string            `json:"build_method" enum:"auto,dockerfile,buildpack"`
		Builder         string            `json:"builder"`
		Buildpacks      []string          `json:"buildpacks"`
		BuildEnv        map[string]string `json:"build_env"`
	}
}

// Reported because none of it is visible in the app record afterwards.
type SetSourceResponse struct {
	Application *models.Application       `json:"application"`
	Change      *application.SourceChange `json:"change"`
}

func (h *ApplicationHandler) SetSource(c *okapi.Context, req *SetSourceRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := sourceLock(app); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	ch, err := h.svc.SetSource(app, application.SourceInput{
		SourceType:      models.AppSourceType(req.Body.SourceType),
		Image:           req.Body.Image,
		Tag:             req.Body.Tag,
		RegistryID:      req.Body.RegistryID,
		GitRepo:         req.Body.GitRepo,
		GitRef:          req.Body.GitRef,
		GitRepositoryID: req.Body.GitRepositoryID,
		BuildMethod:     models.AppBuildMethod(req.Body.BuildMethod),
		Builder:         req.Body.Builder,
		Buildpacks:      req.Body.Buildpacks,
		BuildEnv:        req.Body.BuildEnv,
	})
	if err != nil {
		if errors.Is(err, application.ErrSourceTypeInvalid) {
			return c.AbortBadRequest(err.Error())
		}
		return h.mapErr(c, err)
	}
	return c.OK(okapi.M{"success": true, "data": SetSourceResponse{Application: app, Change: ch}})
}

// Returns the refusal rather than aborting: an aborted response returns nil and the caller would continue.
func sourceLock(app *models.Application) error {
	owner, owned := models.SourceOwnedElsewhere(app.Metadata)
	if !owned {
		return nil
	}
	if owner == models.ManagedByMarketplace {
		return errors.New("this application's source is managed by its marketplace template — change it with a template upgrade")
	}
	return errors.New("this application's source is managed by GitOps — change it in the Git manifest and sync")
}

// ResyncPipelineRequest re-reads the repository's pipeline-as-code for a git application.
type ResyncPipelineRequest struct {
	WorkspaceID string `path:"workspaceID"`
	AppID       string `path:"appID"`
}

// Changed is false when the repository already matched what was stored: a no-op, not a failure.
type ResyncPipelineResponse struct {
	Pipeline *models.PipelineDefinition `json:"pipeline"`
	Changed  bool                       `json:"changed"`
	Adopted  bool                       `json:"adopted"`
}

// Tighter than background adoption, but must allow a cold clone of a large repository.
const resyncTimeout = 90 * time.Second

func (h *ApplicationHandler) ResyncPipeline(c *okapi.Context, req *ResyncPipelineRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	had, err := h.svc.RepoPipelineForApp(app.ID)
	if err != nil {
		return h.mapErr(c, err)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), resyncTimeout)
	defer cancel()

	def, changed, err := h.svc.ResyncPipeline(ctx, app, userIDPtr(c))
	if err != nil {
		switch {
		case errors.Is(err, application.ErrNotGitApp), errors.Is(err, pipeline.ErrNotGitApp):
			return c.AbortBadRequest("this application does not build from a git repository")
		case errors.Is(err, pipeline.ErrNoPipelineInRepo):
			// A repository with no pipeline is a normal way to run, not a configuration error.
			return c.AbortWithError(http.StatusNotFound, err)
		case errors.Is(err, pipeline.ErrInvalidSpec):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, application.ErrRepoPipelinesDisabled),
			errors.Is(err, application.ErrPipelinesUnavailable),
			errors.Is(err, pipeline.ErrAdoptionUnavailable):
			return c.AbortWithError(http.StatusServiceUnavailable, err)
		}
		return h.mapErr(c, err)
	}
	return c.OK(okapi.M{"success": true, "data": ResyncPipelineResponse{
		Pipeline: def, Changed: changed, Adopted: had == nil,
	}})
}
