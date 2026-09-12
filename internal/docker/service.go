// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// ServiceSpec describes a replicated Swarm service to create or update — the cluster-mode
// analogue of RunSpec: same image/env/mounts/limits, plus replicas, placement constraints and
// rolling-update tuning. Swarm schedules the tasks and load-balances a virtual IP across them.
type ServiceSpec struct {
	Name     string
	Image    string
	Env      []string // KEY=VALUE
	Cmd      []string // command args (ContainerSpec.Args); nil keeps the image default
	Replicas uint64   // 0 is treated as 1 (ignored when Global)
	// Global runs exactly one task on EVERY node the constraints allow, including
	// nodes that join later. It is how a per-node daemon is deployed to a swarm
	// without touching each host: the scheduler ships it, and keeps shipping it.
	Global         bool
	Networks       []string          // swarm-scoped (overlay) network names to attach
	NetworkAliases []string          // DNS aliases applied on each attached network
	Mounts         map[string]string // volume name -> container path
	// MountDrivers carries the volume driver config for a mount, keyed by source name. REQUIRED for
	// shared (nfs/cifs) volumes on a replicated service: without it a task on a node lacking the
	// volume makes an empty *local* one instead, so the "shared" storage silently diverges per node.
	MountDrivers map[string]ServiceMountDriver
	// Binds are host-path bind mounts for operator-managed storage present at the SAME path on every
	// node (a host-path volume under /mnt/*). Unlike Mounts they need no driver config — the path is
	// assumed present on each node the scheduler places a task on.
	Binds []ServiceBind
	// Configs are docker config objects attached as files. Swarm distributes them
	// over raft, so a task lands with its files on whichever node it schedules to.
	Configs     []ServiceConfig
	Labels      map[string]string
	MemoryBytes int64
	NanoCPUs    int64
	// Constraints are Swarm placement constraints, e.g. "node.role==worker" or
	// "node.id==abc" (pin to a node).
	Constraints []string
	Healthcheck *HealthcheckSpec
	User        string
	CapAdd      []string
	CapDrop     []string
	// NoNewPrivileges and ReadOnlyRootfs harden each task the way RunSpec hardens a container.
	NoNewPrivileges bool
	ReadOnlyRootfs  bool
	// Rolling update tuning (0 = Swarm defaults).
	UpdateParallelism uint64
	UpdateDelay       time.Duration
	// RegistryAuth authenticates the swarm to a private registry and is distributed to worker nodes
	// so their tasks can pull too (the daemon equivalent of --with-registry-auth). Required for the
	// built-in registry and any private one — without it a worker task fails to pull. nil if public.
	RegistryAuth *RegistryAuth
	// IngressNetwork is an additional attachable overlay the service joins with only IngressAlias
	// registered on it. Kept separate from Networks so tenant-scoped east-west aliases are NOT
	// registered on this shared network, where they would collide across workspaces. Empty disables it.
	IngressNetwork string
	IngressAlias   string
	// EndpointMode is "vip" (the default: one IPVS-balanced virtual IP) or "dnsrr" (the name resolves to the task
	// addresses, for hosts that cannot program IPVS).
	EndpointMode string
}

// ServiceBind is a host-path bind mount for a swarm service task (mount.TypeBind).
type ServiceBind struct {
	Source   string // host path (present on every node)
	Target   string // container path
	ReadOnly bool
}

// ServiceMountDriver is the Docker volume driver config for a service mount, so every node a task
// lands on materializes the SAME backing volume rather than an empty local one. Name is the
// driver ("local" for Miabi's nfs/cifs); Options are the mount options (type, device, o, ...).
type ServiceMountDriver struct {
	Name    string
	Options map[string]string
}

// ServiceStatus summarizes a Swarm service: its identity, desired replica count,
// and how many tasks are currently running (used to gate a deploy on
// convergence).
type ServiceStatus struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Image        string `json:"image,omitempty"`
	Replicas     uint64 `json:"replicas"`      // desired
	RunningTasks uint64 `json:"running_tasks"` // currently running
	// Placement maps a swarm node id -> the count of this service's tasks running
	// on that node. Populated by ServiceInspect so callers can show where the
	// scheduler actually placed the replicas (nil until inspected).
	Placement map[string]int `json:"placement,omitempty"`
	// StartedAt (RFC3339) is when the service's longest-running task entered the running state. It
	// comes from the swarm control plane, so it is known even for a task on a node Miabi has no
	// Docker client for, where inspecting the container is impossible.
	StartedAt string `json:"started_at,omitempty"`
}

