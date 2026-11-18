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

// Package placement picks a location then a node for a new resource, once at create; nothing is rescheduled.
package placement

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrLocationNotFound   = errors.New("no such location")
	ErrLocationNotAllowed = errors.New("this workspace cannot use that location")
	ErrLocationCordoned   = errors.New("that location accepts no new resources")
	ErrNoLocation         = errors.New("no location is available to this workspace")
	ErrNoSchedulableNode  = errors.New("no node in that location can take new resources right now")
	ErrNodePinAdminOnly   = errors.New("only platform admins can choose a node; choose a location instead")
	ErrLocationMismatch   = errors.New("the chosen node is not in the chosen location")
	ErrNoGPUNode          = errors.New("no schedulable node in that location has enough enabled GPUs of the requested kind")
	ErrNodePinningOff     = errors.New("choosing a node is turned off for this workspace; choose a location instead")
	ErrNodeNotInLocation  = errors.New("node not found in this location")
	ErrNodeCordoned       = errors.New("that node is cordoned and accepts no new resources")
	ErrNodeAndServerID    = errors.New("set node or server_id, not both")
)

// GPUInventory lists a node's enabled, present GPUs. Satisfied by the GPU device repository.
type GPUInventory interface {
	ListEnabledByServer(serverID uint) ([]models.GPUDevice, error)
}

// Online reports whether a remote node's agent is connected. Satisfied by nodes.Clients.Connected.
type Online func(serverID uint) bool

// Service places new resources.
type Service struct {
	clusters *repositories.ClusterRepository
	servers  *repositories.ServerRepository
	online   Online
	policy   Policy
	orgs     Orgs
	gpus     GPUInventory
	pinning  bool
}

// SetNodePinning is the platform default for whether workspaces may choose a node; a plan may override it.
func (s *Service) SetNodePinning(on bool) { s.pinning = on }

// PinningAllowed reports whether a workspace may set placement.node; platform admins always may.
func (s *Service) PinningAllowed(workspaceID uint, admin bool) bool {
	if admin {
		return true
	}
	if p, enforced := s.planPlacement(workspaceID); enforced && p.NodePinning != nil {
		return *p.NodePinning
	}
	return s.pinning
}

// Unset (GPU support off) places GPU requests like any other app.
func (s *Service) SetGPUInventory(g GPUInventory) { s.gpus = g }

func NewService(clusters *repositories.ClusterRepository, servers *repositories.ServerRepository, online Online) *Service {
	return &Service{clusters: clusters, servers: servers, online: online, pinning: true}
}

type Policy interface {
	EffectivePlacement(workspaceID uint) (models.PlanPlacement, bool)
}

type Orgs interface {
	OrganizationOfWorkspace(workspaceID uint) uint
	OwnsClusters(orgID uint) bool
	// Empty when it cannot be read.
	OrganizationLabel(orgID uint) string
}

// Visibility follows the caller, ownership the workspace: an org owning clusters is confined to them for every caller.
type access struct {
	admin    bool
	orgID    uint
	confined bool
}

// Admins are excepted because they can already list every cluster.
func (a access) hidden(c *models.Cluster) bool {
	if a.admin {
		return false
	}
	if c.OrganizationID != nil {
		return a.orgID == 0 || *c.OrganizationID != a.orgID
	}
	return c.Visibility == models.ClusterVisibilityRestricted
}

// allows reports whether a create for this workspace may land in the cluster.
func (a access) allows(c *models.Cluster) bool {
	// Ownership is checked before the admin bypass on purpose: it binds the workspace, not the caller.
	if c.OrganizationID != nil {
		return a.orgID != 0 && *c.OrganizationID == a.orgID
	}
	if a.confined {
		return false
	}
	if a.admin {
		return true
	}
	return c.Visibility != models.ClusterVisibilityRestricted
}

// Resolved even for an admin, because confinement follows the workspace.
func (s *Service) accessFor(workspaceID uint, admin bool) access {
	if s.orgs == nil {
		return access{admin: admin}
	}
	orgID := s.orgs.OrganizationOfWorkspace(workspaceID)
	if orgID == 0 {
		return access{admin: admin}
	}
	return access{admin: admin, orgID: orgID, confined: s.orgs.OwnsClusters(orgID)}
}

// SetPolicy wires plan placement (nil-safe; nil binds no workspace to locations or pools).
func (s *Service) SetPolicy(p Policy) { s.policy = p }

// SetOrgs wires organization-dedicated clusters (nil-safe; nil leaves every cluster shared).
func (s *Service) SetOrgs(o Orgs) { s.orgs = o }

