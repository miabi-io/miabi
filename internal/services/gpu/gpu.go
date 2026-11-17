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

// Package gpu inventories node GPUs over the Docker API, so the node agent needs no changes.
package gpu

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/metrics"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var ErrGPUDisabled = errors.New("GPU support is disabled on this platform")

var ErrDeviceNotOnNode = errors.New("gpu device does not belong to this node")

// NoDeviceError names the node and requested kind so the deploy log is actionable.
type NoDeviceError struct {
	Node      string
	Kind      string
	Requested int
	Available int
	Busy      int // matching dedicated cards held by another app
}

func (e *NoDeviceError) Error() string {
	kind := e.Kind
	if kind == "" {
		kind = "any"
	}
	msg := fmt.Sprintf("node %q has %d available GPU(s) of kind %q, but the app requested %d", e.Node, e.Available, kind, e.Requested)
	if e.Busy > 0 {
		msg += fmt.Sprintf(" (%d dedicated GPU(s) are held by other apps)", e.Busy)
	}
	return msg
}
func (e *NoDeviceError) Code() string { return "GPU_NO_DEVICE" }

type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// Config carries the GPU knobs from the platform config.
type Config struct {
	Enabled       bool
	NvidiaRuntime string // container runtime name that signals the NVIDIA toolkit
	ProbeImage    string // one-shot image the inventory probe runs nvidia-smi in
}

type Service struct {
	nodeLocks sync.Map // serverID -> *sync.Mutex
	devices   *repositories.GPUDeviceRepository
	servers   *repositories.ServerRepository
	apps      *repositories.ApplicationRepository
	clients   NodeDocker
	quota     *quota.Service
	cfg       Config
}

// NewService builds the GPU service. quota is wired separately via SetQuota.
func NewService(devices *repositories.GPUDeviceRepository, servers *repositories.ServerRepository, apps *repositories.ApplicationRepository, clients NodeDocker, cfg Config) *Service {
	if cfg.NvidiaRuntime == "" {
		cfg.NvidiaRuntime = "nvidia"
	}
	return &Service{devices: devices, servers: servers, apps: apps, clients: clients, cfg: cfg}
}

// nil skips capability and quota checks, matching the single-tenant no-gating stance.
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// Enabled reports whether GPU support is switched on.
func (s *Service) Enabled() bool { return s != nil && s.cfg.Enabled }

func (s *Service) Preflight(app *models.Application) error {
	if app == nil || app.GPUCount <= 0 {
		return nil
	}
	if !s.Enabled() {
		return ErrGPUDisabled
	}
	if s.quota != nil {
		if err := s.quota.Require(app.WorkspaceID, quota.CapGPU); err != nil {
			return err
		}
		if err := s.quota.CheckGPURequest(app.WorkspaceID, app.GPUCount*effReplicas(app), app.ID); err != nil {
			return err
		}
	}
	return nil
}

// The returned release must be called once the container is running: holders are read from running containers, so the lock covers the gap.
func (s *Service) ResolveDevices(ctx context.Context, app *models.Application, eng docker.Client) ([]docker.GPURequest, func(), error) {
	noop := func() {}
	if app == nil || app.GPUCount <= 0 {
		return nil, noop, nil
	}
	if !s.Enabled() {
		return nil, noop, ErrGPUDisabled
	}
	node := s.nodeName(app.ServerID)
	enabled, err := s.devices.ListEnabledByServer(app.ServerID)
	if err != nil {
		return nil, noop, fmt.Errorf("list node GPUs: %w", err)
	}
	// A device row exists only after a probe attached GPUs through this same request path, so it outranks the runtime name.
	if len(enabled) == 0 {
		if info, err := eng.Info(ctx); err == nil && !hasRuntime(info.Runtimes, s.cfg.NvidiaRuntime) {
			return nil, noop, fmt.Errorf("node %q is not GPU-capable: no GPU has been found on it and the %q runtime is not registered; install the NVIDIA Container Toolkit, then rescan the node's GPUs", node, s.cfg.NvidiaRuntime)
		}
	}
	matched := filterByKind(enabled, app.GPUKind)

	release := s.lockNode(app.ServerID)
	containers, err := eng.ListContainers(ctx, false)
	if err != nil {
		release()
		return nil, noop, fmt.Errorf("list node containers for GPU holders: %w", err)
	}
	picked, busy := pickDevices(matched, app.GPUCount, gpuHolders(containers, app.ID))
	if len(picked) < app.GPUCount {
		release()
		return nil, noop, &NoDeviceError{Node: node, Kind: app.GPUKind, Requested: app.GPUCount, Available: len(picked), Busy: busy}
	}
	ids := make([]string, len(picked))
	for i, d := range picked {
		ids[i] = d.UUID
	}
	return []docker.GPURequest{{DeviceIDs: ids, Capabilities: [][]string{deviceCapabilities(app.GPUCapabilities)}}}, release, nil
}

