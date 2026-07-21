// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dockerimport adopts pre-existing Docker resources (hand-run
// containers, compose stacks, volumes, networks) into Miabi without tearing
// down what is running. Discovery lists the node's *unmanaged* resources; import
// creates the matching domain records. The default mode is adopt-in-place: an
// app record plus a synthetic active Release pointing at the live container, so
// status/logs/stats/stop/restart work immediately and the container keeps
// running. A native deploy later recreates it under Miabi conventions.
package dockerimport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// Clients resolves a node's Docker client (local or remote agent).
type Clients interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
	// SelfContainerID is Miabi's own container on the node (the control plane
	// locally, the agent remotely), or "" when it cannot be determined. Discovery
	// uses it to find the Compose project the platform stack runs under — see
	// platformComposeProject.
	SelfContainerID(serverID uint) string
}

// Service implements discovery + import of pre-existing Docker resources.
type Service struct {
	clients      Clients
	apps         *application.Service
	stacks       *stack.Service
	appRepo      *repositories.ApplicationRepository
	releases     *repositories.ReleaseRepository
	deployments  *repositories.DeploymentRepository
	volumes      *repositories.VolumeRepository
	networks     *repositories.NetworkRepository
	stackRepo    *repositories.StackRepository
	portBindings *repositories.PortBindingRepository
	now          func() time.Time
}

func NewService(
	clients Clients,
	apps *application.Service,
	stacks *stack.Service,
	appRepo *repositories.ApplicationRepository,
	releases *repositories.ReleaseRepository,
	deployments *repositories.DeploymentRepository,
	volumes *repositories.VolumeRepository,
	networks *repositories.NetworkRepository,
	stackRepo *repositories.StackRepository,
	portBindings *repositories.PortBindingRepository,
) *Service {
	return &Service{
		clients: clients, apps: apps, stacks: stacks, appRepo: appRepo,
		releases: releases, deployments: deployments, volumes: volumes,
		networks: networks, stackRepo: stackRepo, portBindings: portBindings, now: time.Now,
	}
}

// systemNetworks are Docker's built-in networks, never importable.
var systemNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

// --- discovery DTOs ---

// Importable is a node's set of adoptable (unmanaged) resources.
type Importable struct {
	Containers []ImportableContainer `json:"containers"`
	Volumes    []ImportableVolume    `json:"volumes"`
	Networks   []ImportableNetwork   `json:"networks"`
}

// ImportableContainer is an unmanaged container enriched from inspect, with its
// compose grouping and volume/network relationships.
type ImportableContainer struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Image           string               `json:"image"`
	Tag             string               `json:"tag"`
	State           string               `json:"state"`
	Ports           []docker.PortMapping `json:"ports,omitempty"`
	EnvCount        int                  `json:"env_count"`
	SecretEnvKeys   []string             `json:"secret_env_keys,omitempty"`
	Volumes         []string             `json:"volumes,omitempty"`  // referenced Docker volume names
	Networks        []string             `json:"networks,omitempty"` // attached network names (non-system)
	RestartPolicy   string               `json:"restart_policy"`
	MemoryBytes     int64                `json:"memory_bytes"`
	NanoCPUs        int64                `json:"nano_cpus"`
	Command         []string             `json:"command,omitempty"`
	ComposeProject  string               `json:"compose_project,omitempty"`
	ComposeService  string               `json:"compose_service,omitempty"`
	SuggestedName   string               `json:"suggested_name"`
	AlreadyImported bool                 `json:"already_imported"`
}

// ImportableVolume is an unmanaged Docker volume and the unmanaged containers
// that use it.
type ImportableVolume struct {
	Name            string   `json:"name"`
	Driver          string   `json:"driver"`
	UsedBy          []string `json:"used_by,omitempty"`
	AlreadyImported bool     `json:"already_imported"`
}

// ImportableNetwork is an unmanaged Docker network and the unmanaged containers
// attached to it.
type ImportableNetwork struct {
	Name            string   `json:"name"`
	Driver          string   `json:"driver"`
	UsedBy          []string `json:"used_by,omitempty"`
	AlreadyImported bool     `json:"already_imported"`
}

