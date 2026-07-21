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
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

// engineClient adapts the Docker SDK to the Client interface.
type engineClient struct {
	cli *client.Client
}

// New connects to the Docker engine using the standard environment
// (DOCKER_HOST etc.) with API version negotiation.
func New() (Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

func (e *engineClient) Close() error { return e.cli.Close() }

func (e *engineClient) Ping(ctx context.Context) error {
	_, err := e.cli.Ping(ctx)
	return err
}

func (e *engineClient) Info(ctx context.Context) (Info, error) {
	info, err := e.cli.Info(ctx)
	if err != nil {
		return Info{}, err
	}
	ping, _ := e.cli.Ping(ctx)
	runtimes := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		runtimes = append(runtimes, name)
	}
	return Info{
		Name:          info.Name,
		Version:       info.ServerVersion,
		APIVersion:    string(ping.APIVersion),
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
	list, err := e.cli.ContainerList(ctx, container.ListOptions{All: all})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(list))
	for _, c := range list {
		ports := make([]Port, 0, len(c.Ports))
		for _, p := range c.Ports {
			ports = append(ports, Port{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Protocol: p.Type})
		}
		out = append(out, Container{
			ID: c.ID, Names: c.Names, Image: c.Image, State: c.State,
			Status: c.Status, Created: c.Created, Ports: ports, Labels: c.Labels,
		})
	}
	return out, nil
}

func (e *engineClient) InspectContainer(ctx context.Context, id string) (Container, error) {
	c, err := e.cli.ContainerInspect(ctx, id)
	if err != nil {
		return Container{}, wrapNotFound(err)
	}
	health := ""
	if c.State.Health != nil {
		health = c.State.Health.Status
	}
	var nets []ContainerNetwork
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			if ep == nil || ep.IPAddress == "" {
				continue
			}
			nets = append(nets, ContainerNetwork{
				Name: name, IPAddress: ep.IPAddress, Gateway: ep.Gateway, Aliases: ep.Aliases,
			})
		}
	}
	return Container{
		ID:           c.ID,
		Names:        []string{strings.TrimPrefix(c.Name, "/")},
		Image:        c.Config.Image,
		State:        c.State.Status,
		Status:       c.State.Status,
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
	c, err := e.cli.ContainerInspect(ctx, id)
	if err != nil {
		return ContainerConfig{}, wrapNotFound(err)
	}
	cfg := ContainerConfig{
		ID:    c.ID,
		Name:  strings.TrimPrefix(c.Name, "/"),
		State: c.State.Status,
	}
	if c.Config != nil {
		cfg.Image = c.Config.Image
		cfg.Command = c.Config.Cmd
		cfg.Entrypoint = c.Config.Entrypoint
		cfg.Env = c.Config.Env
		cfg.Labels = c.Config.Labels
	}
	// Published host ports, from the host config's port bindings.
	if c.HostConfig != nil {
		cfg.MemoryBytes = c.HostConfig.Memory
		cfg.NanoCPUs = c.HostConfig.NanoCPUs
		cfg.RestartPolicy = restartPolicyString(c.HostConfig.RestartPolicy)
		for p, binds := range c.HostConfig.PortBindings {
			pm := PortMapping{ContainerPort: p.Int(), Protocol: p.Proto()}
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

// restartPolicyString renders an engine restart policy back to the string form
// Miabi stores (e.g. "on-failure:3"). Empty/"no" both map to "no".
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

// hostBinds renders privileged host bind mounts as Docker "source:target[:ro]"
// strings.
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

func (e *engineClient) RunContainer(ctx context.Context, spec RunSpec) (string, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for cp, hp := range spec.Ports {
		p := nat.Port(cp)
		exposed[p] = struct{}{}
		hostIP := "0.0.0.0"
		if ip := spec.PortBindIPs[cp]; ip != "" {
			hostIP = ip // publish on a specific interface (e.g. the node's private IP)
		}
		bindings[p] = []nat.PortBinding{{HostIP: hostIP, HostPort: hp}}
	}

	binds := make([]string, 0, len(spec.Mounts)+len(spec.Binds))
	for vol, path := range spec.Mounts {
		binds = append(binds, vol+":"+path)
	}
	binds = append(binds, hostBinds(spec.Binds)...)

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

	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, spec.Name)
	if err != nil {
		return "", err
	}
	if err := e.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return created.ID, err
	}
	return created.ID, nil
}

// restartPolicy maps a RunSpec restart-policy string onto the Docker engine
// policy. Empty (or unrecognized) falls back to "unless-stopped", the platform's
// historical default. "on-failure" may carry a ":N" max-retry suffix.
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
	return wrapNotFound(e.cli.ContainerStart(ctx, id, container.StartOptions{}))
}

