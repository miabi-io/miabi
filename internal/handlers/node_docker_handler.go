// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/edgegateway"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/node"
)

// Node-scoped Docker operations. Every action resolves the target node's Docker
// client via the registry (local or remote agent), so the control-plane node is
// managed at /admin/nodes/{localID}/... exactly like any agent node. Removal of
// platform-managed resources (labelled miabi.*) is blocked — they must be
// managed through their owning app/volume, not deleted from under Miabi.

// --- request DTOs ---

type RunContainerRequest struct {
	Body struct {
		Name        string            `json:"name" required:"true"`
		Image       string            `json:"image" required:"true"`
		Env         map[string]string `json:"env"`
		Cmd         []string          `json:"cmd"`
		Ports       map[string]string `json:"ports"`  // "80/tcp" -> "8080"
		Mounts      map[string]string `json:"mounts"` // volume -> path
		MemoryBytes int64             `json:"memory_bytes"`
		NanoCPUs    int64             `json:"nano_cpus"`
		Network     string            `json:"network"`
	} `json:"body"`
}

type PullImageRequest struct {
	Body struct {
		Ref string `json:"ref" required:"true"`
	} `json:"body"`
}

type CreateVolumeRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
	} `json:"body"`
}

type CreateDockerNetworkRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		Driver   string `json:"driver"`
		Internal bool   `json:"internal"`
	} `json:"body"`
}

type RunContainerResponse struct {
	ID string `json:"id"`
}

