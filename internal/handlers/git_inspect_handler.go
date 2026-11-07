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
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// The create-app modal blocks on this, so it must fail fast on an unreachable or oversized repository.
const inspectTimeout = 20 * time.Second

type GitInspectHandler struct {
	gitRepos *gitrepo.Service
}

func NewGitInspectHandler(gitRepos *gitrepo.Service) *GitInspectHandler {
	return &GitInspectHandler{gitRepos: gitRepos}
}

type InspectGitRequest struct {
	Body struct {
		// May be omitted when GitRepositoryID names a stored credential carrying its own URL.
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

type InspectGitResponse struct {
	Ref           string `json:"ref"`
	Commit        string `json:"commit"`
	HasDockerfile bool   `json:"has_dockerfile"`
	HasPipeline   bool   `json:"has_pipeline"`
	PipelinePath  string `json:"pipeline_path,omitempty"`
	PipelineName  string `json:"pipeline_name,omitempty"`
	// HasPipeline is false then; the file is reported so the user can fix it instead of getting a plain build.
	PipelineError string           `json:"pipeline_error,omitempty"`
	Steps         []InspectGitStep `json:"steps,omitempty"`
	PushBranches  []string         `json:"push_branches,omitempty"`
	TriggersPush  bool             `json:"triggers_push,omitempty"`
	Manual        bool             `json:"manual,omitempty"`
	Schedule      string           `json:"schedule,omitempty"`
	// Spec is the document verbatim, for the preview pane; only set when it parsed.
	Spec string `json:"spec,omitempty"`
}

// Gated at Developer: it makes the control plane clone a caller-supplied URL.
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
		// A clone failure here is nearly always bad user input: URL, branch, or credential.
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
