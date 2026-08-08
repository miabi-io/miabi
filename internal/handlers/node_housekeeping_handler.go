// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Node housekeeping: reclaim disk (prune dangling images and build cache), reconcile drift, and
// report what is on the node. Admin-only, and every destructive apply is dry-run-first (Plan)
// and audited.

// HousekeepingSelectionRequest is the body for the plan/apply endpoints: which
// safe reclaim categories to run and which orphan resources to remove.
type HousekeepingSelectionRequest struct {
	Body struct {
		Reclaim struct {
			DanglingImages bool `json:"dangling_images"`
			BuildCache     bool `json:"build_cache"`
		} `json:"reclaim"`
		Orphans []struct {
			Kind string `json:"kind"` // container | volume
			Ref  string `json:"ref"`
		} `json:"orphans"`
	} `json:"body"`
}

func (r *HousekeepingSelectionRequest) selection() housekeeping.Selection {
	sel := housekeeping.Selection{
		Reclaim: housekeeping.ReclaimSelection{
			DanglingImages: r.Body.Reclaim.DanglingImages,
			BuildCache:     r.Body.Reclaim.BuildCache,
		},
	}
	for _, o := range r.Body.Orphans {
		sel.Orphans = append(sel.Orphans, housekeeping.ResourceRef{Kind: o.Kind, Ref: o.Ref})
	}
	return sel
}

// Housekeeping returns the node's housekeeping analysis: disk usage, the safe
// reclaimable breakdown, and the drift table (orphans / missing / untracked).
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

// HousekeepingPlan dry-runs a selection: exactly what would be reclaimed and
// which orphans would be removed, with projected bytes. No mutations.
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

// HousekeepingApply executes a selection (reclaim and/or orphan removal). Each
// destructive action is re-validated against fresh state inside the service and
// audited here. Returns the bytes freed and the orphans removed.
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

// auditHousekeeping writes an audit entry for each destructive housekeeping
// action that actually happened: the reclaim (bytes freed) and each orphan
// removal (node, kind, owner, bytes).
func (h *NodeHandler) auditHousekeeping(c *okapi.Context, nodeID uint, res *housekeeping.Result) {
	actor := middlewares.UserID(c)
	nodeStr := strconv.Itoa(int(nodeID))
	if res.ImagesDeleted > 0 || res.BuildCacheBytes > 0 {
		h.audit.Record(audit.Entry{
			ActorID: &actor, Action: "node.housekeeping.reclaim", TargetType: "node", TargetID: nodeStr, IP: c.RealIP(),
			Metadata: map[string]any{
				"images_deleted":              res.ImagesDeleted,
				"images_reclaimed_bytes":      res.ImagesBytes,
				"build_cache_reclaimed_bytes": res.BuildCacheBytes,
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