// dc resolves the Docker client for the {nodeID} path param. A node with no live
// client (offline agent) returns 503 so the UI can show an offline state.
func (h *NodeHandler) dc(c *okapi.Context) (docker.Client, error) {
	id, err := h.id(c)
	if err != nil {
		return nil, c.AbortBadRequest("invalid node id")
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return nil, c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	return dc, nil
}

// isManaged reports whether a Docker resource is owned by Miabi (any platform
// label, io.miabi.* or legacy). Such resources are protected from direct removal.
func isManaged(labels map[string]string) bool {
	return docker.IsManaged(labels)
}

// isNodeGateway reports whether cont is the given node's edge (Goma) gateway —
// either the managed one (io.miabi.role=node-gateway) or an imported one tracked
// by its container name. The imported gateway carries no platform label, so it
// would otherwise slip past isManaged and be deletable from the containers list.
func (h *NodeHandler) isNodeGateway(nodeID uint, cont docker.Container) bool {
	if role, _ := docker.LabelValue(cont.Labels, docker.LabelRole); role == "node-gateway" {
		return true
	}
	srv, err := h.nodes.Get(nodeID)
	if err != nil {
		return false
	}
	name := edgegateway.ContainerNameFor(srv)
	for _, n := range cont.Names {
		if strings.TrimPrefix(n, "/") == name {
			return true
		}
	}
	return false
}

// guardContainerOp blocks destructive container operations (stop/restart/remove)
// on platform-critical containers that have no owning resource to manage them
// through: the Miabi control plane or this node's agent, and the node's edge
// gateway. When blockManaged is set (removal), any Miabi-managed container
// (apps, databases, …) is blocked too, since those are removed via their resource.
//
// When the op must be blocked it writes a 409 response and returns true; the
// caller MUST then return without performing the operation. A failed inspect is
// ignored (returns false) so the underlying op can surface its own not-found.
//
// It returns a bool rather than an error on purpose: okapi's c.Abort* helpers
// write the response but return a nil error, so the return value cannot drive
// control flow — checking it would let the destructive op run after the 409.
func (h *NodeHandler) guardContainerOp(c *okapi.Context, dc docker.Client, nodeID uint, cid string, blockManaged bool) bool {
	cont, err := dc.InspectContainer(c.Request().Context(), cid)
	if err != nil {
		return false
	}
	switch {
	case h.manager.Clients().IsSelfContainer(nodeID, cont.ID):
		_ = c.AbortWithError(http.StatusConflict, errors.New("this is the Miabi control-plane/agent container and cannot be modified from the containers list"))
	case docker.IsProtected(cont.Labels):
		// The Miabi stack itself: its Postgres, its Redis, the central gateway. The
		// self-check above only ever covers ONE container (the one this process runs
		// in) — it cannot protect the database the process depends on, and it protects
		// nothing at all on a node the control plane does not run on. Stopping
		// miabi-postgres from the containers list took the whole platform down.
		_ = c.AbortWithError(http.StatusConflict, errors.New(protectedMessage(cont.Labels)))
	case h.isNodeGateway(nodeID, cont):
		_ = c.AbortWithError(http.StatusConflict, errors.New("this is the node's edge gateway; manage it from the node's gateway page"))
	case blockManaged && isManaged(cont.Labels):
		_ = c.AbortWithError(http.StatusConflict, errors.New("this container is managed by Miabi; manage it from its app or gateway"))
	default:
		return false
	}
	return true
}

// protectedMessage names the component being protected, so the 409 tells the admin
// what they just tried to break rather than only that they may not.
func protectedMessage(labels map[string]string) string {
	role, _ := docker.LabelValue(labels, docker.LabelRole)
	what := map[string]string{
		docker.RoleControlPlane:  "the Miabi control plane",
		docker.RolePlatformDB:    "Miabi's own database",
		docker.RolePlatformCache: "Miabi's own Redis",
		docker.RoleGateway:       "the Miabi gateway",
		docker.RoleAgent:         "this node's Miabi agent",
		docker.RoleRegistry:      "the built-in registry",
	}[role]
	if what == "" {
		what = "part of the Miabi platform"
	}
	msg := "this container is " + what + " and cannot be stopped or removed from the containers list"
	if docker.ManagedBy(labels) == docker.ManagedByCompose {
		msg += "; it is managed by Docker Compose (docker compose up -d in your Miabi install directory)"
	}
	return msg
}

// --- containers ---

func (h *NodeHandler) ContainersList(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	list, err := dc.ListContainers(c.Request().Context(), c.Query("all") != "false")
	if err != nil {
		return c.AbortInternalServerError("failed to list containers", err)
	}
	return ok(c, list)
}

// foreignWorkspaceContainer reports whether a container belongs to a workspace
// the viewer is NOT a member of. Unowned/raw containers and platform
// infrastructure (the node's gateway/redis) are not foreign. Used to keep a
// platform admin from reading another workspace's container logs.
func foreignWorkspaceContainer(labels map[string]string, member map[uint]bool) bool {
	if docker.IsPlatformInfra(labels) {
		return false
	}
	wsID, ok := docker.WorkspaceID(labels)
	if !ok {
		return false // not workspace-owned
	}
	return !member[wsID]
}

// blockForeignLogs writes a 403 and returns true when the viewer may not read
// the given container's logs: it is owned by a workspace they are not a member
// of. With no membership source wired, or for unowned/infra containers, it is a
// no-op. A failed inspect is ignored so the log stream surfaces its own 404.
func (h *NodeHandler) blockForeignLogs(c *okapi.Context, dc docker.Client, cid string) bool {
	if h.members == nil {
		return false
	}
	cont, err := dc.InspectContainer(c.Request().Context(), cid)
	if err != nil {
		return false
	}
	if !foreignWorkspaceContainer(cont.Labels, h.memberWorkspaces(c)) {
		return false
	}
	_ = c.AbortForbidden("these logs belong to another workspace's container; you are not a member of that workspace")
	return true
}

// memberWorkspaces returns the set of workspace ids the request's user belongs to.
func (h *NodeHandler) memberWorkspaces(c *okapi.Context) map[uint]bool {
	member := map[uint]bool{}
	rows, err := h.members.ListForUser(middlewares.UserID(c))
	if err != nil {
		return member
	}
	for _, r := range rows {
		member[r.ID] = true
	}
	return member
}

// NodePortUsage is one host port published on the node, with its owner. Built
// from live Docker so it includes containers deployed outside Miabi — the
// missing piece that let imports auto-bind a port already taken externally.
type NodePortUsage struct {
	HostPort    int    `json:"host_port"`
	PrivatePort int    `json:"private_port"`
	Protocol    string `json:"protocol"`
	Container   string `json:"container"`
	ContainerID string `json:"container_id"`
	Managed     bool   `json:"managed"` // owned by Miabi (miabi.* label)
}

// NodePorts lists every host port currently published on the node (across all
// running containers, platform-managed or not), so admins can see what's taken
// before binding a new one.
func (h *NodeHandler) NodePorts(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	list, err := dc.ListContainers(c.Request().Context(), false) // running only
	if err != nil {
		return c.AbortInternalServerError("failed to list containers", err)
	}
	out := []NodePortUsage{}
	for _, ct := range list {
		name := ct.ID
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		managed := isManaged(ct.Labels)
		for _, p := range ct.Ports {
			if p.PublicPort == 0 {
				continue // not published to the host
			}
			out = append(out, NodePortUsage{
				HostPort: int(p.PublicPort), PrivatePort: int(p.PrivatePort), Protocol: p.Protocol,
				Container: name, ContainerID: ct.ID, Managed: managed,
			})
		}
	}
	return ok(c, out)
}

// ContainerStat is one container's live resource sample.
type ContainerStat struct {
	ID string `json:"id"`
	docker.StatsSample
}

// ContainersStats returns a one-shot resource sample for each running container
// on the node (CPU/memory/cumulative net), for the live containers table. The
// UI polls this and derives net rate from successive samples.
func (h *NodeHandler) ContainersStats(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	list, err := dc.ListContainers(ctx, false) // running only
	if err != nil {
		return c.AbortInternalServerError("failed to list containers", err)
	}
	out := make([]ContainerStat, len(list))
	sem := make(chan struct{}, 8) // bound concurrent stats calls
	var wg sync.WaitGroup
	for i := range list {
		out[i].ID = list[i].ID
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, cid string) {
			defer wg.Done()
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if s, serr := dc.StatsOnce(sctx, cid); serr == nil {
				out[i].StatsSample = s
			}
		}(i, list[i].ID)
	}
	wg.Wait()
	return ok(c, out)
}