// Discover lists the node's unmanaged containers/volumes/networks, enriched with
// inspect data, compose grouping, relationships, and already-imported flags.
func (s *Service) Discover(ctx context.Context, serverID uint) (*Importable, error) {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return nil, err
	}
	containers, err := dc.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	dockerVolumes, err := dc.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	dockerNetworks, err := dc.ListNetworks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}

	out := &Importable{}
	// volume name -> unmanaged containers using it; same for networks.
	volUsedBy := map[string][]string{}
	netUsedBy := map[string][]string{}

	// The Compose project Miabi itself runs under, so an UNLABELED stack (installed
	// before platform labels) is still shielded. Compose stamps the project on the
	// containers, volumes and networks alike, so this one lookup covers all three.
	project := s.platformComposeProject(ctx, serverID, dc)

	for i := range containers {
		c := containers[i]
		// Skip anything Miabi manages — and anything Miabi IS. Until platform labels
		// existed this checked only isManaged(), so the unlabeled compose stack
		// (miabi-postgres, miabi-redis, the control plane) was offered for import.
		if notImportable(c.Labels, containerName(c)) || inPlatformProject(c.Labels, project) {
			continue
		}
		cfg, ierr := dc.InspectContainerConfig(ctx, c.ID)
		if ierr != nil {
			continue // skip containers that vanished mid-scan
		}
		ic := s.toImportableContainer(cfg)
		for _, vn := range ic.Volumes {
			volUsedBy[vn] = append(volUsedBy[vn], ic.Name)
		}
		for _, nn := range ic.Networks {
			netUsedBy[nn] = append(netUsedBy[nn], ic.Name)
		}
		out.Containers = append(out.Containers, ic)
	}

	for i := range dockerVolumes {
		v := dockerVolumes[i]
		if notImportable(v.Labels, v.Name) || inPlatformProject(v.Labels, project) {
			continue
		}
		out.Volumes = append(out.Volumes, ImportableVolume{
			Name: v.Name, Driver: v.Driver, UsedBy: volUsedBy[v.Name],
			AlreadyImported: s.volumeImported(v.Name),
		})
	}

	for i := range dockerNetworks {
		n := dockerNetworks[i]
		if systemNetworks[n.Name] || notImportable(n.Labels, n.Name) || inPlatformProject(n.Labels, project) {
			continue
		}
		out.Networks = append(out.Networks, ImportableNetwork{
			Name: n.Name, Driver: n.Driver, UsedBy: netUsedBy[n.Name],
			AlreadyImported: s.networkImported(n.Name),
		})
	}
	return out, nil
}

func (s *Service) toImportableContainer(cfg docker.ContainerConfig) ImportableContainer {
	image, tag := splitImageTag(cfg.Image)
	ic := ImportableContainer{
		ID: cfg.ID, Name: cfg.Name, Image: image, Tag: tag, State: cfg.State,
		Ports: cfg.Ports, EnvCount: len(cfg.Env), RestartPolicy: cfg.RestartPolicy,
		MemoryBytes: cfg.MemoryBytes, NanoCPUs: cfg.NanoCPUs, Command: cfg.Command,
		ComposeProject:  cfg.Labels["com.docker.compose.project"],
		ComposeService:  cfg.Labels["com.docker.compose.service"],
		AlreadyImported: s.containerImported(cfg.ID),
	}
	for _, e := range cfg.Env {
		if k, _, ok := strings.Cut(e, "="); ok && isSecretKey(k) {
			ic.SecretEnvKeys = append(ic.SecretEnvKeys, k)
		}
	}
	for _, m := range cfg.Mounts {
		if m.Type == "volume" && m.Name != "" {
			ic.Volumes = append(ic.Volumes, m.Name)
		}
	}
	for _, nn := range cfg.Networks {
		if !systemNetworks[nn] && !isMiabiName(nn) {
			ic.Networks = append(ic.Networks, nn)
		}
	}
	ic.SuggestedName = firstNonEmpty(ic.ComposeService, sanitizeName(ic.Name))
	return ic
}

