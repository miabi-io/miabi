// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
)

// ImageRef resolves a platform-image catalog key to an image ref (implemented
// by platformimage.Resolver).
type ImageRef interface {
	Ref(key string) string
}

// SwarmEnricher annotates servers with their transient swarm role/availability
// for the Nodes page. Implemented by cluster.Service; nil on builds without it.
type SwarmEnricher interface {
	Enrich(servers []models.Server)
}

// NodeHandler exposes admin node management and the agent connect endpoint.
type NodeHandler struct {
	nodes       *node.Service
	manager     *nodes.Manager
	gateway     *edgegateway.Service
	importer    *dockerimport.Service
	housekeeper *housekeeping.Service
	cluster     SwarmEnricher
	images      ImageRef
	controlURL  string
	audit       *audit.Logger
	bus         *eventbus.Bus
	members     WorkspaceMembership
	gpu         *gpu.Service // GPU inventory + admin device policy (nil = disabled)
	// secEnforce blocks disruptive raw-Docker actions (stop/remove) on managed
	// containers from the admin node view (MIABI_SECURITY_ENFORCEMENT, default on).
	secEnforce bool
	hostProc   string // procfs dir for local-node host metrics (default /host/proc)
	upgrader   websocket.Upgrader
}

// WorkspaceMembership lists the workspaces a user belongs to. It gates node
// container-log streaming so a platform admin cannot read the logs of a
// container owned by a workspace they are not a member of (listing and operating
// containers is unaffected). Implemented by the workspace repository; injected
// after construction (nil disables the guard).
type WorkspaceMembership interface {
	ListForUser(userID uint) ([]models.WorkspaceWithRole, error)
}

// SetMembership wires the workspace-membership lookup used to gate foreign
// container-log streaming (nil-safe).
func (h *NodeHandler) SetMembership(m WorkspaceMembership) { h.members = m }

// SetSecurityEnforcement toggles blocking of stop/remove on managed containers
// from the admin node view (MIABI_SECURITY_ENFORCEMENT; default on).
func (h *NodeHandler) SetSecurityEnforcement(on bool) { h.secEnforce = on }

// SetGPU wires the GPU inventory/policy service (nil-safe; nil = GPU support
// disabled, so the GPU endpoints report it off).
func (h *NodeHandler) SetGPU(g *gpu.Service) { h.gpu = g }

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
			// The agent is a Go client (no Origin header), so it passes; a browser
			// with a cross-site Origin is rejected. SetAllowedOrigins may widen this
			// to the configured allowlist.
			CheckOrigin: allowWSOrigin(nil),
		},
	}
}

// SetAllowedOrigins restricts browser WebSocket upgrades to same-origin plus the
// given origins (Go agents send no Origin and are unaffected).
func (h *NodeHandler) SetAllowedOrigins(origins []string) {
	h.upgrader.CheckOrigin = allowWSOrigin(origins)
}

type CreateNodeRequest struct {
	Body struct {
		Name    string `json:"name" required:"true"`
		Address string `json:"address"`
		// PublicIP / PublicHostname are the node's externally reachable DNS target.
		PublicIP       string `json:"public_ip"`
		PublicHostname string `json:"public_hostname"`
		Connectivity   string `json:"connectivity" enum:"port-forward,edge-gateway"`
		// AccessMode is how the control plane reaches this node's Docker engine.
		AccessMode string `json:"access_mode" enum:"agent,api,socket"`
		// DockerEndpoint is required for api: tcp://host:2376.
		DockerEndpoint string `json:"docker_endpoint"`
		// api TLS material (optional, PEM).
		TLSCACert string `json:"tls_ca_cert"`
		TLSCert   string `json:"tls_cert"`
		TLSKey    string `json:"tls_key"`
		// Acknowledge confirms the caller accepts that a reachability change may
		// briefly interrupt the node's running workloads (required on such a change).
		Acknowledge bool `json:"acknowledge"`
	} `json:"body"`
}

func (r *CreateNodeRequest) input() node.NodeInput {
	return node.NodeInput{
		Name:           r.Body.Name,
		Address:        r.Body.Address,
		PublicIP:       r.Body.PublicIP,
		PublicHostname: r.Body.PublicHostname,
		Connectivity:   models.ServerConnectivity(r.Body.Connectivity),
		AccessMode:     models.ServerAccessMode(r.Body.AccessMode),
		DockerEndpoint: r.Body.DockerEndpoint,
		TLSCACert:      r.Body.TLSCACert,
		TLSCert:        r.Body.TLSCert,
		TLSKey:         r.Body.TLSKey,
		Acknowledge:    r.Body.Acknowledge,
	}
}