func (h *NodeHandler) InspectContainer(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	cont, err := dc.InspectContainer(c.Request().Context(), c.Param("cid"))
	if err != nil {
		return h.dockerErr(c, err, "container")
	}
	return ok(c, cont)
}

func (h *NodeHandler) RunContainer(c *okapi.Context, req *RunContainerRequest) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	env := make([]string, 0, len(req.Body.Env))
	for k, v := range req.Body.Env {
		env = append(env, k+"="+v)
	}
	networks := []string{node.AppNetwork}
	if req.Body.Network != "" {
		networks = []string{req.Body.Network}
	}
	id, err := dc.RunContainer(c.Request().Context(), docker.RunSpec{
		Name: req.Body.Name, Image: req.Body.Image, Env: env, Cmd: req.Body.Cmd,
		Ports: req.Body.Ports, Mounts: req.Body.Mounts, Networks: networks,
		MemoryBytes: req.Body.MemoryBytes, NanoCPUs: req.Body.NanoCPUs,
	})
	if err != nil {
		return c.AbortInternalServerError("failed to run container", err)
	}
	h.recordDocker(c, "container.run", "container", id)
	return created(c, RunContainerResponse{ID: id})
}

func (h *NodeHandler) StopContainer(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	id, _ := h.id(c)
	// With security enforcement on, a managed container can't be stopped from the
	// raw containers list — stop it from its owning app/database instead.
	if h.guardContainerOp(c, dc, id, c.Param("cid"), h.secEnforce) {
		return nil // 409 already written by the guard
	}
	if err := dc.StopContainer(c.Request().Context(), c.Param("cid"), 10); err != nil {
		return h.dockerErr(c, err, "container")
	}
	h.recordDocker(c, "container.stop", "container", c.Param("cid"))
	return message(c, "container stopped")
}

func (h *NodeHandler) RestartContainer(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	id, _ := h.id(c)
	if h.guardContainerOp(c, dc, id, c.Param("cid"), false) {
		return nil // 409 already written by the guard
	}
	if err := dc.RestartContainer(c.Request().Context(), c.Param("cid"), 10); err != nil {
		return h.dockerErr(c, err, "container")
	}
	h.recordDocker(c, "container.restart", "container", c.Param("cid"))
	return message(c, "container restarted")
}

