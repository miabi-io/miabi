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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hoststats"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/nodes"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/dockerimport"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/gpu"
	"github.com/miabi-io/miabi/internal/services/housekeeping"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/nodestats"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
)

type ImageRef interface {
	Ref(key string) string
}

// Fixed, because the join command creates it and the upgrade command has to find it again.
const agentContainerName = "miabi-agent"

// nil leaves nodes classified against the local supported floor alone, which needs no network.
type AgentReleases interface {
	LatestVersion() string
}

type SwarmEnricher interface {
	Enrich(servers []models.Server)
	// LearnIngressIP gives a node's cluster the public IP its agent connected from, when the cluster has none.
	LearnIngressIP(serverID uint, remoteAddr string)
}

// Identity is the swarm node id the agent reports, verified against the manager's membership list.
type ClusterAgentAuth interface {
	AuthenticateAgent(ctx context.Context, token, swarmNodeID, hostname string) (*models.Server, error)
}

// NodeHandler exposes admin node management and the agent connect endpoint.
type NodeHandler struct {
	nodes       *node.Service
	manager     *nodes.Manager
	gateway     *edgegateway.Service
	importer    *dockerimport.Service
	housekeeper *housekeeping.Service
	cluster     SwarmEnricher
	agentRel    AgentReleases
	images      ImageRef
	controlURL  string
	audit       *audit.Logger
	bus         *eventbus.Bus
	members     WorkspaceMembership
	gpu         *gpu.Service // GPU inventory + admin device policy (nil = disabled)
	secEnforce  bool
	hostProc    string          // procfs dir for local-node host metrics (default /host/proc)
	nodeStats   NodeHostSampler // nil = remote nodes report no host metrics
	// clusterAuth caches cluster-token agents resolved by the stats push endpoint.
	clusterAuth clusterAgentAuthCache
	// poolLabeler mirrors a node's pool onto its Swarm node label; nil leaves it to the cluster refresh.
	poolLabeler func(context.Context, *models.Server) error
	upgrader    websocket.Upgrader
}

// Listing and operating containers are unaffected; nil disables the gate.
type WorkspaceMembership interface {
	ListForUser(userID uint) ([]models.WorkspaceWithRole, error)
}

func (h *NodeHandler) SetMembership(m WorkspaceMembership) { h.members = m }

// SetAgentReleases wires the node-agent release check (nil-safe).
func (h *NodeHandler) SetAgentReleases(r AgentReleases) { h.agentRel = r }

// Computed, not stored: the node's build and the release list move independently.
func (h *NodeHandler) annotateAgentVersions(servers []models.Server) {
	latest := ""
	if h.agentRel != nil {
		latest = h.agentRel.LatestVersion()
	}
	for i := range servers {
		s := &servers[i]
		if s.AccessMode != models.AccessAgent {
			continue
		}
		state := updatecheck.ClassifyAgent(s.AgentVersion, latest)
		s.AgentState = string(state)
		if state == updatecheck.AgentOutdated || state == updatecheck.AgentUnsupported {
			s.AgentLatestVersion = latest
		}
	}
}

func (h *NodeHandler) SetSecurityEnforcement(on bool) { h.secEnforce = on }

func (h *NodeHandler) SetGPU(g *gpu.Service) { h.gpu = g }

type NodeHostSampler interface {
	Get(ctx context.Context, serverID uint) (nodestats.Sample, error)
}

// SetNodeStats wires the remote host-stats sampler (nil-safe).
func (h *NodeHandler) SetNodeStats(s NodeHostSampler) { h.nodeStats = s }