// dockerd sets NVIDIA_DRIVER_CAPABILITIES to exactly the requested set, so asking for video alone would drop compute and utility.
func deviceCapabilities(extra []string) []string {
	if len(extra) == 0 {
		return []string{"gpu"}
	}
	out := []string{"gpu", "compute", "utility"}
	for _, c := range extra {
		if c != "compute" && c != "utility" {
			out = append(out, c)
		}
	}
	return out
}

// Per process only: two worker processes deploying to the same node can still race for one dedicated card.
func (s *Service) lockNode(serverID uint) func() {
	v, _ := s.nodeLocks.LoadOrStore(serverID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	var once sync.Once
	return func() { once.Do(mu.Unlock) }
}

// cardHolders maps a device UUID to how many other apps hold it and whether the requesting app does.
type cardHolders struct {
	others map[string]int
	own    map[string]bool
}

func gpuHolders(containers []docker.Container, appID uint) cardHolders {
	h := cardHolders{others: map[string]int{}, own: map[string]bool{}}
	seen := map[string]map[string]bool{}
	self := strconv.FormatUint(uint64(appID), 10)
	for _, c := range containers {
		raw := c.Labels[docker.LabelGPUs]
		if raw == "" {
			continue
		}
		holder := c.Labels[docker.LabelApp]
		for _, uuid := range docker.ParseGPULabel(raw) {
			if holder == self {
				h.own[uuid] = true
				continue
			}
			if seen[uuid] == nil {
				seen[uuid] = map[string]bool{}
			}
			if !seen[uuid][holder] {
				seen[uuid][holder] = true
				h.others[uuid]++
			}
		}
	}
	return h
}

// Least-loaded first, then cards the app already holds so a canary or redeploy stays put; dedicated cards held by another app are skipped.
func pickDevices(devices []models.GPUDevice, count int, h cardHolders) ([]models.GPUDevice, int) {
	free := make([]models.GPUDevice, 0, len(devices))
	busy := 0
	for _, d := range devices {
		if !d.Shared && h.others[d.UUID] > 0 {
			busy++
			continue
		}
		free = append(free, d)
	}
	sort.SliceStable(free, func(i, j int) bool {
		a, b := free[i], free[j]
		if h.others[a.UUID] != h.others[b.UUID] {
			return h.others[a.UUID] < h.others[b.UUID]
		}
		return h.own[a.UUID] && !h.own[b.UUID]
	})
	if len(free) > count {
		free = free[:count]
	}
	return free, busy
}

func (s *Service) SetDevice(serverID, gpuID uint, enabled, shared *bool) (*models.GPUDevice, error) {
	d, err := s.devices.FindByID(gpuID)
	if err != nil {
		return nil, err
	}
	if d.ServerID != serverID {
		return nil, ErrDeviceNotOnNode
	}
	if enabled != nil {
		d.Enabled = *enabled
	}
	if shared != nil {
		d.Shared = *shared
	}
	if err := s.devices.Update(d); err != nil {
		return nil, err
	}
	s.publishMetrics()
	return d, nil
}

// A per-node probe failure must never fail the whole sweep.
func (s *Service) Inventory(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return err
	}
	for i := range servers {
		if _, err := s.InventoryNode(ctx, servers[i].ID, false); err != nil {
			logger.Warn("gpu inventory failed for node", "node", servers[i].Label(), "error", err)
		}
	}
	s.publishMetrics()
	return nil
}