func (h *NodeHandler) RemoveContainer(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	cid := c.Param("cid")
	id, _ := h.id(c)
	// Guard: the control plane/agent and the node's edge gateway are always
	// protected. With security enforcement on (default), platform-managed
	// containers (apps, databases, …) are too — they're removed via their owning
	// resource, not deleted from under Miabi here. Disabling enforcement is a
	// break-glass escape hatch.
	if h.guardContainerOp(c, dc, id, cid, h.secEnforce) {
		return nil // 409 already written by the guard
	}
	if err := dc.RemoveContainer(c.Request().Context(), cid, c.Query("force") == "true"); err != nil {
		return h.dockerErr(c, err, "container")
	}
	h.recordDocker(c, "container.remove", "container", cid)
	return message(c, "container removed")
}

// ContainerLogs streams container logs as Server-Sent Events. A platform admin
// may list and operate the node's containers, but must not read the logs of a
// container owned by a workspace they don't belong to — those can leak another
// tenant's secrets/PII. Unowned/raw containers and the node's own infrastructure
// are unaffected.
func (h *NodeHandler) ContainerLogs(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	if h.blockForeignLogs(c, dc, c.Param("cid")) {
		return nil // 403 already written by the guard
	}
	follow := c.Query("follow") == "true"
	serr := dc.StreamLogs(c.Request().Context(), c.Param("cid"), follow, c.Query("tail"), func(line docker.LogLine) error {
		return c.SSESendJSON(line)
	})
	if errors.Is(serr, docker.ErrNotFound) {
		return c.AbortNotFound("container not found")
	}
	return serr
}

// ContainerStats streams resource usage samples as Server-Sent Events.
func (h *NodeHandler) ContainerStats(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	serr := dc.StreamStats(c.Request().Context(), c.Param("cid"), func(s docker.StatsSample) error {
		return c.SSESendJSON(s)
	})
	if errors.Is(serr, docker.ErrNotFound) {
		return c.AbortNotFound("container not found")
	}
	return serr
}

// --- images ---

func (h *NodeHandler) PullImage(c *okapi.Context, req *PullImageRequest) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	if perr := dc.PullImage(c.Request().Context(), req.Body.Ref, nil); perr != nil {
		return c.AbortInternalServerError("failed to pull image", perr)
	}
	h.recordDocker(c, "image.pull", "image", req.Body.Ref)
	return message(c, "image pulled")
}

// --- networks ---

func (h *NodeHandler) NetworksList(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	nets, err := dc.ListNetworks(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list networks", err)
	}
	return ok(c, nets)
}

func (h *NodeHandler) CreateNetwork(c *okapi.Context, req *CreateDockerNetworkRequest) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	if _, cerr := dc.CreateNetwork(c.Request().Context(), req.Body.Name, req.Body.Driver, req.Body.Internal); cerr != nil {
		return c.AbortInternalServerError("failed to create network", cerr)
	}
	h.recordDocker(c, "network.create", "network", req.Body.Name)
	return message(c, "network created")
}

func (h *NodeHandler) RemoveNetwork(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	name := c.Param("name")
	// Guard: managed networks (workspace/stack/gateway) are removed via their
	// owning resource. Look the network up to read its labels.
	if nets, lerr := dc.ListNetworks(c.Request().Context()); lerr == nil {
		for _, n := range nets {
			if (n.Name == name || n.ID == name) && isManaged(n.Labels) {
				return c.AbortWithError(http.StatusConflict, errors.New("this network is managed by Miabi"))
			}
		}
	}
	if err := dc.RemoveNetwork(c.Request().Context(), name); err != nil {
		return h.dockerErr(c, err, "network")
	}
	h.recordDocker(c, "network.remove", "network", name)
	return message(c, "network removed")
}

// --- volumes ---

func (h *NodeHandler) VolumesList(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	vols, err := dc.ListVolumes(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("failed to list volumes", err)
	}
	return ok(c, vols)
}

