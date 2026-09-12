// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
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

// UpdateClusterRequest sets a cluster's tenant-facing identity and who may place into it. Omitted fields are
// left unchanged.
type UpdateClusterRequest struct {
	Body struct {
		// DisplayName is the location name tenants see, e.g. "Frankfurt". Empty clears it.
		DisplayName *string `json:"display_name"`
		// LocationCode is the short region code, e.g. "eu-central". Empty clears it.
		LocationCode *string `json:"location_code"`
		// Visibility "restricted" hides the location from tenants; only platform admins place into it.
		Visibility string `json:"visibility" enum:"all,restricted"`
		// Cordoned stops new placements in the location; running workloads stay.
		Cordoned *bool `json:"cordoned"`
	} `json:"body"`
}

// UpdateCluster renames a cluster, sets its location code, visibility or cordon.
func (h *ClusterHandler) UpdateCluster(c *okapi.Context, req *UpdateClusterRequest) error {
	id, err := resolveID(c.Param("clusterID"), h.cluster.ClusterIDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	patch := cluster.ClusterPatch{
		DisplayName:  req.Body.DisplayName,
		LocationCode: req.Body.LocationCode,
		Cordoned:     req.Body.Cordoned,
	}
	if req.Body.Visibility != "" {
		v := models.ClusterVisibility(req.Body.Visibility)
		patch.Visibility = &v
	}
	cl, err := h.cluster.UpdateCluster(id, patch)
	if err != nil {
		return h.mapClusterErr(c, err)
	}
	h.record(c, "cluster.update", cl.ID)
	return ok(c, cl)
}

// SetClusterGatewayRequest picks the node serving a swarm cluster's routes.
type SetClusterGatewayRequest struct {
	Body struct {
		// ServerID is an edge-gateway node of the cluster.
		ServerID uint `json:"server_id" required:"true"`
		// IngressIP and IngressHostname are what DNS records point at, e.g. a load balancer in front of the
		// node. Empty uses the node's public address.
		IngressIP       string `json:"ingress_ip"`
		IngressHostname string `json:"ingress_hostname"`
	} `json:"body"`
}

// SetClusterGateway picks the node that serves a swarm cluster's routes and the address its DNS points at.
func (h *ClusterHandler) SetClusterGateway(c *okapi.Context, req *SetClusterGatewayRequest) error {
	id, err := resolveID(c.Param("clusterID"), h.cluster.ClusterIDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	cl, err := h.cluster.SetGateway(c.Request().Context(), id, cluster.GatewayPatch{
		ServerID: req.Body.ServerID,
		IP:       req.Body.IngressIP,
		Hostname: req.Body.IngressHostname,
	})
	if err != nil {
		return h.mapClusterErr(c, err)
	}
	h.record(c, "cluster.gateway", cl.ID)
	return ok(c, cl)
}

func (h *ClusterHandler) mapClusterErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, cluster.ErrClusterNotFound):
		return c.AbortNotFound("cluster not found")
	case errors.Is(err, cluster.ErrNameTooLong), errors.Is(err, cluster.ErrInvalidLocationCode),
		errors.Is(err, cluster.ErrInvalidVisibility), errors.Is(err, cluster.ErrGatewayNeedsSwarm),
		errors.Is(err, cluster.ErrGatewayNodeNotInCluster), errors.Is(err, cluster.ErrGatewayNodeNoGateway),
		errors.Is(err, cluster.ErrInvalidIngressIP), errors.Is(err, cluster.ErrInvalidIngressHostname):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("cluster operation failed", err)
	}
}
