// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/go-archive"
	"github.com/moby/go-archive/compression"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

type engineClient struct {
	cli *client.Client
}

// New connects to the Docker engine using the standard environment
// (DOCKER_HOST etc.) with API version negotiation.
func New() (Client, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

func (e *engineClient) Close() error { return e.cli.Close() }

func (e *engineClient) Ping(ctx context.Context) error {
	_, err := e.cli.Ping(ctx, client.PingOptions{})
	return err
}

func (e *engineClient) Info(ctx context.Context) (Info, error) {
	res, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, err
	}
	info := res.Info
	ping, _ := e.cli.Ping(ctx, client.PingOptions{})
	runtimes := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		runtimes = append(runtimes, name)
	}
	return Info{
		Name:          info.Name,
		Version:       info.ServerVersion,
		APIVersion:    ping.APIVersion,
		OS:            info.OperatingSystem,
		Arch:          info.Architecture,
		Containers:    info.Containers,
		ContainersRun: info.ContainersRunning,
		Images:        info.Images,
		CPUs:          info.NCPU,
		MemTotal:      info.MemTotal,
		Runtimes:      runtimes,
	}, nil
}

// Capabilities reports what the connected engine supports, from Ping + Info in
// one round-trip. It maps the SDK's too-old-daemon error onto ErrEngineTooOld so
// the caller sees a clear reason ("engine too old, Miabi requires >= X") rather
// than an obscure SDK error.
func (e *engineClient) Capabilities(ctx context.Context) (Capabilities, error) {
	ping, err := e.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		if cerrdefs.IsInvalidArgument(err) {
			return Capabilities{}, fmt.Errorf("%w: Miabi requires Docker Engine >= %s: %v",
				ErrEngineTooOld, MinEngineVersion, err)
		}
		return Capabilities{}, err
	}
	info, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Capabilities{}, err
	}
	caps := Capabilities{
		APIVersion:    ping.APIVersion,
		EngineVersion: info.Info.ServerVersion,
		OS:            info.Info.OSType,
		Arch:          info.Info.Architecture,
	}
	sw := info.Info.Swarm
	caps.SwarmActive = string(sw.LocalNodeState) == "active"
	caps.SwarmManager = sw.ControlAvailable
	return caps, nil
}

// selectorFilters is the ONLY place internal/docker constructs an engine filter
// set, so a future change to the Filters API touches one function. Pairs are
// key,value,key,value…; a trailing unpaired key is ignored.
func selectorFilters(pairs ...string) client.Filters {
	f := client.Filters{}
	for i := 0; i+1 < len(pairs); i += 2 {
		f.Add(pairs[i], pairs[i+1])
	}
	return f
}

// toDeviceRequests maps Miabi GPU requests to Docker's DeviceRequest form. Each
// request targets the "nvidia" device driver; DeviceIDs pins exact cards while a
// nil DeviceIDs falls back to Count-of-any (-1 = all). An empty capability set
// defaults to [["gpu"]].
func toDeviceRequests(gpus []GPURequest) []container.DeviceRequest {
	if len(gpus) == 0 {
		return nil
	}
	out := make([]container.DeviceRequest, 0, len(gpus))
	for _, g := range gpus {
		dr := container.DeviceRequest{Driver: "nvidia", Capabilities: g.Capabilities}
		if len(dr.Capabilities) == 0 {
			dr.Capabilities = [][]string{{"gpu"}}
		}
		if len(g.DeviceIDs) > 0 {
			dr.DeviceIDs = g.DeviceIDs
		} else {
			dr.Count = g.Count
		}
		out = append(out, dr)
	}
	return out
}

func (e *engineClient) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	res, err := e.cli.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		ports := make([]Port, 0, len(c.Ports))
		for _, p := range c.Ports {
			ports = append(ports, Port{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Protocol: p.Type})
		}
		out = append(out, Container{
			ID: c.ID, Names: c.Names, Image: c.Image, State: string(c.State),
			Status: c.Status, Created: c.Created, Ports: ports, Labels: c.Labels,
		})
	}
	return out, nil
}