func (s *Service) planPlacement(workspaceID uint) (models.PlanPlacement, bool) {
	if s.policy == nil {
		return models.PlanPlacement{}, false
	}
	return s.policy.EffectivePlacement(workspaceID)
}

// Confined orgs are exempt: a plan's location list covers shared locations, and intersecting would leave nothing.
func permits(p models.PlanPlacement, enforced bool, clusterID uint, confined bool) bool {
	return confined || !enforced || len(p.Locations) == 0 || slices.Contains(p.Locations, clusterID)
}

// Request describes a resource to place.
type Request struct {
	WorkspaceID uint
	// Location is a location (cluster) name; empty uses the workspace's default location.
	Location string
	// ServerID pins a node, which only platform admins may do.
	ServerID uint
	// Node pins a node by its fixed name, within the location and the nodes the workspace's plan may use.
	Node string
	// Colocate is a node the platform itself requires, such as the node of a database being reused.
	Colocate uint
	Admin    bool
	// Service marks a replicated service app: Swarm schedules it, so its node is only the manager.
	Service bool
	// GPUs and GPUKind narrow the node choice to nodes that have that many matching enabled cards.
	GPUs    int
	GPUKind string
}

// Result is where the resource lands.
type Result struct {
	ClusterID uint `json:"cluster_id"`
	ServerID  uint `json:"server_id"`
}

// Place resolves the cluster and node a new resource lands on.
func (s *Service) Place(req Request) (Result, error) {
	if name := strings.TrimSpace(req.Node); name != "" {
		if req.ServerID != 0 {
			return Result{}, ErrNodeAndServerID
		}
		return s.pinNode(req, name)
	}
	if req.ServerID != 0 && !req.Admin {
		return Result{}, ErrNodePinAdminOnly
	}
	if (req.ServerID != 0 || req.Colocate != 0) && strings.TrimSpace(req.Location) != "" {
		// Check the location before the node, or a mismatch would confirm a hidden location exists.
		if c, err := s.clusters.FindByName(strings.TrimSpace(req.Location)); err == nil && s.accessFor(req.WorkspaceID, req.Admin).hidden(c) {
			return Result{}, ErrLocationNotFound
		}
	}
	if req.ServerID != 0 {
		res, err := s.onNode(req.ServerID, req.Location, true)
		if err != nil {
			return Result{}, err
		}
		// An admin pin must not land in another org's cluster the workspace could not otherwise reach.
		if err := s.sameOrg(req, res.ClusterID); err != nil {
			return Result{}, err
		}
		return res, nil
	}
	if req.Colocate != 0 {
		res, err := s.onNode(req.Colocate, req.Location, false)
		if err != nil {
			return Result{}, err
		}
		// Colocation may reach restricted clusters but must not cross into another org's dedicated cluster.
		if err := s.sameOrg(req, res.ClusterID); err != nil {
			return Result{}, err
		}
		return res, nil
	}
	c, err := s.resolveCluster(req.WorkspaceID, req.Location, req.Admin)
	if err != nil {
		return Result{}, err
	}
	policy, enforced := s.planPlacement(req.WorkspaceID)
	serverID, err := s.pickNode(c, req, policy.Pool, enforced)
	if err != nil {
		return Result{}, err
	}
	return Result{ClusterID: c.ID, ServerID: serverID}, nil
}

// Applies to admins too; see the access doc comment.
func (s *Service) sameOrg(req Request, clusterID uint) error {
	c, err := s.clusters.FindByID(s.resolveID(clusterID))
	if err != nil {
		return nil
	}
	acc := s.accessFor(req.WorkspaceID, req.Admin)
	if c.OrganizationID == nil {
		if acc.confined {
			return ErrLocationNotAllowed
		}
		return nil
	}
	if acc.orgID != *c.OrganizationID {
		return ErrLocationNotAllowed
	}
	return nil
}

// A pin refuses a cordoned node; colocation follows an existing resource, so it does not.
func (s *Service) onNode(serverID uint, location string, pin bool) (Result, error) {
	srv, err := s.servers.FindByID(serverID)
	if err != nil {
		return Result{}, node.ErrNodeNotFound
	}
	if pin && srv.Cordoned {
		return Result{}, ErrNoSchedulableNode
	}
	if name := strings.TrimSpace(location); name != "" {
		c, err := s.clusters.FindByName(name)
		if err != nil {
			return Result{}, ErrLocationNotFound
		}
		// The control-plane node may carry cluster id 0, which stands for the default cluster.
		if c.ID != s.resolveID(srv.ClusterID) {
			return Result{}, ErrLocationMismatch
		}
	}
	return Result{ClusterID: s.resolveID(srv.ClusterID), ServerID: serverID}, nil
}