func NewNodeHandler(n *node.Service, mgr *nodes.Manager, gw *edgegateway.Service, importer *dockerimport.Service, housekeeper *housekeeping.Service, clusterEnricher SwarmEnricher, images ImageRef, controlURL string, auditLog *audit.Logger, bus *eventbus.Bus, hostProc string) *NodeHandler {
	return &NodeHandler{
		nodes:       n,
		manager:     mgr,
		gateway:     gw,
		importer:    importer,
		housekeeper: housekeeper,
		cluster:     clusterEnricher,
		images:      images,
		controlURL:  controlURL,
		audit:       auditLog,
		bus:         bus,
		secEnforce:  true, // fail closed; SetSecurityEnforcement may relax it from config
		hostProc:    hostProc,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// The Go agent sends no Origin header, so it passes; a cross-site browser Origin is rejected.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

func (h *NodeHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

type CreateNodeRequest struct {
	Body struct {
		// The URL-safe handle is derived from it at creation.
		DisplayName string `json:"display_name" required:"true"`
		Address     string `json:"address"`
		// Connectivity "cluster" relies on the cluster's gateway, "edge-gateway" runs one on the node.
		Connectivity string `json:"connectivity" enum:"edge-gateway,cluster"`
		// AccessMode is how the control plane reaches this node's Docker engine.
		AccessMode string `json:"access_mode" enum:"agent,api,socket"`
		// DockerEndpoint is required for api: tcp://host:2376.
		DockerEndpoint string `json:"docker_endpoint"`
		// api TLS material (optional, PEM).
		TLSCACert string `json:"tls_ca_cert"`
		TLSCert   string `json:"tls_cert"`
		TLSKey    string `json:"tls_key"`
		// A reachability change may briefly interrupt the node's running workloads.
		Acknowledge bool `json:"acknowledge"`
	} `json:"body"`
}

func (r *CreateNodeRequest) input() node.NodeInput {
	return node.NodeInput{
		DisplayName:    r.Body.DisplayName,
		Address:        r.Body.Address,
		Connectivity:   models.ServerConnectivity(r.Body.Connectivity),
		AccessMode:     models.ServerAccessMode(r.Body.AccessMode),
		DockerEndpoint: r.Body.DockerEndpoint,
		TLSCACert:      r.Body.TLSCACert,
		TLSCert:        r.Body.TLSCert,
		TLSKey:         r.Body.TLSKey,
		Acknowledge:    r.Body.Acknowledge,
	}
}

// List returns all nodes, annotating live agent connectivity.
func (h *NodeHandler) List(c *okapi.Context) error {
	servers, err := h.nodes.List(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list nodes", err)
	}
	for i := range servers {
		if !servers[i].IsLocal {
			servers[i].AgentConnected = h.manager.Connected(servers[i].ID)
		} else {
			servers[i].AgentConnected = true
		}
	}
	// Annotate swarm role/availability when cluster mode is on (no-op otherwise).
	if h.cluster != nil {
		h.cluster.Enrich(servers)
	}
	h.annotateAgentVersions(servers)
	return ok(c, servers)
}

func (h *NodeHandler) Create(c *okapi.Context, req *CreateNodeRequest) error {
	srv, token, err := h.nodes.CreateNode(req.input())
	if err != nil {
		if errors.Is(err, node.ErrInvalidConnectivity) {
			return c.AbortBadRequest(err.Error())
		}
		if errors.Is(err, node.ErrNameRequired) {
			return c.AbortBadRequest("node name is required")
		}
		var limitErr *node.NodeLimitError
		if errors.As(err, &limitErr) {
			// 402: the cap is an edition limit, lifted by an Enterprise license.
			return c.AbortWithError(402, limitErr)
		}
		return c.AbortInternalServerError("failed to create node", err)
	}
	if cerr := h.manager.ConnectDirect(srv); cerr != nil {
		// Record stays; the node simply shows offline until the config is fixed.
		h.record(c, "node.create", srv.ID)
		return created(c, map[string]any{"node": srv, "token": token, "connect_error": cerr.Error()})
	}
	h.record(c, "node.create", srv.ID)
	return created(c, map[string]any{"node": srv, "token": token})
}

// The join token is hashed and unrecoverable, so the command carries a placeholder.
func (h *NodeHandler) JoinCommand(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	image := "miabi/agent:latest"
	if h.images != nil {
		if r := h.images.Ref("agent"); r != "" {
			image = r
		}
	}
	controlURL := h.controlURL
	if controlURL == "" {
		controlURL = "https://<your-control-plane-url>"
	}
	const tokenPlaceholder = "<JOIN_TOKEN>"
	return ok(c, map[string]any{
		"node":              srv.Name,
		"image":             image,
		"control_url":       controlURL,
		"command":           agentRunCommand(image, controlURL, tokenPlaceholder),
		"script_command":    agentScriptCommand(controlURL, tokenPlaceholder),
		"upgrade_command":   agentUpgradeCommand(image, controlURL),
		"token_hint":        "Replace " + tokenPlaceholder + " with the node's join token (shown once at creation; use Regenerate token to mint a new one).",
		"upgrade_hint":      "Reads the running agent's own token, so it needs no new one. Pulls before it removes anything: a pull that fails after the old agent is gone would leave the node with no way back.",
		"agent_min_version": updatecheck.MinAgentVersion,
	})
}

const agentInstallScript = "https://get.miabi.io/agent"

// agentScriptCommand is the one-liner alternative to agentRunCommand.
func agentScriptCommand(controlURL, token string) string {
	return "curl -fsSL " + agentInstallScript + " | \\\n" +
		"  MIABI_CONTROL_URL=" + controlURL + " MIABI_NODE_TOKEN=" + token + " bash"
}

// The labels keep the agent out of the import list and block stop/remove; managed-by=external.
func agentRunCommand(image, controlURL, token string) string {
	return "docker run -d --name " + agentContainerName + " --restart unless-stopped \\\n" +
		"  -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
		"  --label " + docker.LabelPartOf + "=" + docker.PartOfMiabi + " \\\n" +
		"  --label " + docker.LabelRole + "=" + docker.RoleAgent + " \\\n" +
		"  --label " + docker.LabelManagedBy + "=" + docker.ManagedByExternal + " \\\n" +
		"  --label " + docker.LabelProtected + "=true \\\n" +
		"  -e MIABI_CONTROL_URL=" + controlURL + " \\\n" +
		"  -e MIABI_NODE_TOKEN=" + token + " \\\n" +
		"  " + image
}

// Reads the token back from the old container first and pulls before removing: losing the agent loses the node.
func agentUpgradeCommand(image, controlURL string) string {
	return "TOKEN=$(docker inspect " + agentContainerName +
		" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^MIABI_NODE_TOKEN=//p') \\\n" +
		"  && [ -n \"$TOKEN\" ] \\\n" +
		"  && docker pull " + image + " \\\n" +
		"  && docker rm -f " + agentContainerName + " \\\n" +
		"  && " + agentRunCommand(image, controlURL, "\"$TOKEN\"")
}

// Update edits a node's reachability settings and reconnects its direct client.
func (h *NodeHandler) Update(c *okapi.Context, req *CreateNodeRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	prev, _ := h.nodes.Get(id) // capture prior connectivity before the update
	srv, err := h.nodes.UpdateNode(id, req.input())
	if err != nil {
		return h.mapErr(c, err)
	}
	// Never evict a live agent tunnel: that would flip a connected node offline.
	h.manager.RefreshClient(srv)
	// Best-effort, only if the node is reachable now.
	if prev != nil && prev.Connectivity != srv.Connectivity {
		h.applyConnectivity(c.Request().Context(), prev, srv)
	}
	if prev != nil && (prev.Connectivity != srv.Connectivity || prev.AccessMode != srv.AccessMode ||
		prev.DockerEndpoint != srv.DockerEndpoint || prev.Address != srv.Address) {
		actor := middlewares.UserID(c)
		h.audit.Record(audit.Entry{
			ActorID: &actor, Action: "node.connectivity.change", TargetType: "node",
			TargetID: strconv.Itoa(int(srv.ID)), IP: c.RealIP(),
			Metadata: map[string]any{
				"from_connectivity": string(prev.Connectivity), "to_connectivity": string(srv.Connectivity),
				"from_access_mode": string(prev.AccessMode), "to_access_mode": string(srv.AccessMode),
			},
		})
	} else {
		h.record(c, "node.update", srv.ID)
	}
	return ok(c, srv)
}

func (h *NodeHandler) Workloads(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	apps, dbs, err := h.nodes.WorkloadImpact(id)
	if err != nil {
		return c.AbortInternalServerError("failed to count node workloads", err)
	}
	return ok(c, map[string]any{"apps": apps, "databases": dbs})
}

// SetPoolLabeler wires mirroring a node's pool onto its Swarm node label.
func (h *NodeHandler) SetPoolLabeler(fn func(ctx context.Context, srv *models.Server) error) {
	h.poolLabeler = fn
}

// SetNodePoolRequest puts a node in a pool.
type SetNodePoolRequest struct {
	Body struct {
		// Pool is the pool name, e.g. "pro"; empty takes the node out of its pool.
		Pool string `json:"pool"`
	} `json:"body"`
}

// SetPool puts a node in a pool and mirrors it onto the node's Swarm label.
func (h *NodeHandler) SetPool(c *okapi.Context, req *SetNodePoolRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.SetPool(id, req.Body.Pool)
	if errors.Is(err, node.ErrInvalidPool) {
		return c.AbortBadRequest(err.Error())
	}
	if err != nil {
		return h.mapErr(c, err)
	}
	if h.poolLabeler != nil {
		// Best-effort: the cluster refresh re-asserts the label once the swarm is reachable.
		_ = h.poolLabeler(c.Request().Context(), srv)
	}
	h.record(c, "node.pool", srv.ID)
	return ok(c, srv)
}

// ApplyConnectivity deploys or tears down a node's gateway after its connectivity changed elsewhere.
func (h *NodeHandler) ApplyConnectivity(ctx context.Context, prev, srv *models.Server) {
	h.applyConnectivity(ctx, prev, srv)
}

// Best-effort: failures are logged but never fail the update.
func (h *NodeHandler) applyConnectivity(ctx context.Context, prev, srv *models.Server) {
	dc, err := h.manager.Clients().For(srv.ID)
	if err != nil {
		return // node offline; gateway will deploy on next agent connect
	}
	switch {
	case srv.Connectivity == models.ConnectivityEdgeGateway:
		if !edgegateway.AutoDeploy(srv) {
			return // an imported gateway already serves the node
		}
		if tok, terr := h.nodes.GatewayToken(srv.ID); terr == nil {
			if derr := h.gateway.Ensure(ctx, dc, srv, tok, h.gatewayRedisPassword(srv)); derr == nil {
				h.nodes.MarkGatewayDeployed(srv.ID)
			}
		}
	case prev.Connectivity == models.ConnectivityEdgeGateway:
		h.gateway.Teardown(ctx, dc)
	}
}

// RegenerateToken issues a fresh join token.
func (h *NodeHandler) RegenerateToken(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	token, err := h.nodes.RegenerateToken(id)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "node.token_regenerate", id)
	return ok(c, map[string]any{"token": token})
}

// Cordon / Uncordon toggle a node's drain flag.
func (h *NodeHandler) Cordon(c *okapi.Context) error   { return h.setCordon(c, true) }
func (h *NodeHandler) Uncordon(c *okapi.Context) error { return h.setCordon(c, false) }

func (h *NodeHandler) setCordon(c *okapi.Context, v bool) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	err = h.nodes.SetCordoned(c.Request().Context(), id, v)
	if err != nil && !errors.Is(err, node.ErrSwarmNotMirrored) {
		return h.mapErr(c, err)
	}
	action := "node.uncordon"
	if v {
		action = "node.cordon"
	}
	h.record(c, action, id)
	// A failed Swarm mirror is reported so the operator does not trust a cordon Swarm will ignore.
	if err != nil {
		return message(c, err.Error())
	}
	return message(c, "node updated")
}

// Delete removes a node and tears down its tunnel.
func (h *NodeHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	// Tear down over the still-open tunnel before closing it.
	if srv, err := h.nodes.Get(id); err == nil {
		h.manager.Teardown(c.Request().Context(), srv)
	}
	h.manager.Disconnect(id)
	h.manager.DisconnectDirect(id) // drop any direct (socket/api) client
	h.manager.ForgetStatus(id)
	if err := h.nodes.DeleteNode(id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "node.delete", id)
	return message(c, "node deleted")
}

type NodeStats struct {
	docker.Info
	Volumes  int `json:"volumes"`
	Networks int `json:"networks"`
	// Nil when unknown (detection found nothing or the agent is offline).
	SelfContainer *SelfContainerRef `json:"self_container,omitempty"`
}

// SelfContainerRef identifies Miabi's own container on a node.
type SelfContainerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *NodeHandler) Stats(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	ctx := c.Request().Context()
	info, err := dc.Info(ctx)
	if err != nil {
		return c.AbortInternalServerError("failed to query node", err)
	}
	stats := NodeStats{Info: info}
	if vols, verr := dc.ListVolumes(ctx); verr == nil {
		stats.Volumes = len(vols)
	}
	if nets, nerr := dc.ListNetworks(ctx); nerr == nil {
		stats.Networks = len(nets)
	}
	// Detection may only know a short ID or hostname.
	if selfID := h.manager.Clients().SelfContainerID(id); selfID != "" {
		if cont, ierr := dc.InspectContainer(ctx, selfID); ierr == nil {
			name := cont.ID
			if len(cont.Names) > 0 {
				name = strings.TrimPrefix(cont.Names[0], "/")
			}
			stats.SelfContainer = &SelfContainerRef{ID: cont.ID, Name: name}
		}
	}
	return ok(c, stats)
}

type HostMetricsResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
	// Sampled is true for any remote reading. Deprecated: read Source.
	Sampled      bool `json:"sampled,omitempty"`
	PhysicalHost bool `json:"physical_host,omitempty"`
	// AgeSeconds is how old a remote reading is.
	AgeSeconds    int     `json:"age_seconds"`
	Load1         float64 `json:"load1,omitempty"`
	UptimeSeconds uint64  `json:"uptime_s,omitempty"`
	hoststats.Stats
}

func (h *NodeHandler) HostMetrics(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	if h.nodeStats != nil {
		st, serr := h.nodeStats.Get(c.Request().Context(), srv.ID)
		if serr != nil {
			return ok(c, HostMetricsResponse{Available: false, Reason: "could not sample the node: " + serr.Error()})
		}
		// /proc is not cgroup-aware, so a containerized node reports the machine underneath it.
		age := 0
		if !st.MeasuredAt.IsZero() {
			age = int(time.Since(st.MeasuredAt).Seconds())
		}
		return ok(c, HostMetricsResponse{
			Available: true, Source: st.Source, Sampled: st.Source != nodestats.SourceLocal,
			PhysicalHost: !st.DescribesNode, AgeSeconds: age, Load1: st.Load1, UptimeSeconds: st.UptimeSeconds,
			Stats: st.Stats,
		})
	}
	if !srv.IsLocal {
		return ok(c, HostMetricsResponse{Available: false, Reason: "host metrics are only available for the local node"})
	}
	// /proc already reflects host CPU/memory even from inside a container.
	path := h.hostProc
	if path == "" || !hoststats.Available(path) {
		path = "/proc"
	}
	st, err := hoststats.Read(c.Request().Context(), path)
	if err != nil {
		return ok(c, HostMetricsResponse{Available: false, Reason: "host procfs is not readable"})
	}
	return ok(c, HostMetricsResponse{Available: true, Source: "local", Stats: st})
}