func (e *engineClient) InspectContainer(ctx context.Context, id string) (Container, error) {
	res, err := e.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, wrapNotFound(err)
	}
	c := res.Container
	health := ""
	if c.State.Health != nil {
		health = string(c.State.Health.Status)
	}
	var nets []ContainerNetwork
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			if ep == nil || !ep.IPAddress.IsValid() {
				continue
			}
			gateway := ""
			if ep.Gateway.IsValid() {
				gateway = ep.Gateway.String()
			}
			nets = append(nets, ContainerNetwork{
				Name: name, IPAddress: ep.IPAddress.String(), Gateway: gateway, Aliases: ep.Aliases,
			})
		}
	}
	return Container{
		ID:           c.ID,
		Names:        []string{strings.TrimPrefix(c.Name, "/")},
		Image:        c.Config.Image,
		State:        string(c.State.Status),
		Status:       string(c.State.Status),
		Health:       health,
		Restarting:   c.State.Restarting,
		RestartCount: c.RestartCount,
		ExitCode:     c.State.ExitCode,
		StartedAt:    c.State.StartedAt,
		Labels:       c.Config.Labels,
		Networks:     nets,
	}, nil
}

func (e *engineClient) InspectContainerConfig(ctx context.Context, id string) (ContainerConfig, error) {
	res, err := e.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerConfig{}, wrapNotFound(err)
	}
	c := res.Container
	cfg := ContainerConfig{
		ID:    c.ID,
		Name:  strings.TrimPrefix(c.Name, "/"),
		State: string(c.State.Status),
	}
	if c.Config != nil {
		cfg.Image = c.Config.Image
		cfg.Command = c.Config.Cmd
		cfg.Entrypoint = c.Config.Entrypoint
		cfg.Env = c.Config.Env
		cfg.User = c.Config.User
		cfg.Labels = c.Config.Labels
	}
	if c.HostConfig != nil {
		cfg.MemoryBytes = c.HostConfig.Memory
		cfg.NanoCPUs = c.HostConfig.NanoCPUs
		cfg.RestartPolicy = restartPolicyString(c.HostConfig.RestartPolicy)
		for p, binds := range c.HostConfig.PortBindings {
			pm := PortMapping{ContainerPort: int(p.Num()), Protocol: string(p.Proto())}
			for _, b := range binds {
				if hp, perr := strconv.Atoi(b.HostPort); perr == nil && hp > 0 {
					pm.HostPort = hp
					break
				}
			}
			cfg.Ports = append(cfg.Ports, pm)
		}
	}
	for _, m := range c.Mounts {
		cfg.Mounts = append(cfg.Mounts, ContainerMount{
			Type: string(m.Type), Name: m.Name, Source: m.Source,
			Destination: m.Destination, ReadOnly: !m.RW,
		})
	}
	if c.NetworkSettings != nil {
		for name := range c.NetworkSettings.Networks {
			cfg.Networks = append(cfg.Networks, name)
		}
	}
	return cfg, nil
}

// restartPolicyString renders an engine restart policy back to the string form Miabi stores
// (e.g. "on-failure:3"). Empty and "no" both map to "no".
func restartPolicyString(p container.RestartPolicy) string {
	switch p.Name {
	case container.RestartPolicyAlways:
		return string(container.RestartPolicyAlways)
	case container.RestartPolicyUnlessStopped:
		return string(container.RestartPolicyUnlessStopped)
	case container.RestartPolicyOnFailure:
		if p.MaximumRetryCount > 0 {
			return string(container.RestartPolicyOnFailure) + ":" + strconv.Itoa(p.MaximumRetryCount)
		}
		return string(container.RestartPolicyOnFailure)
	default:
		return string(container.RestartPolicyDisabled) // "no"
	}
}

// hostBinds renders privileged host bind mounts as Docker "source:target[:ro]" strings.
func hostBinds(mounts []BindMount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		b := m.Source + ":" + m.Target
		if m.ReadOnly {
			b += ":ro"
		}
		out = append(out, b)
	}
	return out
}

// containerVolumeMounts renders a RunSpec's named volumes and host binds for the HostConfig.
// Named volumes are bind strings unless NoCopyVolumes is set, in which case they use the Mount
// API with NoCopy so copy-up can't undo the restricted-profile chown. Host binds stay plain.
func containerVolumeMounts(spec RunSpec) ([]string, []mount.Mount) {
	binds := make([]string, 0, len(spec.Mounts)+len(spec.Binds))
	var volMounts []mount.Mount
	for vol, path := range spec.Mounts {
		if spec.NoCopyVolumes {
			volMounts = append(volMounts, mount.Mount{
				Type: mount.TypeVolume, Source: vol, Target: path,
				VolumeOptions: &mount.VolumeOptions{NoCopy: true},
			})
			continue
		}
		binds = append(binds, vol+":"+path)
	}
	binds = append(binds, hostBinds(spec.Binds)...)
	return binds, volMounts
}