func (h *NodeHandler) CreateVolume(c *okapi.Context, req *CreateVolumeRequest) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	vol, cerr := dc.CreateVolume(c.Request().Context(), req.Body.Name, nil, 0)
	if cerr != nil {
		return c.AbortInternalServerError("failed to create volume", cerr)
	}
	h.recordDocker(c, "volume.create", "volume", vol.Name)
	return created(c, vol)
}

func (h *NodeHandler) RemoveVolume(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	name := c.Param("name")
	// Guard: managed volumes (app data) are removed via their volume page.
	if v, ierr := dc.InspectVolume(c.Request().Context(), name); ierr == nil && isManaged(v.Labels) {
		return c.AbortWithError(http.StatusConflict, errors.New("this volume is managed by Miabi; remove it from its volume page"))
	}
	if err := dc.RemoveVolume(c.Request().Context(), name, c.Query("force") == "true"); err != nil {
		return h.dockerErr(c, err, "volume")
	}
	h.recordDocker(c, "volume.remove", "volume", name)
	return message(c, "volume removed")
}

// --- edge gateway ---

// GatewayStatus reports the live state of the node's gateway container.
type GatewayStatus struct {
	Connectivity   string `json:"connectivity"`
	Deployed       bool   `json:"deployed"` // container exists
	Running        bool   `json:"running"`
	Imported       bool   `json:"imported"`            // adopted from a pre-existing container
	Container      string `json:"container,omitempty"` // the tracked container name
	Image          string `json:"image,omitempty"`     // running container's image
	ImageEffective string `json:"image_effective,omitempty"`
	ImageOverride  string `json:"image_override,omitempty"`
	Health         string `json:"health,omitempty"`
	Status         string `json:"status,omitempty"`
	// RedisEnabled: the gateway has Redis wired (shared cache + distributed rate
	// limiting). RedisShared: it reuses the platform Redis (the manager) rather
	// than a per-node Redis (remote edge nodes).
	RedisEnabled bool `json:"redis_enabled"`
	RedisShared  bool `json:"redis_shared"`
	// Update is the in-flight safe-update progress, if any.
	Update *models.GatewayUpdateProgress `json:"update,omitempty"`
}

// GatewayUpdateEvent is the SSE payload carrying a node gateway's live
// safe-update progress.
type GatewayUpdateEvent struct {
	Update *models.GatewayUpdateProgress `json:"update,omitempty"`
}

// gwTopic is the per-node event-bus topic carrying live gateway update progress.
func gwTopic(nodeID uint) string { return fmt.Sprintf("node-gateway:%d", nodeID) }

// gatewayUpdateActive reports whether a non-terminal gateway update is in flight.
func gatewayUpdateActive(p *models.GatewayUpdateProgress) bool {
	return p != nil && p.Phase != "" && p.Phase != "done" && p.Phase != "failed"
}

// gatewayRedisPassword returns the per-node gateway Redis password for a remote
// edge node, or "" for the manager (which reuses the platform Redis).
func (h *NodeHandler) gatewayRedisPassword(srv *models.Server) string {
	if srv.IsLocal {
		return ""
	}
	pw, err := h.nodes.GatewayRedisPassword(srv.ID)
	if err != nil {
		return ""
	}
	return pw
}

// GatewayState returns the node's gateway connectivity + live container status.
func (h *NodeHandler) GatewayState(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	out := GatewayStatus{
		Connectivity:   string(srv.Connectivity),
		Imported:       srv.GatewayImported,
		Container:      edgegateway.ContainerNameFor(srv),
		ImageEffective: h.gateway.Image(srv),
		ImageOverride:  srv.GatewayImage,
		RedisEnabled:   h.gateway.RedisEnabled(srv),
		RedisShared:    srv.IsLocal,
		Update:         srv.GatewayUpdate,
	}
	dc, derr := h.manager.Clients().For(id)
	if derr != nil {
		return ok(c, out) // offline: connectivity known, no live status
	}
	if cont, ierr := dc.InspectContainer(c.Request().Context(), out.Container); ierr == nil {
		out.Deployed = true
		out.Running = cont.State == "running"
		out.Image = cont.Image
		out.Health = cont.Health
		out.Status = cont.Status
	}
	return ok(c, out)
}

