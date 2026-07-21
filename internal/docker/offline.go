// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"io"
	"net"
)

// Offline returns a Client whose every operation fails with err. It lets a
// node-aware caller resolve a client unconditionally: critical operations
// surface the error, while best-effort cleanups (whose results are ignored)
// simply no-op when a node's agent is disconnected.
func Offline(err error) Client { return offlineClient{err: err} }

type offlineClient struct{ err error }

func (o offlineClient) Ping(context.Context) error         { return o.err }
func (o offlineClient) Info(context.Context) (Info, error) { return Info{}, o.err }
func (o offlineClient) ListContainers(context.Context, bool) ([]Container, error) {
	return nil, o.err
}
func (o offlineClient) InspectContainer(context.Context, string) (Container, error) {
	return Container{}, o.err
}
func (o offlineClient) InspectContainerConfig(context.Context, string) (ContainerConfig, error) {
	return ContainerConfig{}, o.err
}
func (o offlineClient) RunContainer(context.Context, RunSpec) (string, error) { return "", o.err }
func (o offlineClient) StartContainer(context.Context, string) error          { return o.err }
func (o offlineClient) StopContainer(context.Context, string, int) error      { return o.err }
func (o offlineClient) RestartContainer(context.Context, string, int) error   { return o.err }
func (o offlineClient) RemoveContainer(context.Context, string, bool) error   { return o.err }
func (o offlineClient) RunOneShot(context.Context, RunSpec) (int, string, error) {
	return -1, "", o.err
}
func (o offlineClient) RunOneShotStream(context.Context, RunSpec, func(LogLine) error) (int, error) {
	return -1, o.err
}
func (o offlineClient) ReadContainerFile(context.Context, string, string) ([]byte, error) {
	return nil, o.err
}
func (o offlineClient) CopyFileFromVolume(context.Context, string, string, string) (io.ReadCloser, int64, error) {
	return nil, 0, o.err
}
func (o offlineClient) CopyToVolume(context.Context, string, string, string, io.Reader, int64) error {
	return o.err
}
func (o offlineClient) DialNetwork(context.Context, string, string, string, int) (net.Conn, error) {
	return nil, o.err
}
func (o offlineClient) PullImage(context.Context, string, *RegistryAuth) error { return o.err }
func (o offlineClient) TagImage(context.Context, string, string) error         { return o.err }
func (o offlineClient) PushImage(context.Context, string, *RegistryAuth) error { return o.err }
func (o offlineClient) BuildImage(context.Context, string, string, string, func(LogLine) error) error {
	return o.err
}
func (o offlineClient) InspectImage(context.Context, string) (ImageInspect, error) {
	return ImageInspect{}, o.err
}
func (o offlineClient) ImageExists(context.Context, string) (bool, error)  { return false, o.err }
func (o offlineClient) RemoveImage(context.Context, string, bool) error    { return o.err }
func (o offlineClient) ListImages(context.Context) ([]Image, error)        { return nil, o.err }
func (o offlineClient) DiskUsage(context.Context) (DiskUsage, error)       { return DiskUsage{}, o.err }
func (o offlineClient) VolumeUsage(context.Context) ([]VolumeUsage, error) { return nil, o.err }
func (o offlineClient) PruneImages(context.Context, PruneImagesOptions) (PruneReport, error) {
	return PruneReport{}, o.err
}
func (o offlineClient) PruneBuildCache(context.Context) (PruneReport, error) {
	return PruneReport{}, o.err
}
func (o offlineClient) Exec(context.Context, string, ExecOptions) (ExecStream, error) {
	return nil, o.err
}
func (o offlineClient) Top(context.Context, string, string) (ProcessList, error) {
	return ProcessList{}, o.err
}
func (o offlineClient) StreamEvents(context.Context, func(EngineEvent) error) error { return o.err }
func (o offlineClient) StreamLogs(context.Context, string, bool, string, func(LogLine) error) error {
	return o.err
}
func (o offlineClient) StreamStats(context.Context, string, func(StatsSample) error) error {
	return o.err
}
func (o offlineClient) StatsOnce(context.Context, string) (StatsSample, error) {
	return StatsSample{}, o.err
}
func (o offlineClient) EnsureNetwork(context.Context, string) (string, error) { return "", o.err }
func (o offlineClient) CreateNetwork(context.Context, string, string, bool) (string, error) {
	return "", o.err
}
func (o offlineClient) CreateNetworkSpec(context.Context, NetworkSpec) (string, error) {
	return "", o.err
}
func (o offlineClient) EnsureNetworkSpec(context.Context, NetworkSpec) (string, error) {
	return "", o.err
}
func (o offlineClient) RemoveNetwork(context.Context, string) error     { return o.err }
func (o offlineClient) ListNetworks(context.Context) ([]Network, error) { return nil, o.err }
func (o offlineClient) NetworkConnect(context.Context, string, string, []string) error {
	return o.err
}
func (o offlineClient) NetworkDisconnect(context.Context, string, string, bool) error { return o.err }
func (o offlineClient) CreateVolume(context.Context, string, map[string]string, int64) (Volume, error) {
	return Volume{}, o.err
}
func (o offlineClient) CreateVolumeWith(context.Context, VolumeSpec) (Volume, error) {
	return Volume{}, o.err
}
func (o offlineClient) ListVolumes(context.Context) ([]Volume, error)         { return nil, o.err }
func (o offlineClient) InspectVolume(context.Context, string) (Volume, error) { return Volume{}, o.err }
func (o offlineClient) RemoveVolume(context.Context, string, bool) error      { return o.err }
func (o offlineClient) Swarm(context.Context) (SwarmInfo, error)              { return SwarmInfo{}, o.err }
func (o offlineClient) SwarmInit(context.Context, SwarmInitRequest) (string, error) {
	return "", o.err
}
func (o offlineClient) SwarmJoin(context.Context, SwarmJoinRequest) error { return o.err }
func (o offlineClient) SwarmLeave(context.Context, bool) error            { return o.err }
func (o offlineClient) SwarmJoinTokens(context.Context) (SwarmJoinTokens, error) {
	return SwarmJoinTokens{}, o.err
}
func (o offlineClient) SwarmNodes(context.Context) ([]SwarmNode, error)             { return nil, o.err }
func (o offlineClient) SwarmNodeRemove(context.Context, string, bool) error         { return o.err }
func (o offlineClient) SwarmNodeAvailability(context.Context, string, string) error { return o.err }
func (o offlineClient) SwarmTasks(context.Context, string) ([]SwarmTask, error) {
	return nil, o.err
}
func (o offlineClient) ServiceCreate(context.Context, ServiceSpec) (string, error) {
	return "", o.err
}
func (o offlineClient) ServiceUpdate(context.Context, string, ServiceSpec) error { return o.err }
func (o offlineClient) ServiceRemove(context.Context, string) error              { return o.err }
func (o offlineClient) ServiceScale(context.Context, string, uint64) error       { return o.err }
func (o offlineClient) ServiceInspect(context.Context, string) (ServiceStatus, error) {
	return ServiceStatus{}, o.err
}
func (o offlineClient) ServiceList(context.Context) ([]ServiceStatus, error) { return nil, o.err }
func (o offlineClient) ServiceRestart(context.Context, string) error         { return o.err }
func (o offlineClient) ServiceTaskContainerID(context.Context, string) (string, error) {
	return "", o.err
}
func (o offlineClient) ServiceEnv(context.Context, string) ([]string, error) {
	return nil, o.err
}
func (o offlineClient) StreamServiceLogs(context.Context, string, bool, string, func(LogLine) error) error {
	return o.err
}
func (o offlineClient) CreateOverlayNetwork(context.Context, string) (string, error) {
	return "", o.err
}
func (o offlineClient) Close() error { return nil }
