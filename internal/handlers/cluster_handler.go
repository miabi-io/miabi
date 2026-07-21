// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/cluster"
	"github.com/miabi-io/miabi/internal/services/node"
)

// ClusterHandler exposes platform-admin cluster (Docker Swarm) management:
// status, enable/adopt/disable, and per-node join/leave. Cluster mode is opt-in
// and auto-detected — these endpoints work on plain Docker too, simply reporting
// "not enabled" and refusing mutations until swarm mode is on.
type ClusterHandler struct {
	cluster *cluster.Service
	nodes   *node.Service
	audit   *audit.Logger
}

func NewClusterHandler(c *cluster.Service, n *node.Service, auditLog *audit.Logger) *ClusterHandler {
	return &ClusterHandler{cluster: c, nodes: n, audit: auditLog}
}

// Status returns the manager's current cluster (swarm) status.
func (h *ClusterHandler) Status(c *okapi.Context) error {
	return ok(c, h.clusterStatus(c))
}

// clusterStatus is Status() plus the facts that need a context to read — currently
// whether the global agent service is deployed, which decides whether swarm workers
// are managed or merely running tasks Miabi cannot see into.
func (h *ClusterHandler) clusterStatus(c *okapi.Context) cluster.Status {
	st := h.cluster.Status()
	if st.Enabled {
		a := h.cluster.AgentStatus(c.Request().Context())
		st.AgentsDeployed, st.AgentTasks = a.Deployed, a.Running
		st.AgentInsecureTLS, st.AgentCustomCA = a.InsecureTLS, a.CustomCA
		st.AgentCACertPath = a.CACertPath
	}
	return st
}

// EnableClusterRequest enables (or adopts) cluster mode.
type EnableClusterRequest struct {
	Body struct {
		// AdvertiseAddr is the address swarm peers reach this manager on (its
		// private/WG address, host or host:port). Required when initializing a new
		// swarm; ignored when adopting one Docker is already in.
		AdvertiseAddr string `json:"advertise_addr"`
		// Name labels the cluster. Swarm gives it an unreadable id and a manager address
		// that moves, so without a name the UI can only say "the cluster" — which is fine
		// with one and useless with two. Optional; nameable later.
		Name string `json:"name"`
	} `json:"body"`
}

// Enable puts the manager into swarm mode (or adopts an existing swarm).
func (h *ClusterHandler) Enable(c *okapi.Context, req *EnableClusterRequest) error {
	status, err := h.cluster.Enable(c.Request().Context(), req.Body.AdvertiseAddr, req.Body.Name)
	if err != nil {
		if errors.Is(err, cluster.ErrAdvertiseAddrRequired) {
			return c.AbortBadRequest("an advertise address is required to enable cluster mode")
		}
		return c.AbortInternalServerError("failed to enable cluster mode", err)
	}
	h.record(c, "cluster.enable", 0)
	return ok(c, status)
}

// Disable removes the manager (and member nodes) from the swarm.
func (h *ClusterHandler) Disable(c *okapi.Context) error {
	if err := h.cluster.Disable(c.Request().Context()); err != nil {
		if errors.Is(err, cluster.ErrNotEnabled) {
			return c.AbortBadRequest("cluster mode is not enabled")
		}
		return c.AbortInternalServerError("failed to disable cluster mode", err)
	}
	h.record(c, "cluster.disable", 0)
	return message(c, "cluster mode disabled")
}

// ApplyNetworking converts the workspace networks still on node-local bridges into
// swarm overlays, so apps and databases reach each other across nodes.
//
// Enable already does this on the transition into cluster mode. This is the
// explicit action for an install that was ALREADY clustered when it upgraded (it
// never saw that transition, so its workspaces are still on per-node islands), and
// for re-running the conversion after a node that was offline comes back.
//
// It briefly drops in-flight connections inside each workspace; containers are not
// restarted.
func (h *ClusterHandler) ApplyNetworking(c *okapi.Context) error {
	if err := h.cluster.ApplyNetworking(c.Request().Context()); err != nil {
		if errors.Is(err, cluster.ErrNotEnabled) {
			return c.AbortBadRequest("cluster mode is not enabled")
		}
		return c.AbortInternalServerError("failed to apply cluster networking", err)
	}
	h.record(c, "cluster.network.apply", 0)
	return ok(c, h.clusterStatus(c))
}

// RenameClusterRequest relabels the cluster.
type RenameClusterRequest struct {
	Body struct {
		// Name is the operator's label. Empty clears it — someone who no longer wants a
		// label should be able to drop it, not be stuck with one.
		Name string `json:"name"`
	} `json:"body"`
}