// Authenticated by join token, not the user JWT; blocks until the agent disconnects.
func (h *NodeHandler) Connect(c *okapi.Context) error {
	token := bearer(c.Header("Authorization"))
	if token == "" {
		token = c.Query("token")
	}
	swarmNodeID := c.Header("X-Agent-Swarm-Node-ID")
	hostname := c.Header("X-Agent-Hostname")

	srv, err := h.nodes.Authenticate(token)
	if err != nil {
		// The cluster-wide token is trusted only because the manager verifies the swarm node id.
		srv, err = h.registerClusterAgent(c, token, swarmNodeID, hostname)
		if err != nil {
			return c.AbortUnauthorized("invalid agent token")
		}
	}

	if h.cluster != nil {
		h.cluster.LearnIngressIP(srv.ID, c.RealIP())
	}

	h.nodes.LearnSwarmNodeID(srv.ID, swarmNodeID)
	ws, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil
	}
	h.manager.Handle(srv, token, c.Header("X-Agent-Version"), c.Header("X-Agent-Container-ID"), ws)
	return nil
}

func (h *NodeHandler) registerClusterAgent(c *okapi.Context, token, swarmNodeID, hostname string) (*models.Server, error) {
	auth, ok := h.cluster.(ClusterAgentAuth)
	if !ok || auth == nil {
		return nil, node.ErrBadToken
	}
	return auth.AuthenticateAgent(c.Request().Context(), token, swarmNodeID, hostname)
}