// --- import DTOs ---

// ImportMode selects how a container is adopted.
type ImportMode string

const (
	// ModeAdopt tracks the live container as-is (zero downtime); converge on the
	// next deploy.
	ModeAdopt ImportMode = "adopt"
	// ModeReconcile adopts, then immediately enqueues a native deploy that
	// recreates the container under Miabi conventions and removes the old one.
	ModeReconcile ImportMode = "reconcile"
)

// ImportRequest is a selection of resources to import into a workspace.
type ImportRequest struct {
	WorkspaceID uint
	// StackName is a fallback stack for container items that carry no per-item
	// StackName (e.g. ungrouped containers). Per-item StackName takes precedence,
	// so a compose project maps to its own stack.
	StackName string
	Items     []ImportItem
}

// ImportItem is one resource to import.
type ImportItem struct {
	Kind    string     // "container" | "volume" | "network"
	Ref     string     // container ID or volume/network name
	AppName string     // optional app name for containers
	Mode    ImportMode // containers only; default adopt
	// StackName groups this container's app under a Stack of that name (created or
	// reused). Typically the container's compose project, so each compose project
	// becomes its own stack. Empty = no stack (falls back to request StackName).
	StackName string
}

// ItemResult reports the outcome of importing one item.
type ItemResult struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Status  string `json:"status"` // imported | skipped | failed
	Message string `json:"message,omitempty"`
	AppID   uint   `json:"app_id,omitempty"`
}

// ImportResult is the aggregate outcome of an import batch.
type ImportResult struct {
	// StackIDs lists the IDs of stacks created or reused during the import
	// (compose project → stack), keyed by stack name.
	StackIDs map[string]uint `json:"stack_ids,omitempty"`
	Items    []ItemResult    `json:"items"`
}

const (
	statusImported = "imported"
	statusSkipped  = "skipped"
	statusFailed   = "failed"
)

// Import creates Miabi records for the selected resources. Networks and
// volumes are imported first so containers can attach them. Each item's outcome
// is reported independently — one failure never aborts the batch.
func (s *Service) Import(ctx context.Context, actorID, serverID uint, req ImportRequest) (*ImportResult, error) {
	if _, err := s.clients.For(serverID); err != nil {
		return nil, err
	}
	res := &ImportResult{}

	// resolveStack maps a stack name to a Stack id, creating it once per batch and
	// reusing an existing one of the same name. Each compose project resolves to
	// its own stack, so containers attach to the right one.
	stackCache := map[string]uint{}
	resolveStack := func(name string) (*uint, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil
		}
		if id, ok := stackCache[name]; ok {
			return &id, nil
		}
		if st, err := s.stackRepo.FindByName(req.WorkspaceID, name); err == nil {
			stackCache[name] = st.ID
			return &st.ID, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		st, err := s.stacks.Create(ctx, req.WorkspaceID, stack.Input{Name: name, Description: "Imported from existing Docker resources"})
		if err != nil {
			return nil, fmt.Errorf("create stack %q: %w", name, err)
		}
		stackCache[name] = st.ID
		return &st.ID, nil
	}

	// Process networks, then volumes, then containers, so a container's
	// dependencies already have records when it attaches them.
	order := map[string]int{"network": 0, "volume": 1, "container": 2}
	items := append([]ImportItem(nil), req.Items...)
	sort.SliceStable(items, func(i, j int) bool { return order[items[i].Kind] < order[items[j].Kind] })

	for _, it := range items {
		switch it.Kind {
		case "network":
			res.Items = append(res.Items, s.importNetwork(ctx, req.WorkspaceID, serverID, it.Ref))
		case "volume":
			res.Items = append(res.Items, s.importVolume(ctx, req.WorkspaceID, serverID, it.Ref))
		case "container":
			stackID, serr := resolveStack(firstNonEmpty(it.StackName, req.StackName))
			if serr != nil {
				res.Items = append(res.Items, ItemResult{Kind: it.Kind, Ref: it.Ref, Status: statusFailed, Message: serr.Error()})
				continue
			}
			res.Items = append(res.Items, s.importContainer(ctx, actorID, req.WorkspaceID, serverID, stackID, it))
		default:
			res.Items = append(res.Items, ItemResult{Kind: it.Kind, Ref: it.Ref, Status: statusFailed, Message: "unknown kind"})
		}
	}
	if len(stackCache) > 0 {
		res.StackIDs = stackCache
	}
	return res, nil
}