// buildSwarmServiceSpec maps a Miabi ServiceSpec to the Docker SDK spec.
// ServiceConfig attaches one docker config object at a path inside the task.
type ServiceConfig struct {
	ID   string
	Name string
	Path string
	Mode uint32
}

func buildSwarmServiceSpec(spec ServiceSpec) swarm.ServiceSpec {
	replicas := spec.Replicas
	if replicas == 0 {
		replicas = 1
	}
	labels := spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedLabel] = "true"

	cspec := &swarm.ContainerSpec{
		Image:          spec.Image,
		Env:            spec.Env,
		Args:           spec.Cmd,
		User:           spec.User,
		Labels:         labels,
		CapabilityAdd:  spec.CapAdd,
		CapabilityDrop: spec.CapDrop,
		ReadOnly:       spec.ReadOnlyRootfs,
	}
	if spec.NoNewPrivileges {
		cspec.Privileges = &swarm.Privileges{NoNewPrivileges: true}
	}
	for vol, path := range spec.Mounts {
		m := mount.Mount{Type: mount.TypeVolume, Source: vol, Target: path}
		if d, ok := spec.MountDrivers[vol]; ok && (d.Name != "" || len(d.Options) > 0) {
			m.VolumeOptions = &mount.VolumeOptions{DriverConfig: &mount.Driver{Name: d.Name, Options: d.Options}}
		}
		cspec.Mounts = append(cspec.Mounts, m)
	}
	for _, b := range spec.Binds {
		cspec.Mounts = append(cspec.Mounts, mount.Mount{Type: mount.TypeBind, Source: b.Source, Target: b.Target, ReadOnly: b.ReadOnly})
	}
	for _, c := range spec.Configs {
		mode := os.FileMode(c.Mode)
		if mode == 0 {
			mode = 0o644
		}
		cspec.Configs = append(cspec.Configs, &swarm.ConfigReference{
			ConfigID:   c.ID,
			ConfigName: c.Name,
			File: &swarm.ConfigReferenceFileTarget{
				Name: c.Path,
				UID:  "0",
				GID:  "0",
				Mode: mode,
			},
		})
	}
	if hc := spec.Healthcheck; hc != nil && len(hc.Test) > 0 {
		cspec.Healthcheck = &container.HealthConfig{
			Test:        hc.Test,
			Interval:    hc.Interval,
			Timeout:     hc.Timeout,
			Retries:     hc.Retries,
			StartPeriod: hc.StartPeriod,
		}
	}

	task := swarm.TaskSpec{
		ContainerSpec: cspec,
		RestartPolicy: &swarm.RestartPolicy{Condition: swarm.RestartPolicyConditionAny},
	}
	if spec.MemoryBytes > 0 || spec.NanoCPUs > 0 {
		// Reserving the limit is what makes Swarm refuse to overcommit a node.
		task.Resources = &swarm.ResourceRequirements{
			Limits:       &swarm.Limit{MemoryBytes: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs},
			Reservations: &swarm.Resources{MemoryBytes: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs},
		}
	}
	if len(spec.Constraints) > 0 {
		task.Placement = &swarm.Placement{Constraints: spec.Constraints}
	}
	for _, n := range spec.Networks {
		task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{Target: n, Aliases: spec.NetworkAliases})
	}
	// The shared ingress overlay carries only the unique upstream alias, never the
	// tenant-scoped aliases, so app names can't collide across workspaces on it.
	if spec.IngressNetwork != "" {
		var aliases []string
		if spec.IngressAlias != "" {
			aliases = []string{spec.IngressAlias}
		}
		task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{Target: spec.IngressNetwork, Aliases: aliases})
	}

	// Global mode has no replica count: one task per eligible node, forever.
	mode := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}
	if spec.Global {
		mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	}

	s := swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: spec.Name, Labels: labels},
		TaskTemplate: task,
		Mode:         mode,
		EndpointSpec: &swarm.EndpointSpec{Mode: endpointMode(spec.EndpointMode)},
	}
	if spec.UpdateParallelism > 0 || spec.UpdateDelay > 0 {
		s.UpdateConfig = &swarm.UpdateConfig{Parallelism: spec.UpdateParallelism, Delay: spec.UpdateDelay}
	}
	return s
}