func (e *engineClient) RunContainer(ctx context.Context, spec RunSpec) (string, error) {
	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for cp, hp := range spec.Ports {
		p, perr := network.ParsePort(cp) // cp is "containerPort/proto", e.g. "80/tcp"
		if perr != nil {
			continue
		}
		exposed[p] = struct{}{}
		hostIP := netip.IPv4Unspecified() // 0.0.0.0 — all interfaces
		if ip := spec.PortBindIPs[cp]; ip != "" {
			if addr, aerr := netip.ParseAddr(ip); aerr == nil {
				hostIP = addr // publish on a specific interface (e.g. the node's private IP)
			}
		}
		bindings[p] = []network.PortBinding{{HostIP: hostIP, HostPort: hp}}
	}

	binds, volMounts := containerVolumeMounts(spec)

	labels := spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedLabel] = "true"

	cfg := &container.Config{
		Image:        spec.Image,
		Hostname:     spec.Hostname,
		User:         spec.User, // "" = image default; "uid:0" under the restricted profile
		Env:          spec.Env,
		Entrypoint:   spec.Entrypoint,
		Cmd:          spec.Cmd,
		WorkingDir:   spec.WorkingDir,
		Labels:       labels,
		ExposedPorts: exposed,
	}
	if hc := spec.Healthcheck; hc != nil && len(hc.Test) > 0 {
		cfg.Healthcheck = &container.HealthConfig{
			Test:        hc.Test,
			Interval:    hc.Interval,
			Timeout:     hc.Timeout,
			Retries:     hc.Retries,
			StartPeriod: hc.StartPeriod,
		}
	}
	hostCfg := &container.HostConfig{
		PortBindings: bindings,
		Binds:        binds,
		Mounts:       volMounts,
		Resources: container.Resources{
			Memory:         spec.MemoryBytes,
			NanoCPUs:       spec.NanoCPUs,
			DeviceRequests: toDeviceRequests(spec.GPUs),
		},
		RestartPolicy: restartPolicy(spec.RestartPolicy),
		CapDrop:       spec.CapDrop,
		GroupAdd:      spec.GroupAdd,
	}
	if spec.NoNewPrivileges {
		hostCfg.SecurityOpt = append(hostCfg.SecurityOpt, "no-new-privileges")
	}

	var netCfg *network.NetworkingConfig
	if len(spec.Networks) > 0 {
		endpoints := map[string]*network.EndpointSettings{}
		for _, n := range spec.Networks {
			aliases := spec.NetworkAliases
			if extra := spec.AliasesByNetwork[n]; len(extra) > 0 {
				aliases = append(append([]string{}, spec.NetworkAliases...), extra...)
			}
			endpoints[n] = &network.EndpointSettings{Aliases: aliases}
		}
		netCfg = &network.NetworkingConfig{EndpointsConfig: endpoints}
	}

	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: cfg, HostConfig: hostCfg, NetworkingConfig: netCfg, Name: spec.Name,
	})
	if err != nil {
		return "", err
	}
	// Files land between create and start so the app never sees a half-written
	// config: the container has a filesystem but no process yet.
	if err := e.copyFiles(ctx, created.ID, spec.Files); err != nil {
		return created.ID, err
	}
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return created.ID, err
	}
	return created.ID, nil
}