// force (an admin rescan) probes a node without the nvidia runtime and reports probe failures; the sweep skips such nodes and stays quiet.
func (s *Service) InventoryNode(ctx context.Context, serverID uint, force bool) (int, error) {
	if !s.Enabled() {
		return 0, ErrGPUDisabled
	}
	cli, err := s.clients.For(serverID)
	if err != nil {
		return 0, err
	}
	info, err := cli.Info(ctx)
	if err != nil {
		return 0, err
	}
	if !force && !hasRuntime(info.Runtimes, s.cfg.NvidiaRuntime) && !s.hasKnownDevices(serverID) {
		return 0, nil
	}
	devs, err := s.probe(ctx, cli, serverID)
	if err != nil {
		if force {
			return 0, err
		}
		logger.Warn("gpu probe failed", "node", serverID, "error", err)
		return 0, nil
	}
	seen := make([]string, 0, len(devs))
	for i := range devs {
		if err := s.devices.Upsert(serverID, devs[i]); err != nil {
			logger.Warn("gpu device upsert failed", "uuid", devs[i].UUID, "error", err)
			continue
		}
		seen = append(seen, devs[i].UUID)
	}
	if len(seen) == len(devs) {
		if n, err := s.devices.MarkMissing(serverID, seen); err != nil {
			logger.Warn("gpu missing-device sweep failed", "node", serverID, "error", err)
		} else if n > 0 {
			logger.Warn("gpu devices no longer reported by the node", "node", serverID, "count", n)
		}
	}
	if force {
		s.publishMetrics()
	}
	return len(devs), nil
}

func (s *Service) hasKnownDevices(serverID uint) bool {
	devices, err := s.devices.ListByServer(serverID)
	if err != nil {
		return false
	}
	for _, d := range devices {
		if !d.Missing {
			return true
		}
	}
	return false
}