// GatewayDeploy (re)deploys the node's Goma gateway. Requires edge-gateway
// connectivity and a reachable node.
func (h *NodeHandler) GatewayDeploy(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	if srv.Connectivity != models.ConnectivityEdgeGateway {
		return c.AbortBadRequest("node connectivity is not edge-gateway")
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	tok, err := h.nodes.GatewayToken(id)
	if err != nil {
		return c.AbortInternalServerError("failed to mint gateway token", err)
	}
	if err := h.gateway.Ensure(c.Request().Context(), dc, srv, tok, h.gatewayRedisPassword(srv)); err != nil {
		return c.AbortInternalServerError("failed to deploy gateway", err)
	}
	h.nodes.MarkGatewayDeployed(id)
	h.record(c, "node.gateway_deploy", id)
	return message(c, "gateway deployed")
}

// GatewayUpdate starts a safe, observed update of the node's gateway to its
// resolved image: a test container is started with the same config + volumes,
// observed for a grace window, then promoted over the live gateway. It runs in
// the background; progress streams over GatewayEvents (SSE) and is persisted on
// the node so it survives a reconnect.
func (h *NodeHandler) GatewayUpdate(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	if srv.Connectivity != models.ConnectivityEdgeGateway {
		return c.AbortBadRequest("node connectivity is not edge-gateway")
	}
	if srv.GatewayImported {
		return c.AbortBadRequest("this gateway was imported and is managed externally; use Install/redeploy to convert it to a managed gateway")
	}
	if gatewayUpdateActive(srv.GatewayUpdate) {
		return c.AbortWithError(http.StatusConflict, errors.New("a gateway update is already in progress"))
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	tok, err := h.nodes.GatewayToken(id)
	if err != nil {
		return c.AbortInternalServerError("failed to mint gateway token", err)
	}

	to := h.gateway.Image(srv)
	from := ""
	if cont, ierr := dc.InspectContainer(c.Request().Context(), edgegateway.ContainerNameFor(srv)); ierr == nil {
		from = cont.Image
	}
	prog := &models.GatewayUpdateProgress{FromImage: from, ToImage: to, Phase: "queued"}
	_ = h.nodes.SetGatewayUpdate(id, prog)
	h.publishGatewayUpdate(id, prog)

	go h.runGatewayUpdate(id, dc, srv, tok, h.gatewayRedisPassword(srv), from, to)
	h.record(c, "node.gateway_update", id)
	return ok(c, prog)
}

// runGatewayUpdate drives a safe gateway update to completion in the background,
// persisting + publishing each phase. The request context is gone by now, so it
// uses a fresh bounded context.
func (h *NodeHandler) runGatewayUpdate(id uint, dc docker.Client, srv *models.Server, token, redisPassword, from, to string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	onPhase := func(phase string, cause error) {
		p := &models.GatewayUpdateProgress{FromImage: from, ToImage: to, Phase: phase}
		if cause != nil {
			p.Error = cause.Error()
		}
		_ = h.nodes.SetGatewayUpdate(id, p)
		h.publishGatewayUpdate(id, p)
	}
	if err := h.gateway.SafeUpdate(ctx, dc, srv, token, redisPassword, onPhase); err != nil {
		return // failure already persisted + published by onPhase
	}
	h.nodes.MarkGatewayDeployed(id)
	// Clear the persisted progress so the page returns to idle; the live "done"
	// event already told open streams the update finished.
	_ = h.nodes.SetGatewayUpdate(id, nil)
}

// GatewayEvents streams a node gateway's safe-update progress as SSE.
func (h *NodeHandler) GatewayEvents(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	// Initial snapshot so a late subscriber sees the current phase immediately.
	if err := c.SSESendJSON(eventbus.Event{Type: "status", Data: GatewayUpdateEvent{Update: srv.GatewayUpdate}}); err != nil {
		return err
	}
	if h.bus == nil {
		<-c.Request().Context().Done()
		return nil
	}
	ch, unsubscribe := h.bus.Subscribe(gwTopic(id))
	defer unsubscribe()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			if err := c.SSESendJSON(e); err != nil {
				return err
			}
		}
	}
}