// copyFiles writes the entries into the created container as one tar stream
// extracted at /. CopyToContainer does not create parent directories, so the
// archive carries a directory entry for each one; a nested key would otherwise
// fail with "could not find the file".
func (e *engineClient) copyFiles(ctx context.Context, id string, files []FileEntry) error {
	if len(files) == 0 {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	seen := map[string]bool{}
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, "/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			return fmt.Errorf("config file path %q is not a file", f.Path)
		}
		if dir := path.Dir(rel); dir != "." {
			var built string
			for _, seg := range strings.Split(dir, "/") {
				built = path.Join(built, seg)
				if seen[built] {
					continue
				}
				seen[built] = true
				if err := tw.WriteHeader(&tar.Header{Name: built + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
					return fmt.Errorf("tar dir %s: %w", built, err)
				}
			}
		}
		mode := int64(0o644)
		if f.Mode != "" {
			if v, perr := strconv.ParseInt(f.Mode, 8, 32); perr == nil {
				mode = v & 0o777
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: mode, Size: int64(len(f.Content))}); err != nil {
			return fmt.Errorf("tar header for %s: %w", f.Path, err)
		}
		if _, err := tw.Write([]byte(f.Content)); err != nil {
			return fmt.Errorf("tar body for %s: %w", f.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if _, err := e.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: "/", Content: &buf}); err != nil {
		return fmt.Errorf("copy config files: %w", err)
	}
	return nil
}

// restartPolicy maps a RunSpec restart-policy string onto the Docker engine policy. Empty or
// unrecognized falls back to "unless-stopped", the platform's historical default. "on-failure"
// may carry a ":N" max-retry suffix.
func restartPolicy(p string) container.RestartPolicy {
	switch {
	case p == string(container.RestartPolicyDisabled): // "no"
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}
	case p == string(container.RestartPolicyAlways):
		return container.RestartPolicy{Name: container.RestartPolicyAlways}
	case p == string(container.RestartPolicyOnFailure) || strings.HasPrefix(p, string(container.RestartPolicyOnFailure)+":"):
		rp := container.RestartPolicy{Name: container.RestartPolicyOnFailure}
		if _, rest, ok := strings.Cut(p, ":"); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				rp.MaximumRetryCount = n
			}
		}
		return rp
	default:
		return container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	}
}

func (e *engineClient) StartContainer(ctx context.Context, id string) error {
	_, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return wrapNotFound(err)
}

func (e *engineClient) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	t := timeoutSeconds
	_, err := e.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &t})
	return wrapNotFound(err)
}

func (e *engineClient) RestartContainer(ctx context.Context, id string, timeoutSeconds int) error {
	t := timeoutSeconds
	_, err := e.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &t})
	return wrapNotFound(err)
}

func (e *engineClient) RemoveContainer(ctx context.Context, id string, force bool) error {
	_, err := e.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force})
	return wrapNotFound(err)
}

// createOneShot creates (but does not start) a one-shot helper container from a RunSpec. Resource
// limits and the restart policy are honored so callers can cap a build/probe container; one-shots
// never restart.
func (e *engineClient) createOneShot(ctx context.Context, spec RunSpec) (string, error) {
	labels := spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedLabel] = "true"

	binds := make([]string, 0, len(spec.Mounts)+len(spec.Binds))
	for vol, path := range spec.Mounts {
		binds = append(binds, vol+":"+path)
	}
	binds = append(binds, hostBinds(spec.Binds)...)

	cfg := &container.Config{Image: spec.Image, Env: spec.Env, Entrypoint: spec.Entrypoint, Cmd: spec.Cmd, WorkingDir: spec.WorkingDir, Labels: labels}
	hostCfg := &container.HostConfig{
		Binds: binds,
		Resources: container.Resources{
			Memory:         spec.MemoryBytes,
			NanoCPUs:       spec.NanoCPUs,
			DeviceRequests: toDeviceRequests(spec.GPUs), // used by the GPU inventory probe
		},
	}

	var netCfg *network.NetworkingConfig
	if len(spec.Networks) > 0 {
		endpoints := map[string]*network.EndpointSettings{}
		for _, n := range spec.Networks {
			aliases := spec.NetworkAliases
			if extra := spec.AliasesByNetwork[n]; len(extra) > 0 {
				aliases = append(append([]string{}, spec.NetworkAliases...), extra...)
			}
			endpoints[n] = &network.EndpointSettings{Aliases: aliases}
		}
		netCfg = &network.NetworkingConfig{EndpointsConfig: endpoints}
	}

	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: cfg, HostConfig: hostCfg, NetworkingConfig: netCfg, Name: spec.Name,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (e *engineClient) RunOneShot(ctx context.Context, spec RunSpec) (int, string, error) {
	id, err := e.createOneShot(ctx, spec)
	if err != nil {
		return -1, "", err
	}
	defer func() {
		_, _ = e.cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	}()

	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return -1, "", err
	}

	wait := e.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var exitCode int
	select {
	case err := <-wait.Error:
		if err != nil {
			return -1, e.collectLogs(id), err
		}
	case st := <-wait.Result:
		exitCode = int(st.StatusCode)
	case <-ctx.Done():
		return -1, e.collectLogs(id), ctx.Err()
	}
	return exitCode, e.collectLogs(id), nil
}

