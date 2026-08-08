// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// SetSourceRequest replaces where an application's image comes from. It is a PUT rather than a
// field on the general PATCH because it is a whole-source replacement: switching image <-> git
// changes which fields mean anything, and a partial payload would leave the app claiming both.
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

// SetSourceResponse reports what else moved, because none of it is visible in the app record
// afterwards: whether a repo pipeline was dropped, and whether the running container is now stale.
type SetSourceResponse struct {
	Application *models.Application       `json:"application"`
	Change      *application.SourceChange `json:"change"`
}

// SetSource switches an application between a prebuilt image and a Git build, or edits the details
// of its current source. Previously this needed a delete and recreate, which discarded the app's
// domains, environment, volumes and deployment history.
func (h *ApplicationHandler) SetSource(c *okapi.Context, req *SetSourceRequest) error {
	app, err := h.load(c)
	if err != nil {
		return c.AbortNotFound("application not found")
	}
	if err := h.requireSourceEditable(c, app); err != nil {
		return err
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

// requireSourceEditable refuses an interactive source edit on an application whose source is owned
// elsewhere. A GitOps-managed app would have the change reverted on the next sync, and a
// marketplace app would lose the upgrade path its template provides — in both cases the edit
// silently does not stick, which is worse than being told no.
//
// It guards the HANDLER only. The GitOps engine and the marketplace installer write through
// application.Service directly, and must stay able to manage the resources they own.
func (h *ApplicationHandler) requireSourceEditable(c *okapi.Context, app *models.Application) error {
	owner, owned := models.SourceOwnedElsewhere(app.Metadata)
	if !owned {
		return nil
	}
	if owner == models.ManagedByMarketplace {
		return c.AbortWithError(http.StatusConflict, errors.New(
			"this application's source is managed by its marketplace template — change it with a template upgrade"))
	}
	return c.AbortWithError(http.StatusConflict, errors.New(
		"this application's source is managed by GitOps — change it in the Git manifest and sync"))
}

// ResyncPipelineRequest re-reads the repository's pipeline-as-code for a git application.
type ResyncPipelineRequest struct {
	WorkspaceID string `path:"workspaceID"`
	AppID       string `path:"appID"`
}

// ResyncPipelineResponse reports the pipeline the app ended up bound to. Changed is false when the
// repository's document already matched what was stored — a successful no-op, not a failure.
type ResyncPipelineResponse struct {
	Pipeline *models.PipelineDefinition `json:"pipeline"`
	Changed  bool                       `json:"changed"`
	Adopted  bool                       `json:"adopted"`
}

// resyncTimeout bounds the probe clone. It sits on an interactive request, so it is tighter than
// the background adoption path — but still has to allow for a cold clone of a large repository.
const resyncTimeout = 90 * time.Second

// ResyncPipeline reloads the repository's pipelines.yaml: adopting one when the app has none (the
// file was added after the app was created, or the app only just became a git app), and re-syncing
// the stored spec when it already has one.
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
			// Not an error in the app's configuration: the repository simply carries no pipeline,
			// which is a perfectly normal way to run. Say which paths were looked at.
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