// Runs nvidia-smi with every card attached, through the same device-request path a deploy uses.
func (s *Service) probe(ctx context.Context, cli docker.Client, serverID uint) ([]models.GPUDevice, error) {
	if present, _ := cli.ImageExists(ctx, s.cfg.ProbeImage); !present {
		if err := cli.PullImage(ctx, s.cfg.ProbeImage, nil); err != nil {
			return nil, fmt.Errorf("pull GPU probe image %q: %w", s.cfg.ProbeImage, err)
		}
	}
	spec := docker.RunSpec{
		Name:  fmt.Sprintf("mb-gpu-probe-%d-%d", serverID, time.Now().UnixNano()),
		Image: s.cfg.ProbeImage,
		Cmd:   []string{"nvidia-smi", "-q", "-x"},
		GPUs:  []docker.GPURequest{{Count: -1, Capabilities: [][]string{{"gpu"}}}}, // -1 = all
	}
	exit, out, err := cli.RunOneShot(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("GPU probe container could not run (is the NVIDIA Container Toolkit installed?): %w", err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("GPU probe exited with code %d: %s", exit, lastLine(out))
	}
	devs, err := parseNvidiaSMI(out)
	if err != nil {
		return nil, fmt.Errorf("parse nvidia-smi output: %w", err)
	}
	return devs, nil
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Sample refreshes utilization and memory use on nodes that offer GPUs; it never adds or removes devices.
func (s *Service) Sample(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	ids, err := s.devices.ServersWithEnabled()
	if err != nil {
		return err
	}
	for _, id := range ids {
		cli, err := s.clients.For(id)
		if err != nil {
			continue
		}
		devs, err := s.probe(ctx, cli, id)
		if err != nil {
			logger.Debug("gpu sample failed", "node", id, "error", err)
			continue
		}
		now := time.Now()
		for _, d := range devs {
			if err := s.devices.UpdateUsage(id, d.UUID, d.UtilizationPct, d.MemoryUsedMB, now); err != nil {
				logger.Warn("gpu usage update failed", "uuid", d.UUID, "error", err)
			}
		}
	}
	s.publishMetrics()
	return nil
}

// CapacityWarning is empty when the app's node holds its GPU request; a deploy would otherwise fail there, so it names the location's nodes that could.
func (s *Service) CapacityWarning(app *models.Application) string {
	if !s.Enabled() || app == nil || app.GPUCount <= 0 || app.RuntimeKind == models.RuntimeService {
		return ""
	}
	have := s.matchingCards(app.ServerID, app.GPUKind)
	if have >= app.GPUCount {
		return ""
	}
	kind := app.GPUKind
	if strings.TrimSpace(kind) == "" {
		kind = "any"
	}
	msg := fmt.Sprintf("node %q has %d enabled GPU(s) of kind %q, but the app requests %d, so its deploy will fail there",
		s.nodeName(app.ServerID), have, kind, app.GPUCount)
	if others := s.nodesWithCards(app, kind); len(others) > 0 {
		return msg + ". Nodes in this location with enough: " + strings.Join(others, ", ") + ". Ask a platform admin to move the app, or create it again in this location"
	}
	return msg + ". No node in this location has enough; ask a platform admin to enable more cards"
}

func (s *Service) matchingCards(serverID uint, kind string) int {
	devices, err := s.devices.ListEnabledByServer(serverID)
	if err != nil {
		return 0
	}
	return len(filterByKind(devices, kind))
}

func (s *Service) nodesWithCards(app *models.Application, kind string) []string {
	cur, err := s.servers.FindByID(app.ServerID)
	if err != nil {
		return nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil
	}
	if kind == "any" {
		kind = ""
	}
	var out []string
	for i := range servers {
		srv := &servers[i]
		if srv.ID == app.ServerID || srv.ClusterID != cur.ClusterID || srv.Cordoned {
			continue
		}
		if s.matchingCards(srv.ID, kind) >= app.GPUCount {
			out = append(out, srv.Label())
		}
	}
	return out
}

type Kind struct {
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Count    int    `json:"count"`
	MemoryMB int    `json:"memory_mb"`
}

// Kinds groups a node's enabled, present cards by model; tenants see models and counts, never UUIDs or holders.
func (s *Service) Kinds(serverID uint) ([]Kind, error) {
	devices, err := s.devices.ListEnabledByServer(serverID)
	if err != nil {
		return nil, err
	}
	out := []Kind{}
	at := map[string]int{}
	for _, d := range devices {
		key := string(d.Vendor) + "\x00" + d.Model
		if i, ok := at[key]; ok {
			out[i].Count++
			continue
		}
		at[key] = len(out)
		out = append(out, Kind{Vendor: string(d.Vendor), Model: d.Model, Count: 1, MemoryMB: d.MemoryMB})
	}
	return out, nil
}

type Holder struct {
	AppID       uint   `json:"app_id"`
	AppName     string `json:"app_name"`
	WorkspaceID uint   `json:"workspace_id"`
	Job         bool   `json:"job,omitempty"`
}

type DeviceView struct {
	models.GPUDevice
	Holders []Holder `json:"holders"`
}

type NodeView struct {
	// Capable is true when the nvidia runtime is registered or a probe has already found cards.
	Capable       bool         `json:"toolkit_present"`
	NvidiaRuntime bool         `json:"nvidia_runtime"`
	Devices       []DeviceView `json:"devices"`
	// HoldersUnknown is set when the node could not be asked which containers hold its cards.
	HoldersUnknown bool `json:"holders_unknown,omitempty"`
}

// An offline node still lists its devices, just without holders.
func (s *Service) Node(ctx context.Context, serverID uint) (NodeView, error) {
	devices, err := s.devices.ListByServer(serverID)
	if err != nil {
		return NodeView{}, err
	}
	view := NodeView{Devices: make([]DeviceView, len(devices))}
	for i, d := range devices {
		view.Devices[i] = DeviceView{GPUDevice: d, Holders: []Holder{}}
		if !d.Missing {
			view.Capable = true
		}
	}
	cli, err := s.clients.For(serverID)
	if err != nil {
		view.HoldersUnknown = true
		return view, nil
	}
	if info, err := cli.Info(ctx); err == nil && hasRuntime(info.Runtimes, s.cfg.NvidiaRuntime) {
		view.NvidiaRuntime, view.Capable = true, true
	}
	containers, err := cli.ListContainers(ctx, false)
	if err != nil {
		view.HoldersUnknown = true
		return view, nil
	}
	byUUID := s.holdersByUUID(containers)
	for i := range view.Devices {
		if hs := byUUID[view.Devices[i].UUID]; len(hs) > 0 {
			view.Devices[i].Holders = hs
		}
	}
	return view, nil
}

func (s *Service) holdersByUUID(containers []docker.Container) map[string][]Holder {
	out := map[string][]Holder{}
	seen := map[string]bool{}
	names := map[uint]*models.Application{}
	for _, c := range containers {
		raw := c.Labels[docker.LabelGPUs]
		if raw == "" {
			continue
		}
		id, err := strconv.ParseUint(c.Labels[docker.LabelApp], 10, 64)
		if err != nil {
			continue
		}
		appID := uint(id)
		job := c.Labels[docker.LabelJob] != ""
		app, ok := names[appID]
		if !ok {
			app, _ = s.apps.FindByID(appID)
			names[appID] = app
		}
		h := Holder{AppID: appID, Job: job}
		if app != nil {
			h.AppName, h.WorkspaceID = app.Name, app.WorkspaceID
		}
		for _, uuid := range docker.ParseGPULabel(raw) {
			key := fmt.Sprintf("%s/%d/%v", uuid, appID, job)
			if seen[key] {
				continue
			}
			seen[key] = true
			out[uuid] = append(out[uuid], h)
		}
	}
	return out
}

// PublishMetrics refreshes this process's GPU gauges from the database, so every replica's /metrics agrees.
func (s *Service) PublishMetrics() error {
	if !s.Enabled() {
		return nil
	}
	s.publishMetrics()
	return nil
}

func (s *Service) publishMetrics() {
	if s == nil {
		return
	}
	devices, err := s.devices.ListAll()
	if err != nil {
		return
	}
	names := map[uint]string{}
	samples := make([]metrics.GPUDeviceSample, 0, len(devices))
	for _, d := range devices {
		name, ok := names[d.ServerID]
		if !ok {
			name = s.nodeName(d.ServerID)
			names[d.ServerID] = name
		}
		samples = append(samples, metrics.GPUDeviceSample{
			Server: name, Vendor: string(d.Vendor), UUID: d.UUID, Model: d.Model,
			Enabled: d.Enabled && !d.Missing, UtilizationPct: d.UtilizationPct, MemoryUsedMB: d.MemoryUsedMB, MemoryTotalMB: d.MemoryMB,
		})
	}
	alloc, _ := s.apps.SumRunningGPUsPerWorkspace()
	metrics.SetGPUStats(samples, alloc)
}

func (s *Service) nodeName(serverID uint) string {
	if srv, err := s.servers.FindByID(serverID); err == nil && srv.Name != "" {
		return srv.Name
	}
	return fmt.Sprintf("#%d", serverID)
}

// A GPU app is single-container, so this is normally 1.
func effReplicas(app *models.Application) int {
	if app.Replicas < 1 {
		return 1
	}
	return app.Replicas
}

func hasRuntime(runtimes []string, name string) bool {
	for _, r := range runtimes {
		if r == name {
			return true
		}
	}
	return false
}

func filterByKind(devices []models.GPUDevice, kind string) []models.GPUDevice {
	out := make([]models.GPUDevice, 0, len(devices))
	for _, d := range devices {
		if d.MatchesKind(kind) {
			out = append(out, d)
		}
	}
	return out
}

// Tolerates leading non-XML noise: some drivers print warnings first.
func parseNvidiaSMI(raw string) ([]models.GPUDevice, error) {
	if i := strings.Index(raw, "<?xml"); i > 0 {
		raw = raw[i:]
	} else if i := strings.Index(raw, "<nvidia_smi_log"); i > 0 {
		raw = raw[i:]
	}
	var doc struct {
		XMLName xml.Name `xml:"nvidia_smi_log"`
		GPUs    []struct {
			UUID        string `xml:"uuid"`
			ProductName string `xml:"product_name"`
			MinorNumber string `xml:"minor_number"`
			FB          struct {
				Total string `xml:"total"`
				Used  string `xml:"used"`
			} `xml:"fb_memory_usage"`
			Utilization struct {
				GPU string `xml:"gpu_util"`
			} `xml:"utilization"`
		} `xml:"gpu"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	out := make([]models.GPUDevice, 0, len(doc.GPUs))
	for i, g := range doc.GPUs {
		uuid := strings.TrimSpace(g.UUID)
		if uuid == "" {
			continue
		}
		index := i
		if n, err := strconv.Atoi(strings.TrimSpace(g.MinorNumber)); err == nil {
			index = n
		}
		out = append(out, models.GPUDevice{
			UUID:           uuid,
			Index:          index,
			Vendor:         models.GPUVendorNvidia,
			Model:          strings.TrimSpace(g.ProductName),
			MemoryMB:       parseMiB(g.FB.Total),
			UtilizationPct: parseReading(g.Utilization.GPU, "%"),
			MemoryUsedMB:   parseReading(g.FB.Used, "MiB"),
		})
	}
	return out, nil
}

// nvidia-smi prints "N/A" for readings a card does not support, which must stay unknown rather than read as 0.
func parseReading(s, unit string) *int {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), unit))
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

func parseMiB(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "MiB")
	s = strings.TrimSpace(s)
	n, _ := strconv.Atoi(s)
	return n
}