// ServiceCreate creates a replicated Swarm service and returns its id.
func (e *engineClient) ServiceCreate(ctx context.Context, spec ServiceSpec) (string, error) {
	opts := client.ServiceCreateOptions{Spec: buildSwarmServiceSpec(spec)}
	enc, err := encodeRegistryAuth(spec.RegistryAuth)
	if err != nil {
		return "", fmt.Errorf("encode registry auth: %w", err)
	}
	if enc != "" {
		opts.EncodedRegistryAuth = enc
		opts.QueryRegistry = true
	}
	resp, err := e.cli.ServiceCreate(ctx, opts)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ServiceUpdate updates an existing service in place; Swarm performs a rolling
// replacement of its tasks.
func (e *engineClient) ServiceUpdate(ctx context.Context, idOrName string, spec ServiceSpec) error {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	cur := res.Service
	opts := client.ServiceUpdateOptions{Version: cur.Version, Spec: buildSwarmServiceSpec(spec)}
	enc, err := encodeRegistryAuth(spec.RegistryAuth)
	if err != nil {
		return fmt.Errorf("encode registry auth: %w", err)
	}
	if enc != "" {
		opts.EncodedRegistryAuth = enc
		opts.RegistryAuthFrom = swarm.RegistryAuthFromSpec
	}
	_, err = e.cli.ServiceUpdate(ctx, cur.ID, opts)
	return err
}

// ServiceScale sets a replicated service's desired replica count.
func (e *engineClient) ServiceScale(ctx context.Context, idOrName string, replicas uint64) error {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	cur := res.Service
	if cur.Spec.Mode.Replicated == nil {
		return fmt.Errorf("service %s is not replicated", idOrName)
	}
	cur.Spec.Mode.Replicated.Replicas = &replicas
	_, err = e.cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{Version: cur.Version, Spec: cur.Spec})
	return err
}

// ServiceSetEndpointMode switches how a service is reached by name ("vip" or "dnsrr") in place; its tasks keep running.
func (e *engineClient) ServiceSetEndpointMode(ctx context.Context, idOrName, mode string) error {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return wrapNotFound(err)
	}
	cur := res.Service
	want := endpointMode(mode)
	if cur.Spec.EndpointSpec == nil {
		cur.Spec.EndpointSpec = &swarm.EndpointSpec{}
	}
	if cur.Spec.EndpointSpec.Mode == want {
		return nil
	}
	cur.Spec.EndpointSpec.Mode = want
	_, err = e.cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{Version: cur.Version, Spec: cur.Spec})
	return err
}

func endpointMode(mode string) swarm.ResolutionMode {
	if mode == string(swarm.ResolutionModeDNSRR) {
		return swarm.ResolutionModeDNSRR
	}
	return swarm.ResolutionModeVIP
}

// ServiceRemove deletes a service. A missing service is treated as success.
func (e *engineClient) ServiceRemove(ctx context.Context, idOrName string) error {
	if _, err := e.cli.ServiceRemove(ctx, idOrName, client.ServiceRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// ServiceInspect returns a service's desired replicas and running-task count.
func (e *engineClient) ServiceInspect(ctx context.Context, idOrName string) (ServiceStatus, error) {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return ServiceStatus{}, err
	}
	svc := res.Service
	st := ServiceStatus{ID: svc.ID, Name: svc.Spec.Name}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		st.Image = cs.Image
	}
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		st.Replicas = *svc.Spec.Mode.Replicated.Replicas
	}
	tasks, terr := e.cli.TaskList(ctx, client.TaskListOptions{
		Filters: selectorFilters("service", svc.ID, "desired-state", "running"),
	})
	if terr == nil {
		var oldest time.Time
		for _, t := range tasks.Items {
			if t.Status.State != swarm.TaskStateRunning {
				continue
			}
			st.RunningTasks++
			// Record which node the scheduler placed the task on, so callers can show
			// the real replica distribution rather than a single static node.
			if t.NodeID != "" {
				if st.Placement == nil {
					st.Placement = map[string]int{}
				}
				st.Placement[t.NodeID]++
			}
			// Uptime, from the swarm control plane rather than the container: the task
			// may be on a node we have no Docker client for, where there is nothing to
			// inspect. Status.Timestamp is when it entered the running state.
			if ts := t.Status.Timestamp; !ts.IsZero() && (oldest.IsZero() || ts.Before(oldest)) {
				oldest = ts
			}
		}
		if !oldest.IsZero() {
			st.StartedAt = oldest.UTC().Format(time.RFC3339Nano)
		}
	}
	return st, nil
}