// PlaceableNode is the minimal node info any workspace member needs to choose
// where to place a resource. It deliberately omits admin-only fields (token
// hash, address) — node names are not secret, but credentials are.
type PlaceableNode struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	Connectivity string `json:"connectivity"`
	IsLocal      bool   `json:"is_local"`
	Online       bool   `json:"online"`
	Cordoned     bool   `json:"cordoned"`
}

// ListPlaceable returns the nodes a resource can be placed on, for the create
// forms' node picker. Available to any authenticated user (not just platform
// admins), since developers choose placement when creating apps/databases.
func (h *NodeHandler) ListPlaceable(c *okapi.Context) error {
	servers, err := h.nodes.List(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list nodes", err)
	}
	out := make([]PlaceableNode, 0, len(servers))
	for i := range servers {
		s := &servers[i]
		out = append(out, PlaceableNode{
			ID:           s.ID,
			Name:         s.Name,
			Connectivity: string(s.Connectivity),
			IsLocal:      s.IsLocal,
			Online:       s.IsLocal || h.manager.Connected(s.ID),
			Cordoned:     s.Cordoned,
		})
	}
	return ok(c, out)
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
	return ok(c, servers)
}

// Create registers a remote node. For agent mode it returns a one-time join
// token; for socket/api it connects the node's Docker client immediately.
func (h *NodeHandler) Create(c *okapi.Context, req *CreateNodeRequest) error {
	srv, token, err := h.nodes.CreateNode(req.input())
	if err != nil {
		if errors.Is(err, node.ErrNameRequired) {
			return c.AbortBadRequest("node name is required")
		}
		var limitErr *node.NodeLimitError
		if errors.As(err, &limitErr) {
			// 402: the cap is an edition limit, lifted by an Enterprise license.
			// The envelope promotes Code()/Message() (NODE_LIMIT_REACHED).
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

// JoinCommand returns the docker command an operator runs on the node host to
// start the agent and connect it to this control plane. The join token is
// hashed (unrecoverable), so the command carries a placeholder — the operator
// uses the token shown at node creation, or regenerates one.
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
	command := "docker run -d --name miabi-agent --restart unless-stopped \\\n" +
		"  -v /var/run/docker.sock:/var/run/docker.sock \\\n" +
		"  -e MIABI_CONTROL_URL=" + controlURL + " \\\n" +
		"  -e MIABI_NODE_TOKEN=" + tokenPlaceholder + " \\\n" +
		"  " + image
	return ok(c, map[string]any{
		"node":        srv.Name,
		"image":       image,
		"control_url": controlURL,
		"command":     command,
		"token_hint":  "Replace " + tokenPlaceholder + " with the node's join token (shown once at creation; use Regenerate token to mint a new one).",
	})
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
	// Rebuild the client to pick up endpoint/mode/credential changes — but never
	// evict a live agent tunnel (that would flip a connected node offline).
	h.manager.RefreshClient(srv)
	// React to a connectivity change: deploy the gateway when switching to
	// edge-gateway, tear it down when switching away (best-effort, only if the
	// node is reachable now).
	if prev != nil && prev.Connectivity != srv.Connectivity {
		h.applyConnectivity(c.Request().Context(), prev, srv)
	}
	// Record a reachability change distinctly (with before/after) so it stands out
	// in the audit log from a routine metadata edit.
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

// Workloads returns how many apps and databases are placed on the node, so the
// UI can show the blast radius of a connectivity change.
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

// applyConnectivity deploys or tears down the node gateway after a connectivity
// switch. Best-effort: a deploy needs a live Docker client; failures are logged
// (the admin can retry from the gateway panel) but never fail the update.
func (h *NodeHandler) applyConnectivity(ctx context.Context, prev, srv *models.Server) {
	dc, err := h.manager.Clients().For(srv.ID)
	if err != nil {
		return // node offline; gateway will deploy on next agent connect
	}
	switch {
	case srv.Connectivity == models.ConnectivityEdgeGateway:
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
	if err := h.nodes.SetCordoned(id, v); err != nil {
		return h.mapErr(c, err)
	}
	action := "node.uncordon"
	if v {
		action = "node.cordon"
	}
	h.record(c, action, id)
	return message(c, "node updated")
}

// Delete removes a node and tears down its tunnel.
func (h *NodeHandler) Delete(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	// Tear down node-side infrastructure (e.g. the edge gateway) over the
	// still-open tunnel before closing it and dropping the record.
	if srv, err := h.nodes.Get(id); err == nil {
		h.manager.Teardown(c.Request().Context(), srv)
	}
	h.manager.Disconnect(id)
	h.manager.DisconnectDirect(id) // drop any direct (socket/api) client
	if err := h.nodes.DeleteNode(id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, "node.delete", id)
	return message(c, "node deleted")
}

// NodeStats is the node detail dashboard payload: the engine info plus volume
// and network counts, so the page needs a single request rather than separate
// info/containers/volumes/networks calls.
type NodeStats struct {
	docker.Info
	Volumes  int `json:"volumes"`
	Networks int `json:"networks"`
	// SelfContainer is Miabi's own runtime container on this node (the
	// control plane locally, or the node's agent), surfaced so the UI can show a
	// dedicated card. Nil when unknown (detection found nothing / agent offline).
	SelfContainer *SelfContainerRef `json:"self_container,omitempty"`
}

// SelfContainerRef identifies Miabi's own container on a node.
type SelfContainerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Stats returns the node's live Docker engine info (version, CPU, memory,
// container/image counts) plus volume and network counts, in one request.
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
	// Miabi's own container on this node, so the UI can show a runtime card.
	// Resolve the full ID + name (detection may only know a short ID/hostname).
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

// HostMetricsResponse reports real host CPU/memory for the local node. Available
// is false when host stats can't be read here — for remote nodes (the procfs is
// the control plane's, not theirs) or when no readable procfs is mounted.
type HostMetricsResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	hoststats.Stats
}

// HostMetrics returns the local node's real host CPU and memory usage, read from
// procfs (the optional /host/proc bind, else /proc). Remote nodes report
// Available=false, since this process can only read its own host.
func (h *NodeHandler) HostMetrics(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	if !srv.IsLocal {
		return ok(c, HostMetricsResponse{Available: false, Reason: "host metrics are only available for the local node"})
	}
	// Prefer the configured path (default /host/proc); fall back to /proc, which
	// already reflects host CPU/memory even from inside a container.
	path := h.hostProc
	if path == "" || !hoststats.Available(path) {
		path = "/proc"
	}
	st, err := hoststats.Read(c.Request().Context(), path)
	if err != nil {
		return ok(c, HostMetricsResponse{Available: false, Reason: "host procfs is not readable"})
	}
	return ok(c, HostMetricsResponse{Available: true, Stats: st})
}

// Connect is the agent WebSocket endpoint: authenticated by join token (not the
// user JWT). It upgrades the connection and hands the tunnel to the manager,
// blocking until the agent disconnects.
func (h *NodeHandler) Connect(c *okapi.Context) error {
	token := bearer(c.Header("Authorization"))
	if token == "" {
		token = c.Query("token")
	}
	srv, err := h.nodes.Authenticate(token)
	if err != nil {
		return c.AbortUnauthorized("invalid agent token")
	}
	// Learn the node's public endpoint from this connection — its source IP (as
	// seen here) and self-reported hostname — so the admin needn't enter them when
	// adding the node. Non-destructive (only fills blank fields). Read before the
	// upgrade hijacks the request.
	h.nodes.LearnEndpoint(srv.ID, c.RealIP(), c.Header("X-Agent-Hostname"))
	ws, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil // upgrade failed; response already handled
	}
	h.manager.Handle(srv, token, c.Header("X-Agent-Version"), c.Header("X-Agent-Container-ID"), ws)
	return nil
}

func bearer(h string) string {
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// --- GPUs ---

// GPUListResponse is a node's GPU inventory plus whether the node advertises the
// NVIDIA runtime (so the UI can explain a node showing no cards).
type GPUListResponse struct {
	Enabled        bool               `json:"enabled"`         // platform GPU support on (MIABI_GPU_ENABLED)
	ToolkitPresent bool               `json:"toolkit_present"` // node has the NVIDIA Container Toolkit
	Devices        []models.GPUDevice `json:"devices"`
}

// UpdateGPURequest toggles a device's admin policy. Both fields are optional
// (nil = leave unchanged).
type UpdateGPURequest struct {
	Body struct {
		Enabled *bool `json:"enabled"`
		Shared  *bool `json:"shared"`
	} `json:"body"`
}

// ListGPUs returns the GPUs discovered on a node and the platform/node GPU
// capability flags. Devices arrive disabled until an admin opts each one in.
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

// UpdateGPU applies admin policy (enable/disable, shared/dedicated) to one of a
// node's GPUs.
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

// RescanGPUs re-runs the inventory probe on a node on demand (the admin "Rescan
// GPUs" button) and returns the refreshed device list.
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
	default:
		return c.AbortInternalServerError("node operation failed", err)
	}
}
