// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/cluster"
	"github.com/miabi-io/miabi/internal/services/node"
)

// ClusterHandler exposes platform-admin cluster management: the cluster inventory and each cluster's
// Docker Swarm. Swarm routes under /admin/clusters/{clusterID} target that cluster; the legacy
// /admin/cluster routes target the default one. On plain Docker they report "not enabled".
type ClusterHandler struct {
	cluster           *cluster.Service
	nodes             *node.Service
	audit             *audit.Logger
	applyConnectivity ConnectivityApplier
}

func NewClusterHandler(c *cluster.Service, n *node.Service, auditLog *audit.Logger) *ClusterHandler {
	return &ClusterHandler{cluster: c, nodes: n, audit: auditLog}
}

func (h *ClusterHandler) clusterID(c *okapi.Context) (uint, error) {
	ref := c.Param("clusterID")
	if ref == "" {
		return models.DefaultClusterID, nil
	}
	return resolveID(ref, h.cluster.ClusterIDByUID)
}

// Status returns a cluster's swarm status.
func (h *ClusterHandler) Status(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	return ok(c, h.clusterStatus(c, id))
}

// clusterStatus adds what needs a live manager call to the cached status: whether the global agent
// service is deployed, which decides whether swarm members are managed.
func (h *ClusterHandler) clusterStatus(c *okapi.Context, id uint) cluster.Status {
	st := h.cluster.Status(id)
	if st.Enabled {
		a := h.cluster.AgentStatus(c.Request().Context(), id)
		st.AgentsDeployed, st.AgentTasks = a.Deployed, a.Running
		st.AgentInsecureTLS, st.AgentCustomCA = a.InsecureTLS, a.CustomCA
		st.AgentCACertPath = a.CACertPath
	}
	return st
}

// EnableClusterRequest enables (or adopts) Swarm on a cluster.
type EnableClusterRequest struct {
	Body struct {
		// AdvertiseAddr is the address swarm peers reach the manager on (its private address, host or
		// host:port). Required when initializing a new swarm; ignored when adopting one.
		AdvertiseAddr string `json:"advertise_addr"`
		// Name labels the default cluster. Optional; ignored for other clusters, which are renamed with PATCH.
		Name string `json:"name"`
	} `json:"body"`
}

// Enable initializes (or adopts) a swarm on the cluster's manager node.
func (h *ClusterHandler) Enable(c *okapi.Context, req *EnableClusterRequest) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	if name := strings.TrimSpace(req.Body.Name); name != "" && id == models.DefaultClusterID {
		if err := h.cluster.SetName(name); err != nil {
			return h.mapErr(c, err, "failed to name the cluster")
		}
	}
	status, err := h.cluster.Enable(c.Request().Context(), id, req.Body.AdvertiseAddr)
	if err != nil {
		return h.mapErr(c, err, "failed to enable cluster mode")
	}
	h.record(c, "cluster.enable", id)
	return ok(c, status)
}

// Disable takes the cluster out of swarm mode; its member nodes become standalone clusters.
func (h *ClusterHandler) Disable(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	if err := h.cluster.Disable(c.Request().Context(), id); err != nil {
		return h.mapErr(c, err, "failed to disable cluster mode")
	}
	h.record(c, "cluster.disable", id)
	return message(c, "cluster mode disabled")
}

// ApplyNetworking converts the default cluster's workspace networks still on node-local bridges into
// swarm overlays, for an install already clustered when it upgraded.
func (h *ClusterHandler) ApplyNetworking(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	if err := h.cluster.ApplyNetworking(c.Request().Context(), id); err != nil {
		return h.mapErr(c, err, "failed to apply cluster networking")
	}
	h.record(c, "cluster.network.apply", id)
	return ok(c, h.clusterStatus(c, id))
}

// RenameClusterRequest relabels the default cluster.
type RenameClusterRequest struct {
	Body struct {
		// Name is the operator's label. Empty clears it.
		Name string `json:"name"`
	} `json:"body"`
}