func (s *Service) ResolveLocation(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	return s.resolveCluster(workspaceID, location, admin)
}

// The default cluster comes first among usable locations.
func (s *Service) resolveCluster(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	policy, enforced := s.planPlacement(workspaceID)
	acc := s.accessFor(workspaceID, admin)
	usable := func(c *models.Cluster) bool {
		return acc.allows(c) && !c.Cordoned && permits(policy, enforced, c.ID, acc.confined)
	}
	if name := strings.TrimSpace(location); name != "" {
		c, err := s.clusters.FindByName(name)
		if err != nil {
			return nil, ErrLocationNotFound
		}
		// Hidden rather than forbidden so a stranger cannot enumerate tenant locations by name.
		if acc.hidden(c) {
			return nil, ErrLocationNotFound
		}
		if !acc.allows(c) || !permits(policy, enforced, c.ID, acc.confined) {
			return nil, ErrLocationNotAllowed
		}
		if c.Cordoned {
			return nil, ErrLocationCordoned
		}
		return c, nil
	}
	if def, err := s.clusters.WorkspaceDefault(workspaceID); err == nil && usable(def) {
		return def, nil
	}
	if enforced {
		for _, id := range policy.Locations {
			if c, err := s.clusters.FindByID(id); err == nil && usable(c) {
				return c, nil
			}
		}
	}
	list, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if usable(&list[i]) {
			return &list[i], nil
		}
	}
	if acc.confined {
		// Name the org so the operator checks its clusters rather than the workspace's plan.
		who := "this workspace's organization"
		if s.orgs != nil {
			if label := s.orgs.OrganizationLabel(acc.orgID); label != "" {
				who = label
			}
		}
		return nil, fmt.Errorf("%w: %s runs its own locations and none of them is accepting new resources", ErrNoLocation, who)
	}
	return nil, ErrNoLocation
}

// pickNode prefers the least container memory placed; cordoned nodes are refused, and pooled plans count only pool nodes.
func (s *Service) pickNode(c *models.Cluster, req Request, pool string, pooled bool) (uint, error) {
	service := req.Service
	if pooled && (c.Mode != models.ClusterModeSwarm || service) {
		servers, err := s.servers.List()
		if err != nil {
			return 0, err
		}
		if !slices.ContainsFunc(servers, func(srv models.Server) bool {
			return srv.ClusterID == c.ID && !srv.Cordoned && models.PoolOf(&srv) == pool
		}) {
			return 0, poolUnavailable(pool)
		}
	}
	if c.Mode != models.ClusterModeSwarm || service {

		hostsWorkload := c.Mode != models.ClusterModeSwarm
		if c.IsDefault {
			local, err := s.servers.FindLocal()
			if err != nil {
				return 0, ErrNoSchedulableNode
			}
			if hostsWorkload && local.Cordoned {
				return 0, ErrNoSchedulableNode
			}
			return local.ID, nil
		}
		if hostsWorkload && s.cordoned(c.ManagerServerID) {
			return 0, ErrNoSchedulableNode
		}
		return c.ManagerServerID, nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return 0, err
	}
	loads, err := s.clusters.ServerLoads(c.ID)
	if err != nil {
		return 0, err
	}
	var best uint
	var bestLoad repositories.ServerLoad
	bestOnGPUNode := false
	gpuShort := false
	for i := range servers {
		srv := &servers[i]
		if srv.ClusterID != c.ID || srv.Cordoned || (!srv.IsLocal && (s.online == nil || !s.online(srv.ID))) {
			continue
		}
		if pooled && models.PoolOf(srv) != pool {
			continue
		}
		if !s.hasGPUs(srv.ID, req.GPUs, req.GPUKind) {
			gpuShort = true
			continue
		}
		// A GPU-free request goes to a GPU node only when no other node can take it, so cards stay reachable for GPU apps.
		onGPUNode := req.GPUs <= 0 && s.offersGPUs(srv.ID)
		load := loads[srv.ID]
		switch {
		case best == 0,
			bestOnGPUNode && !onGPUNode,
			bestOnGPUNode == onGPUNode && load.Less(bestLoad):
			best, bestLoad, bestOnGPUNode = srv.ID, load, onGPUNode
		}
	}
	if best == 0 && gpuShort {
		return 0, ErrNoGPUNode
	}
	if best == 0 && pooled {
		return 0, poolUnavailable(pool)
	}
	if best == 0 {
		return 0, ErrNoSchedulableNode
	}
	return best, nil
}