func bearer(h string) string {
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

type GPUListResponse struct {
	Enabled        bool               `json:"enabled"`         // platform GPU support on (MIABI_GPU_ENABLED)
	ToolkitPresent bool               `json:"toolkit_present"` // node has the NVIDIA Container Toolkit
	Devices        []models.GPUDevice `json:"devices"`
}

type UpdateGPURequest struct {
	Body struct {
		Enabled *bool `json:"enabled"`
		Shared  *bool `json:"shared"`
	} `json:"body"`
}

// Devices arrive disabled until an admin opts each one in.
func (h *NodeHandler) ListGPUs(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if h.gpu == nil || !h.gpu.Enabled() {
		return ok(c, GPUListResponse{Enabled: false, Devices: []models.GPUDevice{}})
	}
	devices, err := h.gpu.Devices(id)
	if err != nil {
		return c.AbortInternalServerError("failed to list node GPUs", err)
	}
	if devices == nil {
		devices = []models.GPUDevice{}
	}
	return ok(c, GPUListResponse{
		Enabled:        true,
		ToolkitPresent: h.gpu.NodeCapable(c.Request().Context(), id),
		Devices:        devices,
	})
}

func (h *NodeHandler) UpdateGPU(c *okapi.Context, req *UpdateGPURequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	gpuID, err := uintParam(c, "gpuID")
	if err != nil {
		return c.AbortBadRequest("invalid gpu id")
	}
	if h.gpu == nil || !h.gpu.Enabled() {
		return c.AbortBadRequest("GPU support is disabled on this platform")
	}
	dev, err := h.gpu.SetDevice(id, gpuID, req.Body.Enabled, req.Body.Shared)
	if err != nil {
		if errors.Is(err, gpu.ErrDeviceNotOnNode) {
			return c.AbortNotFound("gpu not found on this node")
		}
		return c.AbortInternalServerError("failed to update GPU", err)
	}
	h.record(c, "node.gpu.update", id)
	return ok(c, dev)
}

func (h *NodeHandler) RescanGPUs(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if h.gpu == nil || !h.gpu.Enabled() {
		return c.AbortBadRequest("GPU support is disabled on this platform")
	}
	if _, err := h.gpu.InventoryNode(c.Request().Context(), id); err != nil {
		return c.AbortInternalServerError("GPU rescan failed", err)
	}
	devices, err := h.gpu.Devices(id)
	if err != nil {
		return c.AbortInternalServerError("failed to list node GPUs", err)
	}
	if devices == nil {
		devices = []models.GPUDevice{}
	}
	h.record(c, "node.gpu.rescan", id)
	return ok(c, GPUListResponse{
		Enabled:        true,
		ToolkitPresent: h.gpu.NodeCapable(c.Request().Context(), id),
		Devices:        devices,
	})
}

func (h *NodeHandler) id(c *okapi.Context) (uint, error) {
	id, err := resolveID(c.Param("nodeID"), h.nodes.IDByUID)
	if err != nil {
		return 0, errors.New("invalid node id")
	}
	return id, nil
}

func (h *NodeHandler) record(c *okapi.Context, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: "node", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *NodeHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, node.ErrNodeNotFound):
		return c.AbortNotFound("node not found")
	case errors.Is(err, node.ErrLocalNode):
		return c.AbortBadRequest("the local node cannot be modified this way")
	case errors.Is(err, node.ErrConnectivityAckRequired):
		return c.AbortWithError(409, err)
	case errors.Is(err, node.ErrInvalidConnectivity), errors.Is(err, node.ErrEdgeGatewayInDefaultCluster):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("node operation failed", err)
	}
}