// RunOneShotStream runs a one-shot helper container to completion, streaming its combined output
// to sink line-by-line as it runs, then removes it and returns the exit code. Used by the
// buildpack build provider, which needs live `pack build` output.
func (e *engineClient) RunOneShotStream(ctx context.Context, spec RunSpec, sink func(LogLine) error) (int, error) {
	id, err := e.createOneShot(ctx, spec)
	if err != nil {
		return -1, err
	}
	defer func() {
		_, _ = e.cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	}()

	// Wait must be set up before start so a fast-exiting container's status isn't
	// missed.
	wait := e.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return -1, err
	}

	// Follow the logs until the stream closes (container exit) or the context is
	// cancelled. stdout and stderr both feed the same sink, interleaved as emitted.
	rc, lerr := e.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if lerr == nil {
		w := &lineWriter{stream: "build", sink: sink}
		_, _ = stdcopy.StdCopy(w, w, rc)
		_ = rc.Close()
	}

	select {
	case err := <-wait.Error:
		if err != nil {
			return -1, err
		}
	case st := <-wait.Result:
		return int(st.StatusCode), nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
	return -1, nil
}

func (e *engineClient) CopyToVolume(ctx context.Context, volumeName, image, name string, content io.Reader, size int64) error {
	cfg := &container.Config{Image: image, Labels: map[string]string{ManagedLabel: "true"}}
	hostCfg := &container.HostConfig{Binds: []string{volumeName + ":/dpvol"}}
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg})
	if err != nil {
		return err
	}
	defer func() {
		_, _ = e.cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()

	// CopyToContainer extracts a tar at the destination path; stream a single-file
	// tar so large dumps don't buffer in memory.
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(tw, content); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = tw.Close()
		_ = pw.Close()
	}()
	if _, err := e.cli.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/dpvol", Content: pr}); err != nil {
		return fmt.Errorf("copy to volume: %w", err)
	}
	return nil
}

// ReadContainerFile reads a single regular file from a container's filesystem
// via CopyFromContainer (a tar stream with one entry), capped at 5 MiB.
func (e *engineClient) ReadContainerFile(ctx context.Context, containerID, path string) ([]byte, error) {
	res, err := e.cli.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return nil, wrapNotFound(err)
	}
	defer func() { _ = res.Content.Close() }()
	tr := tar.NewReader(res.Content)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		const maxSize = 5 << 20 // 5 MiB — config files are tiny
		return io.ReadAll(io.LimitReader(tr, maxSize))
	}
}

func (e *engineClient) CopyFileFromVolume(ctx context.Context, volumeName, image, file string) (io.ReadCloser, int64, error) {
	cfg := &container.Config{Image: image, Labels: map[string]string{ManagedLabel: "true"}}
	hostCfg := &container.HostConfig{Binds: []string{volumeName + ":/backup:ro"}}
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg})
	if err != nil {
		return nil, 0, err
	}
	cleanup := func() {
		_, _ = e.cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}

	// CopyFromContainer returns the path as a tar stream; the file we want is
	// the single entry inside it.
	res, err := e.cli.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: "/backup/" + file})
	if err != nil {
		cleanup()
		return nil, 0, wrapNotFound(err)
	}
	tr := tar.NewReader(res.Content)
	hdr, err := tr.Next()
	if err != nil {
		_ = res.Content.Close()
		cleanup()
		if errors.Is(err, io.EOF) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return &volumeFileReader{tr: tr, closer: res.Content, cleanup: cleanup}, hdr.Size, nil
}

// volumeFileReader streams one file out of a CopyFromContainer tar archive and, on Close,
// releases the tar stream and removes the helper container.
type volumeFileReader struct {
	tr      *tar.Reader
	closer  io.Closer
	cleanup func()
	once    sync.Once
}

func (r *volumeFileReader) Read(p []byte) (int, error) { return r.tr.Read(p) }

func (r *volumeFileReader) Close() error {
	r.once.Do(func() {
		_ = r.closer.Close()
		r.cleanup()
	})
	return nil
}

func (e *engineClient) collectLogs(id string) string {
	rc, err := e.cli.ContainerLogs(context.Background(), id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: "all",
	})
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	_, _ = stdcopy.StdCopy(&buf, &buf, rc)
	return buf.String()
}

