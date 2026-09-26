// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package placement decides where a new resource lands: a location (cluster) the workspace may use, then a
// node inside it. It runs once, at create; nothing is rescheduled afterwards.
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
)

// Online reports whether a remote node's agent is connected. Satisfied by nodes.Clients.Connected.
type Online func(serverID uint) bool

// Service places new resources.
type Service struct {
	clusters *repositories.ClusterRepository
	servers  *repositories.ServerRepository
	online   Online
	policy   Policy
	orgs     Orgs
}

func NewService(clusters *repositories.ClusterRepository, servers *repositories.ServerRepository, online Online) *Service {
	return &Service{clusters: clusters, servers: servers, online: online}
}

// Policy resolves the locations and node pool a workspace's plan binds it to; false means no policy applies.
// Satisfied by the quota service.
type Policy interface {
	EffectivePlacement(workspaceID uint) (models.PlanPlacement, bool)
}

// Orgs resolves the organization a workspace belongs to (0 when unknown) and whether that
// organization owns clusters of its own. Satisfied by the organization wiring in routes.
type Orgs interface {
	OrganizationOfWorkspace(workspaceID uint) uint
	OwnsClusters(orgID uint) bool
	// OrganizationLabel names the organization for the explanation shown in place of a picker the
	// workspace no longer controls. Empty when it cannot be read.
	OrganizationLabel(orgID uint) string
}

// access is which clusters a create may land in. Two different things decide it:
//
//   - Visibility is about the CALLER. A restricted cluster is for platform admins, and an admin
//     pinning a node is exercising their own privilege.
//   - Organization ownership is about the WORKSPACE. The resource belongs to the workspace, so a
//     dedicated tenant's workloads stay on their own hardware no matter who clicked deploy — an
//     admin included. Nothing else would be worth calling dedicated.
//
// An organization that owns clusters is therefore CONFINED to them, and that confinement binds
// every caller.
type access struct {
	admin    bool
	orgID    uint
	confined bool
}

// hidden reports that the caller should not learn the cluster exists at all: it belongs to a DIFFERENT
// organization, or it is restricted to platform admins. A platform admin is excepted: they can already
// list every cluster.
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
	// Ownership is checked before the admin bypass on purpose: it binds the workspace, not the
	// caller. An admin who needs a workspace somewhere else moves the cluster or the organization.
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

// accessFor resolves the access for a create in workspaceID. The organization is resolved even for
// an admin, because confinement follows the workspace.
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

// permits reports whether the plan's location policy allows this cluster.
//
// A confined organization is exempt. The plan's location list is a commercial statement about which
// SHARED locations a tier may use; a tenant running on its own hardware has left that question
// behind, and applying both would intersect to nothing — a plan pinned to location A and an
// organization owning location B leaves no cluster at all, and every create fails with a message
// naming neither. The plan's pool still applies: that is about node class, not geography.
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
	// Colocate is a node the platform itself requires, such as the node of a database being reused.
	Colocate uint
	Admin    bool
	// Service marks a replicated service app: Swarm schedules it, so its node is only the manager.
	Service bool
}

// Result is where the resource lands.
type Result struct {
	ClusterID uint `json:"cluster_id"`
	ServerID  uint `json:"server_id"`
}