func (s *Service) importVolume(ctx context.Context, wsID, serverID uint, name string) ItemResult {
	r := ItemResult{Kind: "volume", Ref: name}
	if _, err := s.ensureVolume(ctx, wsID, serverID, name); err != nil {
		if errors.Is(err, errAlreadyImported) {
			r.Status, r.Message = statusSkipped, "already imported"
			return r
		}
		r.Status, r.Message = statusFailed, err.Error()
		return r
	}
	r.Status = statusImported
	return r
}

func (s *Service) importNetwork(ctx context.Context, wsID, serverID uint, name string) ItemResult {
	r := ItemResult{Kind: "network", Ref: name}
	dc, err := s.clients.For(serverID)
	if err != nil {
		r.Status, r.Message = statusFailed, err.Error()
		return r
	}
	driver := "bridge"
	if nets, lerr := dc.ListNetworks(ctx); lerr == nil {
		for _, n := range nets {
			if n.Name == name && n.Driver != "" {
				driver = n.Driver
			}
		}
	}
	if _, err := s.ensureNetwork(wsID, name, driver); err != nil {
		if errors.Is(err, errAlreadyImported) {
			r.Status, r.Message = statusSkipped, "already imported"
			return r
		}
		r.Status, r.Message = statusFailed, err.Error()
		return r
	}
	r.Status = statusImported
	return r
}

