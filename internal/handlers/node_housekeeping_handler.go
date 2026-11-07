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
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/housekeeping"
)

// Every destructive housekeeping apply is dry-run-first (Plan) and audited.

type HousekeepingSelectionRequest struct {
	Body struct {
		Reclaim struct {
			DanglingImages bool `json:"dangling_images"`
			BuildCache     bool `json:"build_cache"`
			UnusedImages   bool `json:"unused_images"`
			UnusedVolumes  bool `json:"unused_volumes"`
		} `json:"reclaim"`
		Orphans []struct {
			Kind string `json:"kind"` // container | volume
			Ref  string `json:"ref"`
		} `json:"orphans"`
		// Missing are the workloads to redeploy: container | service, ref "app:<id>".
		Missing []struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"missing"`
	} `json:"body"`
}

func (r *HousekeepingSelectionRequest) selection() housekeeping.Selection {
	sel := housekeeping.Selection{
		Reclaim: housekeeping.ReclaimSelection{
			DanglingImages: r.Body.Reclaim.DanglingImages,
			BuildCache:     r.Body.Reclaim.BuildCache,
			UnusedImages:   r.Body.Reclaim.UnusedImages,
			UnusedVolumes:  r.Body.Reclaim.UnusedVolumes,
		},
	}
	for _, o := range r.Body.Orphans {
		sel.Orphans = append(sel.Orphans, housekeeping.ResourceRef{Kind: o.Kind, Ref: o.Ref})
	}
	for _, m := range r.Body.Missing {
		sel.Missing = append(sel.Missing, housekeeping.ResourceRef{Kind: m.Kind, Ref: m.Ref})
	}
	return sel
}

// Pure read.
func (h *NodeHandler) Housekeeping(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	rep, err := h.housekeeper.Analyze(c.Request().Context(), id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	return ok(c, rep)
}

// No mutations.
func (h *NodeHandler) HousekeepingPlan(c *okapi.Context, req *HousekeepingSelectionRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	plan, err := h.housekeeper.Plan(c.Request().Context(), id, req.selection())
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	return ok(c, plan)
}

// Each destructive action is re-validated against fresh state inside the service.
func (h *NodeHandler) HousekeepingApply(c *okapi.Context, req *HousekeepingSelectionRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	res, err := h.housekeeper.Apply(c.Request().Context(), id, req.selection())
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	h.auditHousekeeping(c, id, res)
	return ok(c, res)
}

func (h *NodeHandler) auditHousekeeping(c *okapi.Context, nodeID uint, res *housekeeping.Result) {
	actor := middlewares.UserID(c)
	nodeStr := strconv.Itoa(int(nodeID))
	if res.ImagesDeleted > 0 || res.BuildCacheBytes > 0 || res.VolumesDeleted > 0 {
		h.audit.Record(audit.Entry{
			ActorID: &actor, Action: "node.housekeeping.reclaim", TargetType: "node", TargetID: nodeStr, IP: c.RealIP(),
			Metadata: map[string]any{
				"images_deleted":              res.ImagesDeleted,
				"images_reclaimed_bytes":      res.ImagesBytes,
				"build_cache_reclaimed_bytes": res.BuildCacheBytes,
				"volumes_deleted":             res.VolumesDeleted,
				"volumes_reclaimed_bytes":     res.VolumesBytes,
			},
		})
	}
	for _, m := range res.Redeployed {
		h.audit.Record(audit.Entry{
			ActorID: &actor, Action: "node.housekeeping.redeploy", TargetType: "node", TargetID: nodeStr, IP: c.RealIP(),
			Metadata: map[string]any{
				"kind": m.Kind, "ref": m.Ref, "name": m.Name,
				"owner_kind": m.OwnerKind, "owner_id": m.OwnerID,
			},
		})
	}
	for _, o := range res.OrphansRemoved {
		h.audit.Record(audit.Entry{
			ActorID: &actor, Action: "node.housekeeping.orphan_remove", TargetType: "node", TargetID: nodeStr, IP: c.RealIP(),
			Metadata: map[string]any{
				"kind": o.Kind, "ref": o.Ref, "name": o.Name,
				"owner_kind": o.OwnerKind, "owner_id": o.OwnerID,
			},
		})
	}
}