// publishGatewayUpdate fans out a node gateway's update progress to SSE subscribers.
func (h *NodeHandler) publishGatewayUpdate(id uint, p *models.GatewayUpdateProgress) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(gwTopic(id), eventbus.Event{Type: "status", Data: GatewayUpdateEvent{Update: p}})
}

// GatewayTeardown removes the node's managed gateway container (certs volume is
// kept). For an imported gateway it only stops tracking — the admin's own
// container is left running untouched.
func (h *NodeHandler) GatewayTeardown(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	if srv.GatewayImported {
		if _, rerr := h.nodes.ReleaseGateway(id); rerr != nil {
			return c.AbortInternalServerError("failed to release gateway", rerr)
		}
		h.record(c, "node.gateway_release", id)
		return message(c, "stopped managing the imported gateway (container left running)")
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	h.gateway.Teardown(c.Request().Context(), dc)
	h.record(c, "node.gateway_teardown", id)
	return message(c, "gateway removed")
}

// GatewayCandidates lists running containers on the node that look like a Goma
// gateway, so the admin can import an existing one instead of installing fresh.
func (h *NodeHandler) GatewayCandidates(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	list, err := dc.ListContainers(c.Request().Context(), true)
	if err != nil {
		return c.AbortInternalServerError("failed to list containers", err)
	}
	type candidate struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
	}
	out := []candidate{}
	for _, ct := range list {
		if !edgegateway.LooksLikeGateway(ct.Image) {
			continue
		}
		name := ct.ID
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		out = append(out, candidate{ID: ct.ID, Name: name, Image: ct.Image, State: ct.State})
	}
	return ok(c, out)
}

// ImportGatewayRequest selects an existing gateway container to adopt.
type ImportGatewayRequest struct {
	Body struct {
		// Container is the name or ID of the gateway container to adopt. If empty
		// and exactly one candidate exists on the node, that one is adopted.
		Container string `json:"container"`
	} `json:"body"`
}

// GatewayImport adopts an existing (already-running) gateway container as this
// node's gateway — no recreate, zero downtime. Useful on the manager, which
// often already runs a Goma gateway for the platform.
func (h *NodeHandler) GatewayImport(c *okapi.Context, req *ImportGatewayRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	dc, err := h.manager.Clients().For(id)
	if err != nil {
		return c.AbortWithError(http.StatusServiceUnavailable, err)
	}
	ctx := c.Request().Context()
	target := strings.TrimSpace(req.Body.Container)
	if target == "" {
		// Auto-pick when there is exactly one gateway-looking container.
		list, lerr := dc.ListContainers(ctx, true)
		if lerr != nil {
			return c.AbortInternalServerError("failed to list containers", lerr)
		}
		var matches []docker.Container
		for _, ct := range list {
			if edgegateway.LooksLikeGateway(ct.Image) {
				matches = append(matches, ct)
			}
		}
		if len(matches) == 0 {
			return c.AbortBadRequest("no gateway-like container found to import; install one instead")
		}
		if len(matches) > 1 {
			return c.AbortBadRequest("multiple gateway containers found; specify which to import")
		}
		target = matches[0].ID
	}
	cfg, err := dc.InspectContainerConfig(ctx, target)
	if err != nil {
		return h.dockerErr(c, err, "container")
	}
	name := strings.TrimPrefix(target, "/")
	if cfg.Name != "" {
		name = strings.TrimPrefix(cfg.Name, "/")
	}
	// Best-effort: copy the running gateway's goma.yml so the imported gateway
	// keeps its existing config. Resolve the path the way Goma does (--config/-c,
	// then GOMA_CONFIG_FILE, then the default). A read failure doesn't block the
	// import — the node just falls back to the rendered default config.
	configPath := edgegateway.ConfigFilePath(cfg.Entrypoint, cfg.Command, cfg.Env)
	var configYAML string
	if data, rerr := dc.ReadContainerFile(ctx, target, configPath); rerr == nil {
		configYAML = string(data)
	}
	if _, err := h.nodes.AdoptGateway(id, name, cfg.Image, configYAML); err != nil {
		return c.AbortInternalServerError("failed to adopt gateway", err)
	}
	h.record(c, "node.gateway_import", id)
	return h.GatewayState(c)
}