func (s *Service) importContainer(ctx context.Context, actorID, wsID, serverID uint, stackID *uint, it ImportItem) ItemResult {
	r := ItemResult{Kind: "container", Ref: it.Ref}
	dc, err := s.clients.For(serverID)
	if err != nil {
		r.Status, r.Message = statusFailed, err.Error()
		return r
	}
	cfg, err := dc.InspectContainerConfig(ctx, it.Ref)
	if err != nil {
		r.Status, r.Message = statusFailed, "inspect: "+err.Error()
		return r
	}
	// Discovery already filters these out, but Import takes container refs straight
	// from the request — so re-check here rather than trust the caller's list. A
	// stale page, or a hand-made request, must not be able to import the platform's
	// own database as an application.
	if notImportable(cfg.Labels, strings.TrimPrefix(cfg.Name, "/")) ||
		inPlatformProject(cfg.Labels, s.platformComposeProject(ctx, serverID, dc)) {
		r.Status, r.Message = statusFailed, "this container is part of the Miabi platform and cannot be imported"
		return r
	}
	if s.containerImported(cfg.ID) {
		r.Status, r.Message = statusSkipped, "already imported"
		return r
	}

	name := firstNonEmpty(strings.TrimSpace(it.AppName), cfg.Labels["com.docker.compose.service"], sanitizeName(cfg.Name))

	// Container port declarations (deduped) + the primary port.
	var specs []application.PortSpec
	seenPort := map[string]bool{}
	for _, p := range cfg.Ports {
		key := fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol)
		if p.ContainerPort <= 0 || seenPort[key] {
			continue
		}
		seenPort[key] = true
		specs = append(specs, application.PortSpec{ContainerPort: p.ContainerPort, Protocol: p.Protocol})
	}

	// Adopt referenced networks (best-effort) and collect their record IDs.
	var networkIDs []uint
	for _, nn := range cfg.Networks {
		if systemNetworks[nn] || isMiabiName(nn) {
			continue
		}
		net, nerr := s.ensureNetwork(wsID, nn, "bridge")
		if nerr != nil && !errors.Is(nerr, errAlreadyImported) {
			continue
		}
		if net == nil {
			net, _ = s.networks.FindByDockerName(nn)
		}
		if net != nil && net.WorkspaceID == wsID {
			networkIDs = append(networkIDs, net.ID)
		}
	}

	app, err := s.apps.Create(wsID, application.CreateInput{
		DisplayName:   name,
		Handle:        name,
		ServerID:      serverID,
		SourceType:    models.AppSourceImage,
		Image:         cfg.Image,
		Command:       cfg.Command,
		MemoryBytes:   cfg.MemoryBytes,
		NanoCPUs:      cfg.NanoCPUs,
		RestartPolicy: models.RestartPolicy(cfg.RestartPolicy),
		Ports:         specs,
		NetworkIDs:    networkIDs,
		StackID:       stackID,
		// Carry over the source container's custom labels (Traefik, Watchtower,
		// autoheal, …). Create sanitizes them — the platform (io.miabi.*/miabi.*)
		// and Compose (com.docker.*) keys are stripped, so only genuine user
		// labels survive onto the imported app.
		ContainerLabels: cfg.Labels,
	})
	if err != nil {
		r.Status, r.Message = statusFailed, "create app: "+err.Error()
		return r
	}
	app.Imported = true
	if err := s.appRepo.Update(app); err != nil {
		r.Status, r.Message = statusFailed, err.Error()
		return r
	}

	// Env: import the effective env as plain app vars (secret-looking keys are
	// surfaced at discovery).
	for _, e := range cfg.Env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			continue
		}
		_ = s.apps.SetEnvVar(app.ID, k, v, false)
	}

	// Volumes: ensure a record exists for each referenced volume and attach it.
	for _, m := range cfg.Mounts {
		if m.Type != "volume" || m.Name == "" {
			continue
		}
		vol, verr := s.ensureVolume(ctx, wsID, serverID, m.Name)
		if vol == nil && errors.Is(verr, errAlreadyImported) {
			vol, _ = s.volumes.FindByDockerName(m.Name)
		}
		if vol != nil && vol.WorkspaceID == wsID {
			_ = s.apps.AttachVolume(app, vol.ID, m.Destination)
		}
	}

	// Published host ports already serve traffic, so record them as approved
	// PortBindings (not pending) to reflect reality.
	for _, p := range cfg.Ports {
		if p.HostPort <= 0 {
			continue
		}
		reviewer := actorID
		_ = s.portBindings.Create(&models.PortBinding{
			WorkspaceID: wsID, ApplicationID: app.ID,
			ContainerPort: p.ContainerPort, Protocol: p.Protocol, HostPort: p.HostPort,
			Status: models.PortBindingApproved, RequestedBy: actorID, ReviewedBy: &reviewer,
			ReviewNote: "imported (already published)",
		})
	}

	// Synthetic succeeded deployment + active adopted release pointing at the live
	// container, so status/logs/stats/stop/restart resolve to it immediately.
	now := s.now()
	image := app.ImageRef("")
	dep := &models.Deployment{
		ApplicationID: app.ID, Status: models.DeploymentSucceeded, Image: image,
		Trigger: "import", Strategy: models.DeployRolling, ContainerID: cfg.ID,
		StartedAt: &now, FinishedAt: &now,
	}
	if err := s.deployments.Create(dep); err != nil {
		r.Status, r.Message = statusFailed, "record deployment: "+err.Error()
		r.AppID = app.ID
		return r
	}
	version, _ := s.releases.NextVersion(app.ID)
	rel := &models.Release{
		ApplicationID: app.ID, DeploymentID: dep.ID, Version: version, Image: image,
		ContainerID: cfg.ID, Active: true, Adopted: true,
	}
	if err := s.releases.Create(rel); err != nil {
		r.Status, r.Message = statusFailed, "record release: "+err.Error()
		r.AppID = app.ID
		return r
	}
	if err := s.appRepo.SetCurrentRelease(app.ID, rel.ID, models.AppStatusRunning); err != nil {
		r.Status, r.Message = statusFailed, err.Error()
		r.AppID = app.ID
		return r
	}

	r.Status, r.AppID = statusImported, app.ID
	r.Message = "adopted (running)"

	// Reconcile-now: enqueue a native deploy that retires the adopted container
	// (the active release) and replaces it under Miabi conventions.
	if it.Mode == ModeReconcile {
		if _, derr := s.apps.Deploy(app, nil, "", models.DeployRolling); derr != nil {
			r.Message = "adopted; reconcile deploy failed to enqueue: " + derr.Error()
		} else {
			r.Message = "adopted; reconcile deploy enqueued"
		}
	}
	return r
}

