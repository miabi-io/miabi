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

type ServiceSpec struct {
	Name     string
	Image    string
	Env      []string // KEY=VALUE
	Cmd      []string // command args (ContainerSpec.Args); nil keeps the image default
	Replicas uint64   // 0 is treated as 1 (ignored when Global)
	// Global also covers nodes that join later, so a per-node daemon ships without touching each host.
	Global         bool
	Networks       []string          // swarm-scoped (overlay) network names to attach
	NetworkAliases []string          // DNS aliases applied on each attached network
	Mounts         map[string]string // volume name -> container path
	// Required for shared volumes on a service, or a task on another node silently gets an empty local volume.
	MountDrivers map[string]ServiceMountDriver
	// Binds need no driver config; the path is assumed present on every node a task lands on.
	Binds []ServiceBind
	// Swarm distributes configs over raft, so a task lands with its files on any node.
	Configs     []ServiceConfig
	Labels      map[string]string
	MemoryBytes int64
	NanoCPUs    int64
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
	// RegistryAuth is distributed to workers so their tasks can pull (like --with-registry-auth); nil if public.
	RegistryAuth *RegistryAuth
	// IngressNetwork is separate from Networks so tenant aliases never land on the shared overlay and collide.
	IngressNetwork string
	IngressAlias   string
	// EndpointMode "dnsrr" is for hosts that cannot program IPVS; default "vip".
	EndpointMode string
}

// ServiceBind is a host-path bind mount for a swarm service task (mount.TypeBind).
type ServiceBind struct {
	Source   string // host path (present on every node)
	Target   string // container path
	ReadOnly bool
}

// ServiceMountDriver makes every node materialize the same backing volume rather than an empty local one.
type ServiceMountDriver struct {
	Name    string
	Options map[string]string
}

type ServiceStatus struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Image        string         `json:"image,omitempty"`
	Replicas     uint64         `json:"replicas"`      // desired
	RunningTasks uint64         `json:"running_tasks"` // currently running
	Placement    map[string]int `json:"placement,omitempty"`
	// StartedAt comes from the swarm control plane, so it is known even for nodes Miabi has no client for.
	StartedAt string `json:"started_at,omitempty"`
}

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
	// The shared overlay carries only the unique upstream alias so app names can't collide across workspaces.
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

// Idempotent because swarm has no live network connect: a real change is a rolling update that restarts tasks.
func (e *engineClient) ServiceSetIngressNetwork(ctx context.Context, idOrName, network, alias string, attached bool) error {
	res, err := e.cli.ServiceInspect(ctx, idOrName, client.ServiceInspectOptions{})
	if err != nil {
		return wrapNotFound(err)
	}
	cur := res.Service

	// Docker may report an attachment by id, so match the id too or the network is re-added.
	id := ""
	if n, ierr := e.cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{}); ierr == nil {
		id = n.Network.ID
	}
	next, changed := ingressAttachments(cur.Spec.TaskTemplate.Networks, network, id, alias, attached)
	if !changed {
		return nil
	}
	cur.Spec.TaskTemplate.Networks = next
	_, err = e.cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{Version: cur.Version, Spec: cur.Spec})
	return err
}

// Separated so the "no change" answer, which avoids needless task restarts, can be tested.
func ingressAttachments(nets []swarm.NetworkAttachmentConfig, network, id, alias string, attached bool) ([]swarm.NetworkAttachmentConfig, bool) {
	idx := -1
	for i, n := range nets {
		if n.Target == network || (id != "" && n.Target == id) {
			idx = i
			break
		}
	}
	if attached == (idx >= 0) {
		return nets, false
	}
	next := make([]swarm.NetworkAttachmentConfig, 0, len(nets)+1)
	if attached {
		var aliases []string
		if alias != "" {
			aliases = []string{alias}
		}
		next = append(append(next, nets...), swarm.NetworkAttachmentConfig{Target: network, Aliases: aliases})
	} else {
		next = append(next, nets[:idx]...)
		next = append(next, nets[idx+1:]...)
	}
	return next, true
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
		return ServiceStatus{}, wrapNotFound(err)
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
			if t.NodeID != "" {
				if st.Placement == nil {
					st.Placement = map[string]int{}
				}
				st.Placement[t.NodeID]++
			}
			// From the control plane, since the task may be on a node with no Docker client to inspect.
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
	res, err := e.cli.ServiceList(ctx, client.ServiceListOptions{Status: true})
	if err != nil {
		return nil, err
	}
	out := make([]ServiceStatus, 0, len(res.Items))
	for _, s := range res.Items {
		st := ServiceStatus{ID: s.ID, Name: s.Spec.Name}
		if s.Spec.Mode.Replicated != nil && s.Spec.Mode.Replicated.Replicas != nil {
			st.Replicas = *s.Spec.Mode.Replicated.Replicas
		}
		if s.ServiceStatus != nil {
			st.RunningTasks = s.ServiceStatus.RunningTasks
			// Desired comes from the scheduler too, so a global service (no replica count) has one.
			st.Replicas = s.ServiceStatus.DesiredTasks
		}
		out = append(out, st)
	}
	return out, nil
}

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

// Returns ErrNotFound when no task runs on this engine; resolve the node with ServiceTaskNodeID.
func (e *engineClient) ServiceTaskContainerID(ctx context.Context, serviceName string) (string, error) {
	res, err := e.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: selectorFilters("label", SwarmServiceNameLabel+"="+serviceName),
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

// ServiceEnv usually carries secrets, so it is for the service layer to inspect, never to return to a client.
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

// StreamServiceLogs must run on a manager; it is the only way to read tasks on nodes Miabi has no client for.
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

// Requires the engine to be a swarm manager.
func (e *engineClient) CreateOverlayNetwork(ctx context.Context, name string) (string, error) {
	return e.EnsureNetworkSpec(ctx, NetworkSpec{Name: name, Driver: "overlay", Attachable: true, Encrypted: true})
}
