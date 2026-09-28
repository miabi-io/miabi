// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// inspectTimeout bounds the probe clone. The create-app modal blocks on this
// call, so it has to fail fast on an unreachable or oversized repository.
const inspectTimeout = 20 * time.Second

// GitInspectHandler answers "what is in this repository?" for the create-app
// flow: whether it builds from a Dockerfile, and whether it carries a
// pipeline-as-code document Miabi should adopt.
type GitInspectHandler struct {
	gitRepos *gitrepo.Service
}

func NewGitInspectHandler(gitRepos *gitrepo.Service) *GitInspectHandler {
	return &GitInspectHandler{gitRepos: gitRepos}
}

type InspectGitRequest struct {
	Body struct {
		// GitRepo is the repository URL. It may be omitted when GitRepositoryID
		// names a stored credential that carries its own URL.
		GitRepo string `json:"git_repo"`
		// GitRef is the branch, tag or commit to inspect (blank = default branch).
		GitRef string `json:"git_ref"`
		// GitRepositoryID selects a stored credential for a private repository.
		GitRepositoryID *uint `json:"git_repository_id"`
	} `json:"body"`
}

// InspectGitStep is one step of a discovered pipeline, flattened for preview.
type InspectGitStep struct {
	Name            string `json:"name"`
	Uses            string `json:"uses,omitempty"`
	Image           string `json:"image,omitempty"`
	ContinueOnError bool   `json:"continue_on_error,omitempty"`
}

// InspectGitResponse describes what the probe found. HasPipeline is the field the
// create-app modal keys its "use the repository's pipeline" toggle off.
type InspectGitResponse struct {
	Ref           string `json:"ref"`
	Commit        string `json:"commit"`
	HasDockerfile bool   `json:"has_dockerfile"`
	HasPipeline   bool   `json:"has_pipeline"`
	PipelinePath  string `json:"pipeline_path,omitempty"`
	PipelineName  string `json:"pipeline_name,omitempty"`
	// PipelineError is set when a pipeline file was found but failed to parse.
	// HasPipeline is false in that case — the file is reported so the user can fix
	// it rather than silently getting a plain build.
	PipelineError string           `json:"pipeline_error,omitempty"`
	Steps         []InspectGitStep `json:"steps,omitempty"`
	PushBranches  []string         `json:"push_branches,omitempty"`
	TriggersPush  bool             `json:"triggers_push,omitempty"`
	Manual        bool             `json:"manual,omitempty"`
	Schedule      string           `json:"schedule,omitempty"`
	// Spec is the document verbatim, for the preview pane; only set when it parsed.
	Spec string `json:"spec,omitempty"`
}

// Inspect probes a repository. It is a read, but it makes the control plane
// clone a caller-supplied URL, so it is gated at Developer — the same role that
// can already point an app or a GitOps source at an arbitrary repository.
func (h *GitInspectHandler) Inspect(c *okapi.Context, req *InspectGitRequest) error {
	wsID := middlewares.WorkspaceID(c)
	url, auth, err := h.gitRepos.CloneURLAuth(wsID, req.Body.GitRepo, req.Body.GitRepositoryID)
	if err != nil {
		switch {
		case errors.Is(err, gitrepo.ErrNotFound):
			return c.AbortNotFound("git repository credential not found")
		case errors.Is(err, gitrepo.ErrURLRequired):
			return c.AbortBadRequest("a repository URL or a stored git credential is required")
		default:
			return c.AbortInternalServerError("failed to resolve git credentials", err)
		}
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), inspectTimeout)
	defer cancel()

	found, err := pipeline.Discover(ctx, url, strings.TrimSpace(req.Body.GitRef), auth)
	if err != nil {
		// A clone failure here is nearly always the user's input — a bad URL, a
		// missing branch, or a credential that can't read the repo.
		return c.AbortWithError(400, err)
	}

	resp := InspectGitResponse{
		Ref:           found.Ref,
		Commit:        found.Commit,
		HasDockerfile: found.HasDockerfile,
		HasPipeline:   found.HasPipeline(),
		PipelinePath:  found.Path,
		PipelineError: found.SpecError,
	}
	if s := found.Spec; s != nil {
		resp.Spec = found.Raw
		resp.PipelineName = s.Metadata.Name
		resp.Manual = s.On.Manual
		resp.Schedule = s.On.Schedule
		if s.On.Push != nil {
			resp.TriggersPush = true
			resp.PushBranches = s.On.Push.Branches
		}
		resp.Steps = make([]InspectGitStep, 0, len(s.Steps))
		for _, st := range s.Steps {
			resp.Steps = append(resp.Steps, InspectGitStep{
				Name: st.Name, Uses: st.Uses, Image: st.Image, ContinueOnError: st.ContinueOnError,
			})
		}
	}
	return ok(c, resp)
}