func (e *engineClient) StreamEvents(ctx context.Context, sink func(EngineEvent) error) error {
	// Stream all container lifecycle events and filter in Go: the subscriber
	// cheaply ignores any without an io.miabi.app label (see events.Subscriber).
	evs := e.cli.Events(ctx, client.EventsListOptions{Filters: selectorFilters("type", "container")})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-evs.Err:
			return err
		case m := <-evs.Messages:
			if err := sink(EngineEvent{
				Action:      string(m.Action),
				ContainerID: m.Actor.ID,
				Attributes:  m.Actor.Attributes,
			}); err != nil {
				return err
			}
		}
	}
}

func (e *engineClient) PullImage(ctx context.Context, ref string, auth *RegistryAuth) error {
	opts := client.ImagePullOptions{}
	if enc, err := encodeRegistryAuth(auth); err != nil {
		return fmt.Errorf("encode registry auth: %w", err)
	} else if enc != "" {
		opts.RegistryAuth = enc
	}
	rc, err := e.cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(io.Discard, rc)
	return err
}

func (e *engineClient) TagImage(ctx context.Context, source, target string) error {
	_, err := e.cli.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target})
	return err
}

func (e *engineClient) PushImage(ctx context.Context, ref string, auth *RegistryAuth) error {
	opts := client.ImagePushOptions{}
	if enc, err := encodeRegistryAuth(auth); err != nil {
		return fmt.Errorf("encode registry auth: %w", err)
	} else if enc != "" {
		opts.RegistryAuth = enc
	}
	rc, err := e.cli.ImagePush(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	// Push errors (auth denied, quota) surface as a JSON {"error":"…"} message in
	// the progress stream rather than a transport error, so scan for one.
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("push %s: %s", ref, msg.Error)
		}
	}
}

func (e *engineClient) BuildImage(ctx context.Context, contextDir, dockerfile, tag string, sink func(LogLine) error) error {
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	tarball, err := archive.Tar(contextDir, compression.None)
	if err != nil {
		return fmt.Errorf("tar build context: %w", err)
	}
	defer func() { _ = tarball.Close() }()

	resp, err := e.cli.ImageBuild(ctx, tarball, client.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: dockerfile,
		Remove:     true,
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// The build response is a stream of JSON objects: {"stream":"..."} for
	// progress and {"error":"..."} on failure.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("build failed: %s", msg.Error)
		}
		if line := strings.TrimRight(msg.Stream, "\n"); line != "" && sink != nil {
			_ = sink(LogLine{Stream: "build", Text: line})
		}
	}
}

// InspectImage returns a local image's identity (ID + digest) and size. Used by
// the pipeline build step to capture the digest of the image it just built and
// by deploy-by-digest to record what ran.
func (e *engineClient) InspectImage(ctx context.Context, ref string) (ImageInspect, error) {
	insp, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		return ImageInspect{}, wrapNotFound(err)
	}
	digest := insp.ID
	// A pushed/pulled image carries a repo digest (repo@sha256:…); prefer it so
	// provenance survives a registry round-trip. A locally-built image has none,
	// so the content-addressable ID is the stable local handle.
	if len(insp.RepoDigests) > 0 {
		if _, d, ok := strings.Cut(insp.RepoDigests[0], "@"); ok && d != "" {
			digest = d
		}
	}
	return ImageInspect{ID: insp.ID, Digest: digest, Size: insp.Size}, nil
}