// GatewayConfig returns the node's edge-gateway config (custom or the rendered
// default) plus the default (for reset) and whether the default is in use.
func (h *NodeHandler) GatewayConfig(c *okapi.Context) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	srv, err := h.nodes.Get(id)
	if err != nil {
		return c.AbortNotFound("node not found")
	}
	def := h.gateway.RenderConfig(srv)
	cfg := srv.GatewayConfigYAML
	isDefault := strings.TrimSpace(cfg) == ""
	if isDefault {
		cfg = def
	}
	return ok(c, map[string]any{"config": cfg, "default": def, "is_default": isDefault})
}

// UpdateNodeGatewayConfigRequest is the body for saving a node's gateway config.
type UpdateNodeGatewayConfigRequest struct {
	Body struct {
		// Config is the goma.yml; empty resets the node to the rendered default.
		Config string `json:"config"`
	} `json:"body"`
}

// UpdateGatewayConfig validates and stores a node's custom gateway config (empty
// resets to default). Apply it by redeploying the gateway.
func (h *NodeHandler) UpdateGatewayConfig(c *okapi.Context, req *UpdateNodeGatewayConfigRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	config := req.Body.Config
	if strings.TrimSpace(config) != "" {
		if verr := edgegateway.Validate(config); verr != nil {
			return c.AbortBadRequest(verr.Error())
		}
	}
	if _, err := h.nodes.SetGatewayConfig(id, config); err != nil {
		return c.AbortNotFound("node not found")
	}
	h.record(c, "node.gateway_config_update", id)
	return h.GatewayConfig(c)
}

// UpdateNodeGatewayImageRequest is the body for overriding a node's gateway image.
type UpdateNodeGatewayImageRequest struct {
	Body struct {
		// Image is the gateway image/tag; empty resets to the resolved default.
		Image string `json:"image"`
	} `json:"body"`
}

// UpdateGatewayImage sets a node's edge-gateway image override (empty resets to
// the default). Apply it by redeploying the gateway.
func (h *NodeHandler) UpdateGatewayImage(c *okapi.Context, req *UpdateNodeGatewayImageRequest) error {
	id, err := h.id(c)
	if err != nil {
		return c.AbortBadRequest("invalid node id")
	}
	if _, err := h.nodes.SetGatewayImage(id, req.Body.Image); err != nil {
		return c.AbortNotFound("node not found")
	}
	h.record(c, "node.gateway_image_update", id)
	return h.GatewayState(c)
}

// GatewayLogs streams the node gateway container's logs as SSE.
func (h *NodeHandler) GatewayLogs(c *okapi.Context) error {
	dc, err := h.dc(c)
	if err != nil {
		return err
	}
	name := edgegateway.ContainerName
	if id, ierr := h.id(c); ierr == nil {
		if srv, gerr := h.nodes.Get(id); gerr == nil {
			name = edgegateway.ContainerNameFor(srv)
		}
	}
	follow := c.Query("follow") == "true"
	serr := dc.StreamLogs(c.Request().Context(), name, follow, c.Query("tail"), func(line docker.LogLine) error {
		return c.SSESendJSON(line)
	})
	if errors.Is(serr, docker.ErrNotFound) {
		return c.AbortNotFound("gateway not deployed")
	}
	return serr
}

// --- helpers ---

func (h *NodeHandler) recordDocker(c *okapi.Context, action, targetType, targetID string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, Action: action, TargetType: targetType, TargetID: targetID, IP: c.RealIP()})
}

func (h *NodeHandler) dockerErr(c *okapi.Context, err error, kind string) error {
	if errors.Is(err, docker.ErrNotFound) {
		return c.AbortNotFound(kind + " not found")
	}
	return c.AbortInternalServerError("docker operation failed", err)
}
