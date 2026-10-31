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
	"errors"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/services/versioning"
)

// VersioningHandler exposes version policies: how each pipeline-built image repository numbers its
// builds, the versions handed out so far, and the series a repository releases on.
type VersioningHandler struct {
	svc       *versioning.Service
	pipelines *pipeline.Service
	audit     *audit.Logger
}

func NewVersioningHandler(svc *versioning.Service, pipelines *pipeline.Service, auditLog *audit.Logger) *VersioningHandler {
	return &VersioningHandler{svc: svc, pipelines: pipelines, audit: auditLog}
}

// CreateVersionPolicyRequest attaches a policy to a pipeline's image repository: its application's
// when the pipeline is bound to one, the pipeline's own otherwise.
type CreateVersionPolicyRequest struct {
	Body struct {
		// Pipeline is the pipeline's id, uid or name.
		Pipeline string              `json:"pipeline" required:"true"`
		Settings versioning.Settings `json:"settings"`
	} `json:"body"`
}

type UpdateVersionPolicyRequest struct {
	Body versioning.Settings `json:"body"`
}

type SwitchSeriesRequest struct {
	Body struct {
		// Series is the version line to release on next, e.g. 2.x.x or 1.4.x.
		Series string `json:"series" required:"true"`
	} `json:"body"`
}

// PreviewVersionsRequest previews unsaved settings, on top of an existing policy's history when given.
type PreviewVersionsRequest struct {
	Body struct {
		Policy   string              `json:"policy"`
		Pipeline string              `json:"pipeline"`
		Branch   string              `json:"branch"`
		Count    int                 `json:"count"`
		Settings versioning.Settings `json:"settings"`
	} `json:"body"`
}

// maxPreview caps how many upcoming versions a preview lists.
const maxPreview = 20

func (h *VersioningHandler) List(c *okapi.Context) error {
	out, err := h.svc.List(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list version policies", err)
	}
	return ok(c, out)
}

func (h *VersioningHandler) Get(c *okapi.Context) error {
	p, err := h.svc.Get(middlewares.WorkspaceID(c), c.Param("policyID"))
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, p)
}

// ForPipeline returns the policy covering a pipeline's image repository.
func (h *VersioningHandler) ForPipeline(c *okapi.Context) error {
	def, err := h.pipeline(c, c.Param("pipelineID"))
	if err != nil {
		return c.AbortNotFound("pipeline not found")
	}
	p, err := h.svc.ForPipeline(def)
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, p)
}

func (h *VersioningHandler) Create(c *okapi.Context, req *CreateVersionPolicyRequest) error {
	def, err := h.pipeline(c, req.Body.Pipeline)
	if err != nil {
		return c.AbortNotFound("pipeline not found")
	}
	p, err := h.svc.Create(def, req.Body.Settings)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "version_policy.create", p.ID)
	return created(c, p)
}

func (h *VersioningHandler) Update(c *okapi.Context, req *UpdateVersionPolicyRequest) error {
	p, err := h.svc.Update(middlewares.WorkspaceID(c), c.Param("policyID"), req.Body)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "version_policy.update", p.ID)
	return ok(c, p)
}

func (h *VersioningHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Get(wsID, c.Param("policyID"))
	if err != nil {
		return h.mapErr(c, err)
	}
	if err := h.svc.Delete(wsID, p.UID); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "version_policy.delete", p.ID)
	return message(c, "version policy deleted; builds go back to run-<number> tags")
}

func (h *VersioningHandler) SwitchSeries(c *okapi.Context, req *SwitchSeriesRequest) error {
	p, err := h.svc.SwitchSeries(middlewares.WorkspaceID(c), c.Param("policyID"), req.Body.Series)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "version_policy.switch_series", p.ID)
	return ok(c, p)
}

func (h *VersioningHandler) Allocations(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	out, total, err := h.svc.Allocations(middlewares.WorkspaceID(c), c.Param("policyID"), size, offset)
	if err != nil {
		return h.mapErr(c, err)
	}
	return paginated(c, out, total, page, size)
}

func (h *VersioningHandler) Preview(c *okapi.Context) error {
	plans, err := h.svc.Preview(middlewares.WorkspaceID(c), c.Param("policyID"), c.Query("branch"), previewCount(queryInt(c, "count", 5)))
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, plans)
}

// PreviewSettings shows the versions a settings form would produce before it is saved.
func (h *VersioningHandler) PreviewSettings(c *okapi.Context, req *PreviewVersionsRequest) error {
	wsID := middlewares.WorkspaceID(c)
	var base *models.VersionPolicy
	switch {
	case req.Body.Policy != "":
		p, err := h.svc.Get(wsID, req.Body.Policy)
		if err != nil {
			return h.mapErr(c, err)
		}
		base = p
	case req.Body.Pipeline != "":
		if def, err := h.pipeline(c, req.Body.Pipeline); err == nil {
			base, _ = h.svc.ForPipeline(def)
		}
	}
	plans, err := h.svc.PreviewSettings(base, req.Body.Settings, req.Body.Branch, previewCount(req.Body.Count))
	if err != nil {
		return h.mapErr(c, err)
	}
	return ok(c, plans)
}

func previewCount(n int) int {
	switch {
	case n <= 0:
		return 5
	case n > maxPreview:
		return maxPreview
	}
	return n
}

func (h *VersioningHandler) pipeline(c *okapi.Context, ref string) (*models.PipelineDefinition, error) {
	wsID := middlewares.WorkspaceID(c)
	id, err := h.pipelines.IDByRef(wsID, strings.TrimSpace(ref))
	if err != nil {
		return nil, err
	}
	return h.pipelines.Get(wsID, id)
}

func (h *VersioningHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	wsID := middlewares.WorkspaceID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "version_policy", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *VersioningHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, versioning.ErrNotFound):
		return c.AbortNotFound("version policy not found")
	case errors.Is(err, versioning.ErrPolicyExists), errors.Is(err, versioning.ErrManaged):
		return c.AbortWithError(409, err)
	case isVersioningInputErr(err):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("version policy operation failed", err)
	}
}

// isVersioningInputErr reports a refusal caused by the request, not by the server.
func isVersioningInputErr(err error) bool {
	for _, target := range []error{
		versioning.ErrInvalidVersion, versioning.ErrInvalidSeries, versioning.ErrInvalidTag,
		versioning.ErrInvalidSettings, versioning.ErrBumpOutsideSeries, versioning.ErrOutsideSeries,
		versioning.ErrNotGreater, versioning.ErrVersionTaken, versioning.ErrOverrideNotSupported,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
