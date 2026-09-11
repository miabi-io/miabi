// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/cluster"
	"github.com/miabi-io/miabi/internal/services/node"
)

// ConnectivityApplier deploys or tears down a node's gateway after its connectivity changed.
type ConnectivityApplier func(ctx context.Context, prev, srv *models.Server)

// SetConnectivityApplier wires the node gateway lifecycle used by ConvertIngress.
func (h *ClusterHandler) SetConnectivityApplier(fn ConnectivityApplier) { h.applyConnectivity = fn }

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

// ConvertIngressRequest settles how a standalone cluster's node serves its apps.
type ConvertIngressRequest struct {
	Body struct {
		// Action "gateway" serves them through the node's own gateway; "join" joins the node to a swarm
		// cluster as a worker, whose gateway then serves them.
		Action          string `json:"action" required:"true" enum:"gateway,join"`
		TargetClusterID uint   `json:"target_cluster_id"`
	} `json:"body"`
}

// ConvertIngress gives a standalone cluster's node a gateway to serve its apps: its own, or a swarm's.
func (h *ClusterHandler) ConvertIngress(c *okapi.Context, req *ConvertIngressRequest) error {
	id, err := resolveID(c.Param("clusterID"), h.cluster.ClusterIDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	cl, err := h.cluster.Cluster(id)
	if err != nil {
		return h.mapClusterErr(c, err)
	}
	if cl.IsDefault || cl.Mode != models.ClusterModeStandalone {
		return c.AbortBadRequest("only a standalone cluster's node can be converted")
	}
	prev, err := h.nodes.Get(cl.ManagerServerID)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	ctx := c.Request().Context()
	served, connectivity := cl.ID, models.ConnectivityEdgeGateway
	if req.Body.Action == "join" {
		if req.Body.TargetClusterID == 0 {
			return c.AbortBadRequest("choose the swarm cluster to join")
		}
		if err := h.cluster.JoinNode(ctx, req.Body.TargetClusterID, prev.ID); err != nil {
			return h.mapErr(c, err, "failed to join the node")
		}
		served, connectivity = req.Body.TargetClusterID, models.ConnectivityCluster
	}
	if prev.Connectivity != connectivity {
		srv, err := h.nodes.SetConnectivity(prev.ID, connectivity)
		if err != nil {
			return h.mapClusterErr(c, err)
		}
		if h.applyConnectivity != nil {
			h.applyConnectivity(ctx, prev, srv)
		}
	}
	if req.Body.Action == "gateway" {
		if err := h.cluster.ConfirmGateway(cl.ID); err != nil {
			return h.mapClusterErr(c, err)
		}
	}
	h.cluster.ResyncRoutes(ctx, served)
	h.record(c, "cluster.convert_ingress", cl.ID)
	return message(c, "node converted")
}

func (h *ClusterHandler) mapClusterErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, cluster.ErrClusterNotFound):
		return c.AbortNotFound("cluster not found")
	case errors.Is(err, cluster.ErrNameTooLong), errors.Is(err, cluster.ErrInvalidLocationCode),
		errors.Is(err, cluster.ErrInvalidVisibility), errors.Is(err, cluster.ErrGatewayNeedsSwarm),
		errors.Is(err, cluster.ErrGatewayNodeNotInCluster), errors.Is(err, cluster.ErrGatewayNodeNoGateway),
		errors.Is(err, cluster.ErrInvalidIngressIP), errors.Is(err, cluster.ErrInvalidIngressHostname),
		errors.Is(err, node.ErrInvalidConnectivity):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("cluster operation failed", err)
	}
}