// Rename labels the cluster. It is a label and nothing more — one control plane drives
// one swarm, and this is not a step toward managing several.
func (h *ClusterHandler) Rename(c *okapi.Context, req *RenameClusterRequest) error {
	if err := h.cluster.SetName(req.Body.Name); err != nil {
		if errors.Is(err, cluster.ErrNameTooLong) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to rename the cluster", err)
	}
	h.record(c, "cluster.rename", 0)
	return ok(c, h.clusterStatus(c))
}

// Preflight reports what this host can and cannot do before cluster mode is turned
// on: whether its Docker engine can carry the overlay data plane to other hosts at
// all, and the ports that must be open between nodes. Read-only.
func (h *ClusterHandler) Preflight(c *okapi.Context) error {
	p, err := h.cluster.Preflight(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to inspect the Docker engine", err)
	}
	return ok(c, p)
}

// NetCheck probes the cluster's overlay data plane between every pair of nodes,
// separating the three failures that are indistinguishable from inside an app: a
// name that will not resolve, a connection that never completes, and a payload that
// silently dies at the MTU.
//
// It starts and removes probe containers, so it is a mutation, not a read.
func (h *ClusterHandler) NetCheck(c *okapi.Context) error {
	res, err := h.cluster.NetCheck(c.Request().Context())
	if err != nil {
		if errors.Is(err, cluster.ErrNotEnabled) {
			return c.AbortBadRequest("cluster mode is not enabled")
		}
		return c.AbortInternalServerError("failed to run the network check", err)
	}
	h.record(c, "cluster.netcheck", 0)
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

// SetAvailability changes a swarm node's scheduling availability. Keyed by SWARM
// node id, so an unmanaged member (no Miabi agent) can be drained too.
func (h *ClusterHandler) SetAvailability(c *okapi.Context, req *SetAvailabilityRequest) error {
	swarmNodeID := c.Param("swarmNodeID")
	if swarmNodeID == "" {
		return c.AbortBadRequest("swarm node id is required")
	}
	err := h.cluster.SetAvailability(c.Request().Context(), swarmNodeID, req.Body.Availability)
	switch {
	case errors.Is(err, cluster.ErrNotEnabled):
		return c.AbortBadRequest("cluster mode is not enabled")
	case errors.Is(err, cluster.ErrInvalidAvailability):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to change node availability", err)
	}
	h.record(c, "cluster.node.availability", 0)
	return message(c, "node availability set to "+req.Body.Availability)
}

// NodeTasks lists the service tasks the scheduler placed on a swarm node. This is
// the only way to see the workload of an unmanaged member — the containers live on
// the node, which Miabi has no Docker client for.
func (h *ClusterHandler) NodeTasks(c *okapi.Context) error {
	tasks, err := h.cluster.Tasks(c.Request().Context(), c.Param("swarmNodeID"))
	if err != nil {
		return c.AbortInternalServerError("failed to list the node's tasks", err)
	}
	return ok(c, tasks)
}

// DeployAgentsRequest configures the global agent service.
type DeployAgentsRequest struct {
	Body struct {
		// InsecureSkipVerify makes the agents skip verification of the control plane's
		// TLS certificate. Needed when the control plane is behind a self-signed or
		// private-CA certificate, where an agent otherwise dies with "certificate signed
		// by unknown authority" and never connects. It is a real downgrade — anyone able
		// to intercept the agents could impersonate a control plane that drives Docker on
		// every node — so it is a deliberate choice, not a default.
		InsecureSkipVerify bool `json:"insecure_skip_verify"`
		// CACert trusts a specific authority instead of skipping verification: the agents
		// still VERIFY, anchored on this CA, so a forged certificate is still rejected.
		// This is what makes a self-hosted control plane behind a private CA safe, and it
		// is the option an operator should reach for first.
		CACert string `json:"ca_cert"`
		// CACertPath is a CA file that already exists on every node — usually the host's
		// own trust anchor (e.g. /etc/pki/ca-trust/source/anchors/my-ca.crt). It is
		// bind-mounted into each agent, which is more direct than copying the PEM through
		// an environment variable and stays correct when the CA is rotated on the hosts.
		CACertPath string `json:"ca_cert_path"`
	} `json:"body"`
}

// DeployAgents installs the Miabi agent on every swarm worker, as a global service —
// so Swarm carries it to each node, including nodes that join later, with no SSH and
// no per-host step. Every worker becomes managed: metrics, stats, shell and
// housekeeping all start working where they previously could not.
//
// It is an explicit action, not something enabling cluster mode does silently: it
// grants Miabi the Docker socket (root-equivalent) on every machine in this swarm,
// now and in future. That is right for a homelab and surprising for a shared cluster,
// and the operator should be the one to say which they have.
func (h *ClusterHandler) DeployAgents(c *okapi.Context, req *DeployAgentsRequest) error {
	err := h.cluster.DeployAgents(c.Request().Context(), cluster.AgentOptions{
		InsecureTLS: req.Body.InsecureSkipVerify,
		CACert:      req.Body.CACert,
		CACertPath:  req.Body.CACertPath,
	})
	switch {
	case errors.Is(err, cluster.ErrNotEnabled):
		return c.AbortBadRequest("cluster mode is not enabled")
	case errors.Is(err, cluster.ErrControlURLRequired), errors.Is(err, cluster.ErrAgentImageRequired):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to deploy the cluster agents", err)
	}
	h.record(c, "cluster.agents.deploy", 0)
	return ok(c, h.cluster.AgentStatus(c.Request().Context()))
}

// RemoveAgents tears the global agent service down. The node records stay — their
// history and placements are still real — they simply go back to being unmanaged.
func (h *ClusterHandler) RemoveAgents(c *okapi.Context) error {
	if err := h.cluster.RemoveAgents(c.Request().Context()); err != nil {
		if errors.Is(err, cluster.ErrNotEnabled) {
			return c.AbortBadRequest("cluster mode is not enabled")
		}
		return c.AbortInternalServerError("failed to remove the cluster agents", err)
	}
	h.record(c, "cluster.agents.remove", 0)
	return message(c, "cluster agents removed")
}

// ControlPlaneCert returns the certificate the control plane currently serves, so the
// agents can be pinned to it instead of skipping verification.
//
// Asking an operator to find and paste a PEM is how you get them to pick "skip
// verification" instead. Miabi knows what it serves; it can just hand it over, with a
// fingerprint they can check.
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

// Members lists the swarm's nodes (docker node ls), annotated with whether each
// maps to a managed Miabi node. Drives the manager detail page's cluster view.
func (h *ClusterHandler) Members(c *okapi.Context) error {
	members, err := h.cluster.Members(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list cluster nodes", err)
	}
	return ok(c, members)
}

// JoinToken returns the manual join command + worker token for joining a host
// that is not connected to the manager over the agent tunnel.
func (h *ClusterHandler) JoinToken(c *okapi.Context) error {
	inst, err := h.cluster.JoinInstructions(c.Request().Context())
	if err != nil {
		switch {
		case errors.Is(err, cluster.ErrNotEnabled):
			return c.AbortBadRequest("cluster mode is not enabled")
		case errors.Is(err, cluster.ErrManagerAddrUnknown):
			return c.AbortWithError(http.StatusConflict, err)
		default:
			return c.AbortInternalServerError("failed to get cluster join command", err)
		}
	}
	return ok(c, inst)
}

// JoinNode joins a worker node to the swarm.
func (h *ClusterHandler) JoinNode(c *okapi.Context) error {
	id, err := h.nodeID(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if err := h.cluster.JoinNode(c.Request().Context(), id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "cluster.node_join", id)
	return message(c, "node joined the cluster")
}

// LeaveNode removes a worker node from the swarm.
func (h *ClusterHandler) LeaveNode(c *okapi.Context) error {
	id, err := h.nodeID(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if err := h.cluster.LeaveNode(c.Request().Context(), id, true); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "cluster.node_leave", id)
	return message(c, "node removed from the cluster")
}

func (h *ClusterHandler) nodeID(c *okapi.Context) (uint, error) {
	return resolveID(c.Param("nodeID"), h.nodes.IDByUID)
}

func (h *ClusterHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, cluster.ErrNotEnabled):
		return c.AbortBadRequest("cluster mode is not enabled")
	case errors.Is(err, cluster.ErrManagerNode):
		return c.AbortBadRequest("the manager node cannot be used for this operation")
	case errors.Is(err, cluster.ErrManagerAddrUnknown):
		return c.AbortWithError(http.StatusConflict, err)
	case errors.Is(err, node.ErrNodeNotFound):
		return c.AbortNotFound("node not found")
	case errors.Is(err, nodes.ErrNodeOffline):
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	default:
		return c.AbortInternalServerError("cluster operation failed", err)
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
