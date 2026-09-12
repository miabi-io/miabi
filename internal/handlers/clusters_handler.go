// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/services/cluster"
)

// ListClusters returns every cluster, the default first, with its node count.
func (h *ClusterHandler) ListClusters(c *okapi.Context) error {
	list, err := h.cluster.Clusters()
	if err != nil {
		return c.AbortInternalServerError("failed to list clusters", err)
	}
	return ok(c, list)
}

// GetCluster returns one cluster with its node count.
func (h *ClusterHandler) GetCluster(c *okapi.Context) error {
	id, err := resolveID(c.Param("clusterID"), h.cluster.ClusterIDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	cl, err := h.cluster.Cluster(id)
	if err != nil {
		return h.mapClusterErr(c, err)
	}
	return ok(c, cl)
}

// UpdateClusterRequest sets a cluster's tenant-facing identity.
type UpdateClusterRequest struct {
	Body struct {
		// DisplayName is the location name tenants see, e.g. "Frankfurt". Empty clears it.
		DisplayName string `json:"display_name" maxLength:"40"`
		// LocationCode is the short region code, e.g. "eu-central". Empty clears it.
		LocationCode string `json:"location_code" maxLength:"32"`
	} `json:"body"`
}

// UpdateCluster renames a cluster and sets its location code.
func (h *ClusterHandler) UpdateCluster(c *okapi.Context, req *UpdateClusterRequest) error {
	id, err := resolveID(c.Param("clusterID"), h.cluster.ClusterIDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	cl, err := h.cluster.UpdateCluster(id, cluster.ClusterPatch{
		DisplayName:  &req.Body.DisplayName,
		LocationCode: &req.Body.LocationCode,
	})
	if err != nil {
		return h.mapClusterErr(c, err)
	}
	h.record(c, "cluster.update", cl.ID)
	return ok(c, cl)
}

func (h *ClusterHandler) mapClusterErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, cluster.ErrClusterNotFound):
		return c.AbortNotFound("cluster not found")
	case errors.Is(err, cluster.ErrNameTooLong), errors.Is(err, cluster.ErrInvalidLocationCode):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("cluster operation failed", err)
	}
}