// Place resolves the cluster and node a new resource lands on.
func (s *Service) Place(req Request) (Result, error) {
	if req.ServerID != 0 && !req.Admin {
		return Result{}, ErrNodePinAdminOnly
	}
	if (req.ServerID != 0 || req.Colocate != 0) && strings.TrimSpace(req.Location) != "" {
		// The named location is checked before the node, or a mismatch would confirm that a hidden
		// location exists.
		if c, err := s.clusters.FindByName(strings.TrimSpace(req.Location)); err == nil && s.accessFor(req.WorkspaceID, req.Admin).hidden(c) {
			return Result{}, ErrLocationNotFound
		}
	}
	if req.ServerID != 0 {
		res, err := s.onNode(req.ServerID, req.Location, true)
		if err != nil {
			return Result{}, err
		}
		// An admin may pin a node, but not into another organization's cluster: the resource would
		// belong to a workspace that cannot otherwise reach it.
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
		// Colocation follows a resource that is already placed, and deliberately reaches restricted
		// clusters the tenant could not pick itself. It must still not cross into another
		// organization's dedicated cluster, which a workspace reassigned after its first resource
		// landed would otherwise do.
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
	serverID, err := s.pickNode(c, req.Service, policy.Pool, enforced)
	if err != nil {
		return Result{}, err
	}
	return Result{ClusterID: c.ID, ServerID: serverID}, nil
}

// sameOrg refuses a placement that would put a workspace's resource in another organization's
// dedicated cluster. It applies to admins too — see the access doc comment.
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

// onNode places on a given node. A pin is a new choice and refuses a cordoned node, as pickNode does;
// colocation follows a resource already there, so it does not.
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

// ResolveLocation is the cluster a create naming location lands in: that location, else the workspace
// default, else the first location the workspace may use.
func (s *Service) ResolveLocation(workspaceID uint, location string, admin bool) (*models.Cluster, error) {
	return s.resolveCluster(workspaceID, location, admin)
}

// resolveCluster picks the named location, else the workspace's default, else the first location the
// workspace may use (the default cluster comes first).
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
		// A location dedicated to another organization, or restricted to admins, is not refused but hidden:
		// answering "forbidden" tells a stranger the name is real, which is how a tenant list gets enumerated.
		// Being confined to one's OWN locations is different — that is the caller's own arrangement,
		// and saying so is help rather than disclosure.
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
		// "no location is available" is true but unhelpful here: the tenant HAS locations and every
		// one of them is cordoned or empty. Name the organization so the operator looks at its
		// clusters rather than at the workspace's plan.
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

// pickNode chooses the node inside a cluster: its only node when standalone, the manager for a service,
// and otherwise the online, uncordoned node with the least container memory already placed on it. A
// cordoned node is refused wherever it would host the workload. With a pooled plan only nodes in the
// plan's pool count, and a cluster with none of them refuses the create.
func (s *Service) pickNode(c *models.Cluster, service bool, pool string, pooled bool) (uint, error) {
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
	for i := range servers {
		srv := &servers[i]
		if srv.ClusterID != c.ID || srv.Cordoned || (!srv.IsLocal && (s.online == nil || !s.online(srv.ID))) {
			continue
		}
		if pooled && models.PoolOf(srv) != pool {
			continue
		}
		load := loads[srv.ID]
		if best == 0 || load.Less(bestLoad) {
			best, bestLoad = srv.ID, load
		}
	}
	if best == 0 && pooled {
		return 0, poolUnavailable(pool)
	}
	if best == 0 {
		return 0, ErrNoSchedulableNode
	}
	return best, nil
}

// cordoned reports whether a node is positively known to be cordoned. A node that cannot be read is
// not treated as cordoned: refusing on a lookup error would turn a transient database fault into a
// failed create.
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

// PoolConstraints are the Swarm constraints that keep a workspace's services in its plan's pool: that pool's
// label, or for a plan without a pool, every pool present in the cluster excluded. Nil when no policy applies.
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
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	IsLocal  bool   `json:"is_local"`
	Online   bool   `json:"online"`
	Cordoned bool   `json:"cordoned"`
	// SwarmNodeID is what a service pin names: Swarm schedules a service, so pinning one is a
	// `node.id==` constraint rather than a server id.
	SwarmNodeID string `json:"swarm_node_id,omitempty"`
}

// Nodes lists the nodes of the location a create naming location would land in, resolved exactly as
// Place resolves it, so a location the workspace cannot use is refused the same way. With a pooled plan
// only the plan's pool is listed.
func (s *Service) Nodes(workspaceID uint, location string, admin bool) ([]Node, error) {
	c, err := s.resolveCluster(workspaceID, location, admin)
	if err != nil {
		return nil, err
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil, err
	}
	policy, enforced := s.planPlacement(workspaceID)
	out := []Node{}
	for i := range servers {
		srv := &servers[i]
		if s.resolveID(srv.ClusterID) != c.ID || (enforced && models.PoolOf(srv) != policy.Pool) {
			continue
		}
		out = append(out, Node{
			ID:          srv.ID,
			Name:        srv.Label(),
			IsLocal:     srv.IsLocal,
			Online:      srv.IsLocal || (s.online != nil && s.online(srv.ID)),
			Cordoned:    srv.Cordoned,
			SwarmNodeID: srv.SwarmNodeID,
		})
	}
	return out, nil
}

// NodeConstraint is the Swarm constraint pinning a service to a node, or "" when the node is not a swarm
// member. Server id 0 is the control-plane node.
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
	// Pinned reports that the workspace's organization runs its own clusters, so the set is fixed
	// and the default is not the workspace's to choose. The UI shows the reason rather than a
	// picker that would refuse every value but one.
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

// ErrLocationPinned is returned when a workspace's organization runs its own clusters: the default
// location follows the organization, so there is nothing for the workspace to set.
var ErrLocationPinned = errors.New("this workspace's organization runs its own locations; the default follows the organization")

// SetDefaultLocation sets the location a workspace's new resources land in when a create names none.
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