// ImageExists reports whether ref resolves to a local image. It lets a deploy
// no-op a redundant pull/build when the artifact is already present on the node.
func (e *engineClient) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := e.cli.ImageInspect(ctx, ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RemoveImage deletes a local image by reference. A not-found image is success
// (idempotent), so GC can prune a row whose image a prior sweep already removed.
func (e *engineClient) RemoveImage(ctx context.Context, ref string, force bool) error {
	_, err := e.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	if err != nil && cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// maxLogTail bounds how many historical log lines a single stream may replay on connect, so a
// request for "all" against a chatty container can't flood the client or pin memory.
const maxLogTail = 5000

// sanitizeTail validates the caller-supplied tail count before it reaches the Docker API. Empty
// defaults to a small window; "all" and out-of-range numbers are clamped to maxLogTail;
// non-numeric garbage falls back to the default.
func sanitizeTail(tail string) string {
	switch tail {
	case "":
		return "100"
	case "all":
		return strconv.Itoa(maxLogTail)
	}
	n, err := strconv.Atoi(tail)
	if err != nil || n <= 0 {
		return "100"
	}
	if n > maxLogTail {
		return strconv.Itoa(maxLogTail)
	}
	return strconv.Itoa(n)
}

func (e *engineClient) StreamLogs(ctx context.Context, id string, follow bool, tail string, sink func(LogLine) error) error {
	tail = sanitizeTail(tail)
	rc, err := e.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
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

func (e *engineClient) StreamStats(ctx context.Context, id string, sink func(StatsSample) error) error {
	res, err := e.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return wrapNotFound(err)
	}
	defer func() { _ = res.Body.Close() }()

	dec := json.NewDecoder(res.Body)
	for {
		if ctx.Err() != nil {
			return nil
		}
		var s container.StatsResponse
		if err := dec.Decode(&s); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := sink(sampleFromStats(s)); err != nil {
			return nil
		}
	}
}

func (e *engineClient) StatsOnce(ctx context.Context, id string) (StatsSample, error) {
	// IncludePreviousSample makes the daemon collect a prior sample so PreCPUStats
	// is populated and the CPU delta is non-zero on a single read.
	res, err := e.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return StatsSample{}, wrapNotFound(err)
	}
	defer func() { _ = res.Body.Close() }()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return StatsSample{}, err
	}
	return sampleFromStats(s), nil
}

func (e *engineClient) EnsureNetwork(ctx context.Context, name string) (string, error) {
	return e.EnsureNetworkSpec(ctx, NetworkSpec{Name: name})
}

func (e *engineClient) CreateNetwork(ctx context.Context, name, driver string, internal bool) (string, error) {
	return e.CreateNetworkSpec(ctx, NetworkSpec{Name: name, Driver: driver, Internal: internal})
}

// createNetworkOptions builds the Docker create options from a NetworkSpec,
// including IPAM when a subnet is set.
func createNetworkOptions(spec NetworkSpec) client.NetworkCreateOptions {
	driver := spec.Driver
	if driver == "" {
		driver = "bridge"
	}
	labels := map[string]string{ManagedLabel: "true"}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	opts := client.NetworkCreateOptions{
		Driver:     driver,
		Internal:   spec.Internal,
		Attachable: spec.Attachable,
		Labels:     labels,
	}
	if spec.Encrypted {
		opts.Options = map[string]string{"encrypted": ""}
	}
	if spec.Subnet != "" {
		if prefix, perr := netip.ParsePrefix(spec.Subnet); perr == nil {
			cfg := network.IPAMConfig{Subnet: prefix}
			if spec.Gateway != "" {
				if gw, gerr := netip.ParseAddr(spec.Gateway); gerr == nil {
					cfg.Gateway = gw
				}
			}
			opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{cfg}}
		}
	}
	return opts
}

// CreateNetworkSpec always creates the network (errors if the name exists).
func (e *engineClient) CreateNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error) {
	created, err := e.cli.NetworkCreate(ctx, spec.Name, createNetworkOptions(spec))
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// EnsureNetworkSpec returns the existing network of the same name, or creates it
// from the spec. Idempotent — used for the shared gateway and remote-node recreate.
func (e *engineClient) EnsureNetworkSpec(ctx context.Context, spec NetworkSpec) (string, error) {
	if existing, err := e.cli.NetworkInspect(ctx, spec.Name, client.NetworkInspectOptions{}); err == nil {
		return existing.Network.ID, nil
	}
	return e.CreateNetworkSpec(ctx, spec)
}

func (e *engineClient) RemoveNetwork(ctx context.Context, name string) error {
	_, err := e.cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (e *engineClient) NetworkConnect(ctx context.Context, name, containerID string, aliases []string) error {
	_, err := e.cli.NetworkConnect(ctx, name, client.NetworkConnectOptions{
		Container:      containerID,
		EndpointConfig: &network.EndpointSettings{Aliases: aliases},
	})
	// Already attached: treat as success so reconciles are idempotent.
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return nil
	}
	return err
}

func (e *engineClient) NetworkDisconnect(ctx context.Context, name, containerID string, force bool) error {
	_, err := e.cli.NetworkDisconnect(ctx, name, client.NetworkDisconnectOptions{Container: containerID, Force: force})
	// Not attached (or the network/container is gone): nothing to do.
	if cerrdefs.IsNotFound(err) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "is not connected")) {
		return nil
	}
	return err
}