// ServiceList returns the swarm's services (identity + desired replicas).
func (e *engineClient) ServiceList(ctx context.Context) ([]ServiceStatus, error) {
	res, err := e.cli.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ServiceStatus, 0, len(res.Items))
	for _, s := range res.Items {
		st := ServiceStatus{ID: s.ID, Name: s.Spec.Name}
		if s.Spec.Mode.Replicated != nil && s.Spec.Mode.Replicated.Replicas != nil {
			st.Replicas = *s.Spec.Mode.Replicated.Replicas
		}
		out = append(out, st)
	}
	return out, nil
}

// ServiceRestart forces a rolling restart of a service's tasks in place
// (equivalent to `docker service update --force`), without changing its spec.
func (e *engineClient) ServiceRestart(ctx context.Context, idOrName string) error {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	cur := res.Service
	cur.Spec.TaskTemplate.ForceUpdate++
	_, err = e.cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{Version: cur.Version, Spec: cur.Spec})
	return err
}

// ServiceTaskContainerID returns the container id of a running task of the named service on THIS
// engine, so logs/stats/exec/top can attach to it. Returns ErrNotFound when no task runs here —
// resolve the node with ServiceTaskNodeID. A non-running task is used as a fallback.
func (e *engineClient) ServiceTaskContainerID(ctx context.Context, serviceName string) (string, error) {
	res, err := e.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: selectorFilters("label", "com.docker.swarm.service.name="+serviceName),
	})
	if err != nil {
		return "", err
	}
	fallback := ""
	for _, c := range res.Items {
		if c.State == "running" {
			return c.ID, nil
		}
		if fallback == "" {
			fallback = c.ID
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", ErrNotFound
}

// ServiceEnv returns a service's environment, answering how a service was configured without
// re-deriving it from stored state. The result usually carries secrets, so it is for the service
// layer to inspect and never to return to a client.
func (e *engineClient) ServiceEnv(ctx context.Context, idOrName string) ([]string, error) {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return nil, wrapNotFound(err)
	}
	if cs := res.Service.Spec.TaskTemplate.ContainerSpec; cs != nil {
		return cs.Env, nil
	}
	return nil, nil
}

// StreamServiceLogs streams a swarm service's logs, aggregated across every task. Must be called
// on a swarm MANAGER. It is the ONLY way to read the logs of a task on a node Miabi has no Docker
// client for, and it aggregates all replicas, which reading one container never could.
func (e *engineClient) StreamServiceLogs(ctx context.Context, serviceName string, follow bool, tail string, sink func(LogLine) error) error {
	tail = sanitizeTail(tail)
	rc, err := e.cli.ServiceLogs(ctx, serviceName, client.ServiceLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: follow, Tail: tail,
	})
	if err != nil {
		return wrapNotFound(err)
	}
	defer func() { _ = rc.Close() }()

	stdout := &lineWriter{stream: "stdout", sink: sink}
	stderr := &lineWriter{stream: "stderr", sink: sink}
	_, err = stdcopy.StdCopy(stdout, stderr, rc)
	if errors.Is(err, errSinkStop) || errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

// CreateOverlayNetwork ensures an attachable, encrypted overlay network exists
// (the per-workspace cluster network). Idempotent: an existing network of the
// same name is reused. Requires the engine to be a swarm manager.
func (e *engineClient) CreateOverlayNetwork(ctx context.Context, name string) (string, error) {
	return e.EnsureNetworkSpec(ctx, NetworkSpec{Name: name, Driver: "overlay", Attachable: true, Encrypted: true})
}
