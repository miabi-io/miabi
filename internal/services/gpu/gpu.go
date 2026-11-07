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
	"strconv"
	"strings"
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
}

func (e *NoDeviceError) Error() string {
	kind := e.Kind
	if kind == "" {
		kind = "any"
	}
	return fmt.Sprintf("node %q has %d enabled GPU(s) of kind %q, but the app requested %d", e.Node, e.Available, kind, e.Requested)
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
	devices *repositories.GPUDeviceRepository
	servers *repositories.ServerRepository
	apps    *repositories.ApplicationRepository
	clients NodeDocker
	quota   *quota.Service
	cfg     Config
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

// A node without the NVIDIA runtime is refused up front rather than failing cryptically at container start.
func (s *Service) ResolveDevices(ctx context.Context, app *models.Application, runtimes []string) ([]docker.GPURequest, error) {
	if app == nil || app.GPUCount <= 0 {
		return nil, nil
	}
	if !s.Enabled() {
		return nil, ErrGPUDisabled
	}
	node := s.nodeName(app.ServerID)
	if !hasRuntime(runtimes, s.cfg.NvidiaRuntime) {
		return nil, fmt.Errorf("node %q is not GPU-capable: the NVIDIA Container Toolkit (%q runtime) is not installed", node, s.cfg.NvidiaRuntime)
	}
	enabled, err := s.devices.ListEnabledByServer(app.ServerID)
	if err != nil {
		return nil, fmt.Errorf("list node GPUs: %w", err)
	}
	matched := filterByKind(enabled, app.GPUKind)
	if len(matched) < app.GPUCount {
		return nil, &NoDeviceError{Node: node, Kind: app.GPUKind, Requested: app.GPUCount, Available: len(matched)}
	}
	picked := matched[:app.GPUCount]
	ids := make([]string, len(picked))
	for i, d := range picked {
		ids[i] = d.UUID
	}
	return []docker.GPURequest{{DeviceIDs: ids, Capabilities: [][]string{{"gpu"}}}}, nil
}

// Devices lists every discovered device on a node (enabled or not).
func (s *Service) Devices(serverID uint) ([]models.GPUDevice, error) {
	return s.devices.ListByServer(serverID)
}

// Lets the admin UI explain a node with no listed GPUs.
func (s *Service) NodeCapable(ctx context.Context, serverID uint) bool {
	cli, err := s.clients.For(serverID)
	if err != nil {
		return false
	}
	info, err := cli.Info(ctx)
	if err != nil {
		return false
	}
	return hasRuntime(info.Runtimes, s.cfg.NvidiaRuntime)
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
		if _, err := s.InventoryNode(ctx, servers[i].ID); err != nil {
			logger.Warn("gpu inventory failed for node", "node", servers[i].Label(), "error", err)
		}
	}
	s.publishMetrics()
	return nil
}

// Most nodes have no GPU, so a missing runtime or failed probe yields zero devices, not an error; new devices arrive disabled.
func (s *Service) InventoryNode(ctx context.Context, serverID uint) (int, error) {
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
	if !hasRuntime(info.Runtimes, s.cfg.NvidiaRuntime) {
		return 0, nil // no toolkit → no GPUs to inventory
	}
	if err := cli.PullImage(ctx, s.cfg.ProbeImage, nil); err != nil {
		return 0, fmt.Errorf("pull GPU probe image %q: %w", s.cfg.ProbeImage, err)
	}
	spec := docker.RunSpec{
		Name:  fmt.Sprintf("mb-gpu-probe-%d-%d", serverID, time.Now().UnixNano()),
		Image: s.cfg.ProbeImage,
		Cmd:   []string{"nvidia-smi", "-q", "-x"},
		GPUs:  []docker.GPURequest{{Count: -1, Capabilities: [][]string{{"gpu"}}}}, // -1 = all
	}
	exit, out, err := cli.RunOneShot(ctx, spec)
	if err != nil {
		logger.Warn("gpu probe container failed", "node", serverID, "error", err)
		return 0, nil // fail soft: a probe error is not a node error
	}
	if exit != 0 {
		logger.Warn("gpu probe exited non-zero", "node", serverID, "exit", exit)
		return 0, nil
	}
	devs, err := parseNvidiaSMI(out)
	if err != nil {
		return 0, fmt.Errorf("parse nvidia-smi output: %w", err)
	}
	for i := range devs {
		if err := s.devices.Upsert(serverID, devs[i]); err != nil {
			logger.Warn("gpu device upsert failed", "uuid", devs[i].UUID, "error", err)
		}
	}
	return len(devs), nil
}

func (s *Service) publishMetrics() {
	if s == nil {
		return
	}
	total, _ := s.devices.CountAll()
	enabled, _ := s.devices.CountEnabled()
	alloc, _ := s.apps.SumRunningGPUs()
	metrics.SetGPUStats(int(total), int(enabled), int(alloc))
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

// An empty kind matches any; otherwise vendor, exact model, or case-insensitive model substring.
func filterByKind(devices []models.GPUDevice, kind string) []models.GPUDevice {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return devices
	}
	lower := strings.ToLower(kind)
	out := make([]models.GPUDevice, 0, len(devices))
	for _, d := range devices {
		if strings.EqualFold(string(d.Vendor), kind) ||
			strings.EqualFold(d.Model, kind) ||
			strings.Contains(strings.ToLower(d.Model), lower) {
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
			} `xml:"fb_memory_usage"`
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
			UUID:     uuid,
			Index:    index,
			Vendor:   models.GPUVendorNvidia,
			Model:    strings.TrimSpace(g.ProductName),
			MemoryMB: parseMiB(g.FB.Total),
		})
	}
	return out, nil
}

func parseMiB(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "MiB")
	s = strings.TrimSpace(s)
	n, _ := strconv.Atoi(s)
	return n
}