func (e *engineClient) ListNetworks(ctx context.Context) ([]Network, error) {
	res, err := e.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(res.Items))
	for _, n := range res.Items {
		subnet := ""
		if len(n.IPAM.Config) > 0 && n.IPAM.Config[0].Subnet.IsValid() {
			subnet = n.IPAM.Config[0].Subnet.String()
		}
		out = append(out, Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Labels: n.Labels, Subnet: subnet})
	}
	return out, nil
}

func (e *engineClient) CreateVolume(ctx context.Context, name string, labels map[string]string, sizeBytes int64) (Volume, error) {
	return e.CreateVolumeWith(ctx, VolumeSpec{Name: name, Labels: labels, SizeBytes: sizeBytes})
}

func (e *engineClient) CreateVolumeWith(ctx context.Context, spec VolumeSpec) (Volume, error) {
	labels := spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedLabel] = "true"
	// Record the declared capacity as a label. A *hard* size cap needs a sized backing volume, which
	// depends on the node's storage backend and is layered on separately; the label makes the size
	// visible to `docker volume inspect` and to quota tracking for soft enforcement.
	if spec.SizeBytes > 0 {
		labels[LabelSizeBytes] = strconv.FormatInt(spec.SizeBytes, 10)
	}
	opts := client.VolumeCreateOptions{Name: spec.Name, Labels: labels}
	// Shared-storage drivers (nfs/cifs/…) carry their backend in driver options;
	// an empty driver uses Docker's default (local). The NFS/CIFS case uses the
	// built-in local driver with mount options, so no external plugin is needed.
	if spec.Driver != "" {
		opts.Driver = spec.Driver
	}
	if len(spec.DriverOpts) > 0 {
		opts.DriverOpts = spec.DriverOpts
	}
	res, err := e.cli.VolumeCreate(ctx, opts)
	if err != nil {
		return Volume{}, err
	}
	v := res.Volume
	return Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt}, nil
}

func (e *engineClient) ListVolumes(ctx context.Context) ([]Volume, error) {
	res, err := e.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, Labels: v.Labels})
	}
	return out, nil
}

func (e *engineClient) InspectVolume(ctx context.Context, name string) (Volume, error) {
	res, err := e.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return Volume{}, wrapNotFound(err)
	}
	v := res.Volume
	return Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, Labels: v.Labels}, nil
}

func (e *engineClient) RemoveVolume(ctx context.Context, name string, force bool) error {
	_, err := e.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return wrapNotFound(err)
}

// --- helpers ---

// encodeRegistryAuth base64-encodes a registry credential for the Docker API's
// X-Registry-Auth header. Returns "" (no error) when auth is nil/empty.
func encodeRegistryAuth(auth *RegistryAuth) (string, error) {
	if auth == nil || (auth.Username == "" && auth.Password == "") {
		return "", nil
	}
	return authconfig.Encode(registry.AuthConfig{
		Username:      auth.Username,
		Password:      auth.Password,
		ServerAddress: auth.Server,
	})
}

func wrapNotFound(err error) error {
	if err != nil && cerrdefs.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func sampleFromStats(s container.StatsResponse) StatsSample {
	var sample StatsSample

	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if sysDelta > 0 && cpuDelta > 0 {
		sample.CPUPercent = (cpuDelta / sysDelta) * cpus * 100.0
	}

	usage := s.MemoryStats.Usage
	if inactive, ok := s.MemoryStats.Stats["inactive_file"]; ok && inactive < usage {
		usage -= inactive
	}
	sample.MemoryUsage = usage
	sample.MemoryLimit = s.MemoryStats.Limit
	if s.MemoryStats.Limit > 0 {
		sample.MemoryPercent = float64(usage) / float64(s.MemoryStats.Limit) * 100.0
	}

	for _, n := range s.Networks {
		sample.NetworkRxBytes += n.RxBytes
		sample.NetworkTxBytes += n.TxBytes
	}
	return sample
}

// errSinkStop signals the log sink asked to stop streaming.
var errSinkStop = errors.New("sink stop")

// lineWriter splits writes into lines and forwards them to a sink.
type lineWriter struct {
	stream string
	sink   func(LogLine) error
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if err := w.sink(LogLine{Stream: w.stream, Text: line}); err != nil {
			return len(p), errSinkStop
		}
	}
	return len(p), nil
}