// Rename labels the default cluster.
func (h *ClusterHandler) Rename(c *okapi.Context, req *RenameClusterRequest) error {
	if err := h.cluster.SetName(req.Body.Name); err != nil {
		return h.mapErr(c, err, "failed to rename the cluster")
	}
	h.record(c, "cluster.rename", 0)
	return ok(c, h.clusterStatus(c, models.DefaultClusterID))
}

// Preflight reports what the cluster's manager engine can and cannot do before Swarm is turned on, and
// the ports that must be open between nodes. Read-only.
func (h *ClusterHandler) Preflight(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	p, err := h.cluster.Preflight(c.Request().Context(), id)
	if err != nil {
		return h.mapErr(c, err, "failed to inspect the Docker engine")
	}
	return ok(c, p)
}

// NetCheck probes the cluster's overlay data plane between every pair of its nodes, separating a name
// that will not resolve, a connection that never completes, and a payload that dies at the MTU.
func (h *ClusterHandler) NetCheck(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	res, err := h.cluster.NetCheck(c.Request().Context(), id)
	if err != nil {
		return h.mapErr(c, err, "failed to run the network check")
	}
	h.record(c, "cluster.netcheck", id)
	return ok(c, res)
}

// SetAvailabilityRequest changes a swarm node's scheduling availability.
type SetAvailabilityRequest struct {
	Body struct {
		// Availability is active | pause | drain. Drain reschedules the node's tasks
		// away, which is what makes it safe to reboot.
		Availability string `json:"availability"`
	} `json:"body"`
}

// SetAvailability changes a swarm node's scheduling availability. Keyed by swarm node id, so an
// unmanaged member (no Miabi agent) can be drained too.
func (h *ClusterHandler) SetAvailability(c *okapi.Context, req *SetAvailabilityRequest) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	swarmNodeID := c.Param("swarmNodeID")
	if swarmNodeID == "" {
		return c.AbortBadRequest("swarm node id is required")
	}
	if err := h.cluster.SetAvailability(c.Request().Context(), id, swarmNodeID, req.Body.Availability); err != nil {
		return h.mapErr(c, err, "failed to change node availability")
	}
	h.record(c, "cluster.node.availability", id)
	return message(c, "node availability set to "+req.Body.Availability)
}

// NodeTasks lists the service tasks the scheduler placed on a swarm node, the only way to see an
// unmanaged member's workload.
func (h *ClusterHandler) NodeTasks(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	tasks, err := h.cluster.Tasks(c.Request().Context(), id, c.Param("swarmNodeID"))
	if err != nil {
		return h.mapErr(c, err, "failed to list the node's tasks")
	}
	return ok(c, tasks)
}

// DeployAgentsRequest configures the global agent service.
type DeployAgentsRequest struct {
	Body struct {
		// InsecureSkipVerify makes the agents skip verification of the control plane's TLS certificate. A real
		// downgrade: an interceptor could impersonate a control plane that drives Docker on every node.
		InsecureSkipVerify bool `json:"insecure_skip_verify"`
		// CACert trusts a specific authority instead: the agents still verify, anchored on this CA.
		CACert string `json:"ca_cert"`
		// CACertPath is a CA file that already exists on every node, bind-mounted into each agent.
		CACertPath string `json:"ca_cert_path"`
	} `json:"body"`
}

// DeployAgents installs the Miabi agent on every node of the cluster's swarm as a global service. It is
// explicit because it grants Miabi the Docker socket on every machine in this swarm.
func (h *ClusterHandler) DeployAgents(c *okapi.Context, req *DeployAgentsRequest) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	err = h.cluster.DeployAgents(c.Request().Context(), id, cluster.AgentOptions{
		InsecureTLS: req.Body.InsecureSkipVerify,
		CACert:      req.Body.CACert,
		CACertPath:  req.Body.CACertPath,
	})
	if err != nil {
		return h.mapErr(c, err, "failed to deploy the cluster agents")
	}
	h.record(c, "cluster.agents.deploy", id)
	return ok(c, h.cluster.AgentStatus(c.Request().Context(), id))
}