func (s *Service) offersGPUs(serverID uint) bool {
	if s.gpus == nil {
		return false
	}
	devices, err := s.gpus.ListEnabledByServer(serverID)
	return err == nil && len(devices) > 0
}

// Capacity only: whether a dedicated card is free is decided at deploy, where the node's running containers are visible.
func (s *Service) hasGPUs(serverID uint, want int, kind string) bool {
	if want <= 0 || s.gpus == nil {
		return true
	}
	devices, err := s.gpus.ListEnabledByServer(serverID)
	if err != nil {
		return false
	}
	n := 0
	for _, d := range devices {
		if d.MatchesKind(kind) {
			n++
		}
	}
	return n >= want
}

// An unreadable node is not cordoned: a transient DB fault must not fail the create.
func (s *Service) cordoned(serverID uint) bool {
	srv, err := s.servers.FindByID(serverID)
	return err == nil && srv.Cordoned
}

func poolUnavailable(pool string) error {
	if pool == "" {
		return fmt.Errorf("%w: every node there is in a pool this workspace's plan does not use", ErrNoSchedulableNode)
	}
	return fmt.Errorf("%w: none is in the %q pool this workspace's plan requires", ErrNoSchedulableNode, pool)
}

// PoolConstraints pin to the plan's pool label or, with no pool, exclude every pool; nil when no policy applies.
func (s *Service) PoolConstraints(workspaceID, clusterID uint) []string {
	policy, enforced := s.planPlacement(workspaceID)
	if !enforced {
		return nil
	}
	if policy.Pool != "" {
		return []string{fmt.Sprintf("node.labels.%s==%s", models.PoolLabel, policy.Pool)}
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil
	}
	clusterID = s.resolveID(clusterID)
	seen := map[string]bool{}
	var out []string
	for i := range servers {
		if p := models.PoolOf(&servers[i]); p != "" && servers[i].ClusterID == clusterID && !seen[p] {
			seen[p] = true
			out = append(out, fmt.Sprintf("node.labels.%s!=%s", models.PoolLabel, p))
		}
	}
	sort.Strings(out)
	return out
}

// Node is a node as a create form sees it: enough to pin a resource, never its address or capacity.
type Node struct {
	ID uint `json:"id"`
	// Name is fixed at node creation, so manifests and API calls use it; DisplayName may change.
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	IsLocal     bool   `json:"is_local"`
	Online      bool   `json:"online"`
	Cordoned    bool   `json:"cordoned"`
	// Pinnable is whether this workspace may set placement.node to it right now.
	Pinnable bool `json:"pinnable"`
	// Swarm schedules services, so a pin is a node.id== constraint rather than a server id.
	SwarmNodeID string `json:"swarm_node_id,omitempty"`
}

// Nodes resolves the location exactly as Place does, so an unusable location is refused the same way.
func (s *Service) Nodes(workspaceID uint, location string, admin bool) ([]Node, error) {
	_, servers, err := s.locationServers(workspaceID, location, admin)
	if err != nil {
		return nil, err
	}
	pinning := s.PinningAllowed(workspaceID, admin)
	out := []Node{}
	for _, srv := range servers {
		online := srv.IsLocal || (s.online != nil && s.online(srv.ID))
		out = append(out, Node{
			ID:          srv.ID,
			Name:        srv.Name,
			DisplayName: srv.Label(),
			IsLocal:     srv.IsLocal,
			Online:      online,
			Cordoned:    srv.Cordoned,
			Pinnable:    pinning && online && !srv.Cordoned,
			SwarmNodeID: srv.SwarmNodeID,
		})
	}
	return out, nil
}

// The servers a workspace sees in a location: the location's own, narrowed to the plan's pool.
func (s *Service) locationServers(workspaceID uint, location string, admin bool) (*models.Cluster, []*models.Server, error) {
	c, err := s.resolveCluster(workspaceID, location, admin)
	if err != nil {
		return nil, nil, err
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil, nil, err
	}
	policy, enforced := s.planPlacement(workspaceID)
	var out []*models.Server
	for i := range servers {
		srv := &servers[i]
		if s.resolveID(srv.ClusterID) != c.ID || (enforced && models.PoolOf(srv) != policy.Pool) {
			continue
		}
		out = append(out, srv)
	}
	return c, out, nil
}