func (e *engineClient) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	t := timeoutSeconds
	return wrapNotFound(e.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &t}))
}

func (e *engineClient) RestartContainer(ctx context.Context, id string, timeoutSeconds int) error {
	t := timeoutSeconds
	return wrapNotFound(e.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &t}))
}

func (e *engineClient) RemoveContainer(ctx context.Context, id string, force bool) error {
	return wrapNotFound(e.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force}))
}

// createOneShot creates (but does not start) a one-shot helper container from a
// RunSpec. Resource limits and the restart policy of a RunSpec are intentionally
// honored so callers can cap a build/probe container; one-shots never restart.
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

	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, spec.Name)
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
		_ = e.cli.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	}()

	if err := e.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return -1, "", err
	}

	statusCh, errCh := e.cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	var exitCode int
	select {
	case err := <-errCh:
		if err != nil {
			return -1, e.collectLogs(id), err
		}
	case st := <-statusCh:
		exitCode = int(st.StatusCode)
	case <-ctx.Done():
		return -1, e.collectLogs(id), ctx.Err()
	}
	return exitCode, e.collectLogs(id), nil
}

// RunOneShotStream runs a one-shot helper container to completion, streaming its
// combined stdout/stderr to sink line-by-line as it runs (so build logs reach
// the UI live), then removes it and returns the exit code. Used by the buildpack
// build provider, which needs live `pack build` output the way BuildImage gives
// live `docker build` output.
func (e *engineClient) RunOneShotStream(ctx context.Context, spec RunSpec, sink func(LogLine) error) (int, error) {
	id, err := e.createOneShot(ctx, spec)
	if err != nil {
		return -1, err
	}
	defer func() {
		_ = e.cli.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	}()

	// Wait must be set up before start so a fast-exiting container's status isn't
	// missed.
	statusCh, errCh := e.cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	if err := e.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return -1, err
	}

	// Follow the logs until the stream closes (container exit) or the context is
	// cancelled. stdout and stderr both feed the same sink, interleaved as emitted.
	rc, lerr := e.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if lerr == nil {
		w := &lineWriter{stream: "build", sink: sink}
		_, _ = stdcopy.StdCopy(w, w, rc)
		_ = rc.Close()
	}

	select {
	case err := <-errCh:
		if err != nil {
			return -1, err
		}
	case st := <-statusCh:
		return int(st.StatusCode), nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
	return -1, nil
}

func (e *engineClient) CopyToVolume(ctx context.Context, volume, image, name string, content io.Reader, size int64) error {
	cfg := &container.Config{Image: image, Labels: map[string]string{ManagedLabel: "true"}}
	hostCfg := &container.HostConfig{Binds: []string{volume + ":/dpvol"}}
	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return err
	}
	defer func() {
		_ = e.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
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
	if err := e.cli.CopyToContainer(ctx, created.ID, "/dpvol", pr, types.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to volume: %w", err)
	}
	return nil
}