// RemoveAgents tears the cluster's global agent service down. Its nodes go back to being unmanaged.
func (h *ClusterHandler) RemoveAgents(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	if err := h.cluster.RemoveAgents(c.Request().Context(), id); err != nil {
		return h.mapErr(c, err, "failed to remove the cluster agents")
	}
	h.record(c, "cluster.agents.remove", id)
	return message(c, "cluster agents removed")
}

// ControlPlaneCert returns the certificate the control plane currently serves, so the agents can
// be pinned to it instead of skipping verification.
func (h *ClusterHandler) ControlPlaneCert(c *okapi.Context) error {
	cert, err := h.cluster.FetchControlPlaneCert(c.Request().Context())
	switch {
	case errors.Is(err, cluster.ErrControlURLRequired), errors.Is(err, cluster.ErrNoTLS):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to read the control plane's certificate", err)
	}
	return ok(c, cert)
}

// Members lists the cluster's swarm nodes, annotated with whether each maps to a managed Miabi node.
func (h *ClusterHandler) Members(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	members, err := h.cluster.Members(c.Request().Context(), id)
	if err != nil {
		return h.mapErr(c, err, "failed to list cluster nodes")
	}
	return ok(c, members)
}

// JoinToken returns the manual join command and worker token for a host not connected over a tunnel.
func (h *ClusterHandler) JoinToken(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	inst, err := h.cluster.JoinInstructions(c.Request().Context(), id)
	if err != nil {
		return h.mapErr(c, err, "failed to get cluster join command")
	}
	return ok(c, inst)
}

// JoinNode joins a node to the cluster's swarm and moves it into the cluster.
func (h *ClusterHandler) JoinNode(c *okapi.Context) error {
	id, err := h.clusterID(c)
	if err != nil {
		return c.AbortBadRequest("invalid cluster id")
	}
	nodeID, err := h.nodeID(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if err := h.cluster.JoinNode(c.Request().Context(), id, nodeID); err != nil {
		return h.mapErr(c, err, "failed to join the node")
	}
	h.record(c, "cluster.node_join", nodeID)
	return message(c, "node joined the cluster")
}

// LeaveNode removes a node from its cluster's swarm; the node becomes a standalone cluster.
func (h *ClusterHandler) LeaveNode(c *okapi.Context) error {
	nodeID, err := h.nodeID(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if err := h.cluster.LeaveNode(c.Request().Context(), nodeID, true); err != nil {
		return h.mapErr(c, err, "failed to remove the node from the cluster")
	}
	h.record(c, "cluster.node_leave", nodeID)
	return message(c, "node removed from the cluster")
}

func (h *ClusterHandler) nodeID(c *okapi.Context) (uint, error) {
	return resolveID(c.Param("nodeID"), h.nodes.IDByUID)
}

func (h *ClusterHandler) mapErr(c *okapi.Context, err error, fallback string) error {
	switch {
	case errors.Is(err, cluster.ErrNotEnabled):
		return c.AbortBadRequest("cluster mode is not enabled")
	case errors.Is(err, cluster.ErrManagerNode):
		return c.AbortBadRequest("the manager node cannot be used for this operation")
	case errors.Is(err, cluster.ErrAdvertiseAddrRequired), errors.Is(err, cluster.ErrNameTooLong),
		errors.Is(err, cluster.ErrInvalidAvailability), errors.Is(err, cluster.ErrControlURLRequired),
		errors.Is(err, cluster.ErrAgentImageRequired):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, cluster.ErrManagerAddrUnknown), errors.Is(err, cluster.ErrNodeHasWorkloads),
		errors.Is(err, cluster.ErrClusterHasWorkloads), errors.Is(err, cluster.ErrNodeInOtherSwarm):
		return c.AbortWithError(http.StatusConflict, err)
	case errors.Is(err, cluster.ErrClusterNotFound):
		return c.AbortNotFound("cluster not found")
	case errors.Is(err, node.ErrNodeNotFound):
		return c.AbortNotFound("node not found")
	case errors.Is(err, nodes.ErrNodeOffline):
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	default:
		return c.AbortInternalServerError(fallback, err)
	}
}

func (h *ClusterHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	target := ""
	if id != 0 {
		target = strconv.Itoa(int(id))
	}
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "cluster", TargetID: target, IP: c.RealIP()})
}