// Only nodes the workspace can list are found, so a name outside its location or pool reads as unknown.
func (s *Service) pinNode(req Request, name string) (Result, error) {
	if !s.PinningAllowed(req.WorkspaceID, req.Admin) {
		return Result{}, ErrNodePinningOff
	}
	c, servers, err := s.locationServers(req.WorkspaceID, req.Location, req.Admin)
	if err != nil {
		return Result{}, err
	}
	for _, srv := range servers {
		if srv.Name != name {
			continue
		}
		if srv.Cordoned {
			return Result{}, fmt.Errorf("%w: %q", ErrNodeCordoned, name)
		}
		return Result{ClusterID: c.ID, ServerID: srv.ID}, nil
	}
	return Result{}, fmt.Errorf("%w: %q in %q", ErrNodeNotInLocation, name, c.Name)
}

// NodeConstraint returns "" for non-swarm nodes; server id 0 is the control plane.
func (s *Service) NodeConstraint(serverID uint) string {
	var srv *models.Server
	var err error
	if serverID == 0 {
		srv, err = s.servers.FindLocal()
	} else {
		srv, err = s.servers.FindByID(serverID)
	}
	if err != nil || srv.SwarmNodeID == "" {
		return ""
	}
	return "node.id==" + srv.SwarmNodeID
}

// Location is a cluster as a workspace sees it: no nodes, addresses or capacity.
type Location struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	LocationCode string `json:"location_code,omitempty"`
	// Default marks the location a create lands in when it names none.
	Default bool `json:"default"`
	// Swarm reports that the location runs a swarm, so an app there may run as a replicated service.
	Swarm bool `json:"swarm"`
}

// Locations lists the locations a workspace may place new resources in.
func (s *Service) Locations(workspaceID uint, admin bool) ([]Location, error) {
	list, err := s.clusters.List()
	if err != nil {
		return nil, err
	}
	fallback, _ := s.resolveCluster(workspaceID, "", admin)
	policy, enforced := s.planPlacement(workspaceID)
	acc := s.accessFor(workspaceID, admin)
	out := make([]Location, 0, len(list))
	for i := range list {
		c := &list[i]
		if !acc.allows(c) || c.Cordoned || !permits(policy, enforced, c.ID, acc.confined) {
			continue
		}
		out = append(out, Location{
			ID:           c.ID,
			Name:         c.Name,
			DisplayName:  c.Label(),
			LocationCode: c.LocationCode,
			Default:      fallback != nil && fallback.ID == c.ID,
			Swarm:        c.Mode == models.ClusterModeSwarm,
		})
	}
	return out, nil
}

// LocationSet is the locations a workspace may use, and whether the choice is still its own.
type LocationSet struct {
	Locations []Location `json:"locations"`
	// The UI shows the reason instead of a picker that would refuse every value but one.
	Pinned bool `json:"pinned"`
	// PinnedTo is the organization's label, named in that explanation.
	PinnedTo string `json:"pinned_to,omitempty"`
}

// LocationsFor is Locations plus whether the organization has taken the choice over.
func (s *Service) LocationsFor(workspaceID uint, admin bool) (LocationSet, error) {
	locs, err := s.Locations(workspaceID, admin)
	if err != nil {
		return LocationSet{}, err
	}
	set := LocationSet{Locations: locs}
	if acc := s.accessFor(workspaceID, admin); acc.confined {
		set.Pinned = true
		if s.orgs != nil {
			set.PinnedTo = s.orgs.OrganizationLabel(acc.orgID)
		}
	}
	return set, nil
}

// The default location follows the organization, so there is nothing for the workspace to set.
var ErrLocationPinned = errors.New("this workspace's organization runs its own locations; the default follows the organization")

// An empty name clears it.
func (s *Service) SetDefaultLocation(workspaceID uint, location string, admin bool) error {
	if s.accessFor(workspaceID, admin).confined {
		return ErrLocationPinned
	}
	name := strings.TrimSpace(location)
	if name == "" {
		return s.clusters.SetWorkspaceDefault(workspaceID, nil)
	}
	c, err := s.clusters.FindByName(name)
	if err != nil {
		return ErrLocationNotFound
	}
	policy, enforced := s.planPlacement(workspaceID)
	acc := s.accessFor(workspaceID, admin)
	if acc.hidden(c) {
		return ErrLocationNotFound
	}
	if !acc.allows(c) || !permits(policy, enforced, c.ID, acc.confined) {
		return ErrLocationNotAllowed
	}
	return s.clusters.SetWorkspaceDefault(workspaceID, &c.ID)
}