// ReadContainerFile reads a single regular file from a container's filesystem
// via CopyFromContainer (a tar stream with one entry), capped at 5 MiB.
func (e *engineClient) ReadContainerFile(ctx context.Context, containerID, path string) ([]byte, error) {
	tarStream, _, err := e.cli.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	defer func() { _ = tarStream.Close() }()
	tr := tar.NewReader(tarStream)
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

func (e *engineClient) CopyFileFromVolume(ctx context.Context, volume, image, file string) (io.ReadCloser, int64, error) {
	cfg := &container.Config{Image: image, Labels: map[string]string{ManagedLabel: "true"}}
	hostCfg := &container.HostConfig{Binds: []string{volume + ":/backup:ro"}}
	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, 0, err
	}
	cleanup := func() {
		_ = e.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}

	// CopyFromContainer returns the path as a tar stream; the file we want is
	// the single entry inside it.
	tarStream, _, err := e.cli.CopyFromContainer(ctx, created.ID, "/backup/"+file)
	if err != nil {
		cleanup()
		return nil, 0, wrapNotFound(err)
	}
	tr := tar.NewReader(tarStream)
	hdr, err := tr.Next()
	if err != nil {
		_ = tarStream.Close()
		cleanup()
		if errors.Is(err, io.EOF) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return &volumeFileReader{tr: tr, closer: tarStream, cleanup: cleanup}, hdr.Size, nil
}

// volumeFileReader streams one file out of a CopyFromContainer tar archive and,
// on Close, releases the tar stream and removes the helper container.
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
	rc, err := e.cli.ContainerLogs(context.Background(), id, container.LogsOptions{
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
	f := filters.NewArgs()
	f.Add("type", "container")
	// Stream all container lifecycle events and filter in Go: the subscriber
	// cheaply ignores any without an io.miabi.app label (see events.Subscriber).
	msgs, errs := e.cli.Events(ctx, dockerevents.ListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			return err
		case m := <-msgs:
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
	opts := image.PullOptions{}
	if auth != nil && (auth.Username != "" || auth.Password != "") {
		encoded, err := registry.EncodeAuthConfig(registry.AuthConfig{
			Username:      auth.Username,
			Password:      auth.Password,
			ServerAddress: auth.Server,
		})
		if err != nil {
			return fmt.Errorf("encode registry auth: %w", err)
		}
		opts.RegistryAuth = encoded
	}
	rc, err := e.cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	// Drain the pull progress stream so the pull completes.
	_, err = io.Copy(io.Discard, rc)
	return err
}

func (e *engineClient) TagImage(ctx context.Context, source, target string) error {
	return e.cli.ImageTag(ctx, source, target)
}

func (e *engineClient) PushImage(ctx context.Context, ref string, auth *RegistryAuth) error {
	opts := image.PushOptions{}
	if auth != nil && (auth.Username != "" || auth.Password != "") {
		encoded, err := registry.EncodeAuthConfig(registry.AuthConfig{
			Username:      auth.Username,
			Password:      auth.Password,
			ServerAddress: auth.Server,
		})
		if err != nil {
			return fmt.Errorf("encode registry auth: %w", err)
		}
		opts.RegistryAuth = encoded
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
	tar, err := archive.Tar(contextDir, archive.Uncompressed)
	if err != nil {
		return fmt.Errorf("tar build context: %w", err)
	}
	defer func() { _ = tar.Close() }()

	resp, err := e.cli.ImageBuild(ctx, tar, types.ImageBuildOptions{
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
	insp, _, err := e.cli.ImageInspectWithRaw(ctx, ref)
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
	_, _, err := e.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RemoveImage deletes a local image by reference. A not-found image is success
// (idempotent), so GC can prune a row whose image a prior sweep already removed.
func (e *engineClient) RemoveImage(ctx context.Context, ref string, force bool) error {
	_, err := e.cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil && errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// maxLogTail bounds how many historical log lines a single stream may replay on
// connect, so a request for "all" (or a huge number) against a chatty container
// can't flood the client or pin memory. It mirrors the web log buffer cap.
const maxLogTail = 5000

// sanitizeTail validates the caller-supplied tail count before it reaches the
// Docker API. Empty defaults to a small window; "all" and out-of-range numbers
// are clamped to maxLogTail; non-numeric garbage falls back to the default.
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
	rc, err := e.cli.ContainerLogs(ctx, id, container.LogsOptions{
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
	resp, err := e.cli.ContainerStats(ctx, id, true)
	if err != nil {
		return wrapNotFound(err)
	}
	defer func() { _ = resp.Body.Close() }()

	dec := json.NewDecoder(resp.Body)
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
	resp, err := e.cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return StatsSample{}, wrapNotFound(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var s container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
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
func createNetworkOptions(spec NetworkSpec) network.CreateOptions {
	driver := spec.Driver
	if driver == "" {
		driver = "bridge"
	}
	labels := map[string]string{ManagedLabel: "true"}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	opts := network.CreateOptions{
		Driver:     driver,
		Internal:   spec.Internal,
		Attachable: spec.Attachable,
		Labels:     labels,
	}
	if spec.Encrypted {
		opts.Options = map[string]string{"encrypted": ""}
	}
	if spec.Subnet != "" {
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{{Subnet: spec.Subnet, Gateway: spec.Gateway}}}
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
	if existing, err := e.cli.NetworkInspect(ctx, spec.Name, network.InspectOptions{}); err == nil {
		return existing.ID, nil
	}
	return e.CreateNetworkSpec(ctx, spec)
}

func (e *engineClient) RemoveNetwork(ctx context.Context, name string) error {
	err := e.cli.NetworkRemove(ctx, name)
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (e *engineClient) NetworkConnect(ctx context.Context, name, containerID string, aliases []string) error {
	err := e.cli.NetworkConnect(ctx, name, containerID, &network.EndpointSettings{Aliases: aliases})
	// Already attached: treat as success so reconciles are idempotent.
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return nil
	}
	return err
}

func (e *engineClient) NetworkDisconnect(ctx context.Context, name, containerID string, force bool) error {
	err := e.cli.NetworkDisconnect(ctx, name, containerID, force)
	// Not attached (or the network/container is gone): nothing to do.
	if errdefs.IsNotFound(err) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "is not connected")) {
		return nil
	}
	return err
}

func (e *engineClient) ListNetworks(ctx context.Context) ([]Network, error) {
	nets, err := e.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(nets))
	for _, n := range nets {
		subnet := ""
		if len(n.IPAM.Config) > 0 {
			subnet = n.IPAM.Config[0].Subnet
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
	// Record the declared capacity as a label. A *hard* size cap needs a sized
	// backing volume, which depends on the node's storage backend (XFS project
	// quotas, ZFS/btrfs dataset quotas) and is layered on separately. The label
	// makes the declared size visible to `docker volume inspect` and to quota
	// tracking; higher layers persist it on the resource for soft enforcement.
	if spec.SizeBytes > 0 {
		labels[LabelSizeBytes] = strconv.FormatInt(spec.SizeBytes, 10)
	}
	opts := volume.CreateOptions{Name: spec.Name, Labels: labels}
	// Shared-storage drivers (nfs/cifs/…) carry their backend in driver options;
	// an empty driver uses Docker's default (local). The NFS/CIFS case uses the
	// built-in local driver with mount options, so no external plugin is needed.
	if spec.Driver != "" {
		opts.Driver = spec.Driver
	}
	if len(spec.DriverOpts) > 0 {
		opts.DriverOpts = spec.DriverOpts
	}
	v, err := e.cli.VolumeCreate(ctx, opts)
	if err != nil {
		return Volume{}, err
	}
	return Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt}, nil
}

func (e *engineClient) ListVolumes(ctx context.Context) ([]Volume, error) {
	resp, err := e.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		out = append(out, Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, Labels: v.Labels})
	}
	return out, nil
}

func (e *engineClient) InspectVolume(ctx context.Context, name string) (Volume, error) {
	v, err := e.cli.VolumeInspect(ctx, name)
	if err != nil {
		return Volume{}, wrapNotFound(err)
	}
	return Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, Labels: v.Labels}, nil
}

func (e *engineClient) RemoveVolume(ctx context.Context, name string, force bool) error {
	return wrapNotFound(e.cli.VolumeRemove(ctx, name, force))
}

// --- helpers ---

func wrapNotFound(err error) error {
	if err != nil && errdefs.IsNotFound(err) {
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