// errAlreadyImported signals an idempotent skip (a record already maps the
// resource); callers translate it to a "skipped" result.
var errAlreadyImported = errors.New("already imported")

// ensureVolume returns the Volume record for an existing Docker volume, creating
// it (Imported=true) if absent. Returns errAlreadyImported when a record already
// exists, with a nil volume (callers look it up if they need it).
func (s *Service) ensureVolume(ctx context.Context, wsID, serverID uint, dockerName string) (*models.Volume, error) {
	if existing, err := s.volumes.FindByDockerName(dockerName); err == nil {
		_ = existing
		return nil, errAlreadyImported
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	volName, err := slug.Unique(dockerName, "vol", func(c string) (bool, error) {
		return s.volumes.ExistsByName(wsID, c)
	})
	if err != nil {
		return nil, err
	}
	var mountpoint string
	if dc, derr := s.clients.For(serverID); derr == nil {
		if dv, ierr := dc.InspectVolume(ctx, dockerName); ierr == nil {
			mountpoint = dv.Mountpoint
		}
	}
	v := &models.Volume{
		WorkspaceID: wsID, Name: volName, DisplayName: dockerName, ServerID: serverID,
		DockerName: dockerName, Mountpoint: mountpoint, Imported: true,
	}
	if err := s.volumes.Create(v); err != nil {
		return nil, err
	}
	return v, nil
}

// ensureNetwork returns the Network record for an existing Docker network,
// creating it (Imported=true) if absent. Returns errAlreadyImported when a
// record already exists.
func (s *Service) ensureNetwork(wsID uint, dockerName, driver string) (*models.Network, error) {
	if _, err := s.networks.FindByDockerName(dockerName); err == nil {
		return nil, errAlreadyImported
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name, err := slug.Unique(dockerName, "net", func(c string) (bool, error) {
		return s.networks.ExistsByName(wsID, c)
	})
	if err != nil {
		return nil, err
	}
	if driver == "" {
		driver = "bridge"
	}
	n := &models.Network{
		WorkspaceID: wsID, Name: name, DockerName: dockerName, Driver: driver, Imported: true,
	}
	if err := s.networks.Create(n); err != nil {
		return nil, err
	}
	return n, nil
}

// --- idempotency helpers ---

func (s *Service) containerImported(containerID string) bool {
	_, err := s.releases.FindByContainerID(containerID)
	return err == nil
}

func (s *Service) volumeImported(dockerName string) bool {
	_, err := s.volumes.FindByDockerName(dockerName)
	return err == nil
}

func (s *Service) networkImported(dockerName string) bool {
	_, err := s.networks.FindByDockerName(dockerName)
	return err == nil
}

// --- pure helpers ---

// isManaged reports whether a resource carries any platform label (io.miabi.* or
// legacy miabi.*).
func isManaged(labels map[string]string) bool {
	return docker.IsManaged(labels)
}

// notImportable reports whether a Docker resource must never be offered for
// import: it is already managed by Miabi, or it IS Miabi.
//
// The second half is the one that bites. Miabi's own stack (control plane,
// Postgres, Redis, gateway) is deployed by compose — from outside Miabi — so
// before platform labels existed it carried no io.miabi.* key at all, isManaged()
// was false for it, and discovery happily offered `miabi-postgres` as an importable
// application. Importing it creates an Application record pointing at the
// platform's own database, which the deploy worker then believes it owns and may
// recreate.
//
// name is checked too, not just labels: a stack deployed before this change has no
// labels to read, and it must not become importable merely because it is old.
func notImportable(labels map[string]string, name string) bool {
	return isManaged(labels) || docker.IsPlatformStack(labels) || isMiabiName(name)
}

// composeProjectLabel is the key Compose stamps on every container, volume and
// network it creates.
const composeProjectLabel = "com.docker.compose.project"

// platformComposeProject returns the Compose project Miabi's own stack runs under
// on this node, or "" when it cannot be determined.
//
// This is the shield for a stack deployed BEFORE platform labels existed, and it
// cannot be done by name. Compose only pins container_name on the gateway; the rest
// get generated names of the form <project>-<service>-<n> — the platform's Postgres
// is really "miabi-miabi-postgres-1", not "miabi-postgres" — and the project is the
// install directory, so it is not knowable up front.
//
// But Miabi always knows its OWN container, and every sibling in the stack carries
// the same com.docker.compose.project. So: ask the control plane which project it is
// in, and everything in that project is the Miabi stack.
//
// Remotely, self is the agent (started by `docker run`, no Compose project), so this
// yields "" and the label/name shields carry the load — which is correct, since the
// platform stack does not run on a worker node.
func (s *Service) platformComposeProject(ctx context.Context, serverID uint, dc docker.Client) string {
	self := s.clients.SelfContainerID(serverID)
	if self == "" {
		return ""
	}
	cfg, err := dc.InspectContainerConfig(ctx, self)
	if err != nil {
		return ""
	}
	return cfg.Labels[composeProjectLabel]
}

// inPlatformProject reports whether a resource belongs to the Compose project the
// Miabi stack itself runs under. project == "" disables the check.
func inPlatformProject(labels map[string]string, project string) bool {
	if project == "" {
		return false
	}
	return labels[composeProjectLabel] == project
}

// containerName returns a container's primary name without Docker's leading "/",
// or "" when it has none.
func containerName(c docker.Container) string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// isMiabiName reports whether a container/volume/network name follows a Miabi
// naming convention, so the platform's own resources are not offered for import
// even when unlabeled (a stack installed before platform labels).
//
// Compose prefixes volume and network names with the project name, so the platform
// volumes surface as e.g. "miabi_pgdata" — hence the miabi_ prefix as well as the
// bare names compose pins via container_name.
func isMiabiName(name string) bool {
	name = strings.TrimPrefix(name, "/") // container names arrive as "/miabi-postgres"
	switch name {
	case "miabi", "miabi-postgres", "miabi-redis", "miabi-gateway", "miabi-agent":
		return true
	}
	return strings.HasPrefix(name, "mb-") || strings.HasPrefix(name, "miabi_")
}

// isSecretKey flags env keys that look sensitive, for the discovery preview.
func isSecretKey(key string) bool {
	k := strings.ToUpper(key)
	for _, suf := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "APIKEY", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY"} {
		if strings.Contains(k, suf) {
			return true
		}
	}
	return strings.HasSuffix(k, "_KEY") || k == "KEY"
}

// splitImageTag splits "repo:tag" into ("repo", "tag"), leaving digests and
// untagged refs with an empty tag. A ":" after the last "/" is the tag.
func splitImageTag(ref string) (string, string) {
	if strings.Contains(ref, "@") { // digest-pinned
		return ref, ""
	}
	slashIdx := strings.LastIndex(ref, "/")
	colonIdx := strings.LastIndex(ref, ":")
	if colonIdx > slashIdx && colonIdx != -1 {
		return ref[:colonIdx], ref[colonIdx+1:]
	}
	return ref, ""
}

// sanitizeName turns a raw container name into a friendly app name.
func sanitizeName(name string) string {
	return strings.TrimSpace(strings.TrimPrefix(name, "/"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
