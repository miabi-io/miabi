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

package gpu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const usageSMI = `<?xml version="1.0" ?>
<nvidia_smi_log>
  <gpu id="00000000:01:00.0">
    <product_name>NVIDIA L4</product_name>
    <uuid>GPU-l4-0</uuid>
    <minor_number>0</minor_number>
    <fb_memory_usage><total>23034 MiB</total><used>1200 MiB</used></fb_memory_usage>
    <utilization><gpu_util>37 %</gpu_util></utilization>
  </gpu>
  <gpu id="00000000:02:00.0">
    <product_name>NVIDIA L4</product_name>
    <uuid>GPU-l4-1</uuid>
    <minor_number>1</minor_number>
    <fb_memory_usage><total>23034 MiB</total><used>N/A</used></fb_memory_usage>
    <utilization><gpu_util>N/A</gpu_util></utilization>
  </gpu>
</nvidia_smi_log>`

type fakeNode struct {
	docker.Client
	runtimes   []string
	probeOut   string
	probeExit  int
	probeErr   error
	probes     int
	containers []docker.Container
}

func (f *fakeNode) Info(context.Context) (docker.Info, error) {
	return docker.Info{Runtimes: f.runtimes}, nil
}
func (f *fakeNode) ImageExists(context.Context, string) (bool, error) { return true, nil }
func (f *fakeNode) RunOneShot(context.Context, docker.RunSpec) (int, string, error) {
	f.probes++
	return f.probeExit, f.probeOut, f.probeErr
}
func (f *fakeNode) ListContainers(context.Context, bool) ([]docker.Container, error) {
	return f.containers, nil
}

type fakeClients struct{ node *fakeNode }

func (c fakeClients) For(uint) (docker.Client, error) { return c.node, nil }
func (c fakeClients) LocalID() uint                   { return 1 }

var testDBs = map[*repositories.GPUDeviceRepository]*gorm.DB{}

func devicesDB(t *testing.T, r *repositories.GPUDeviceRepository) *gorm.DB {
	t.Helper()
	db, ok := testDBs[r]
	if !ok {
		t.Fatal("no test db for repository")
	}
	return db
}

func newTestService(t *testing.T, node *fakeNode) (*Service, *repositories.GPUDeviceRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.GPUDevice{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	devices := repositories.NewGPUDeviceRepository(db)
	testDBs[devices] = db
	svc := NewService(devices, repositories.NewServerRepository(db), repositories.NewApplicationRepository(db), fakeClients{node}, Config{Enabled: true, ProbeImage: "probe"})
	return svc, devices
}

func TestParseNvidiaSMI_Usage(t *testing.T) {
	devs, err := parseNvidiaSMI(usageSMI)
	if err != nil || len(devs) != 2 {
		t.Fatalf("parse: %v, %d devices", err, len(devs))
	}
	if devs[0].UtilizationPct == nil || *devs[0].UtilizationPct != 37 || devs[0].MemoryUsedMB == nil || *devs[0].MemoryUsedMB != 1200 {
		t.Errorf("card 0 usage = %v / %v", devs[0].UtilizationPct, devs[0].MemoryUsedMB)
	}
	if devs[1].UtilizationPct != nil || devs[1].MemoryUsedMB != nil {
		t.Error("N/A readings must stay unknown, not 0")
	}
}

func TestInventory_SweepSkipsUnprovenNodeButRescanProbes(t *testing.T) {
	node := &fakeNode{probeOut: usageSMI}
	svc, devices := newTestService(t, node)

	if n, err := svc.InventoryNode(context.Background(), 1, false); n != 0 || err != nil || node.probes != 0 {
		t.Fatalf("the sweep must not probe a node with no runtime and no known cards: n=%d err=%v probes=%d", n, err, node.probes)
	}
	if n, err := svc.InventoryNode(context.Background(), 1, true); n != 2 || err != nil {
		t.Fatalf("a rescan probes regardless of the runtime: n=%d err=%v", n, err)
	}
	if n, _ := svc.InventoryNode(context.Background(), 1, false); n != 2 || node.probes != 2 {
		t.Errorf("once cards are known the sweep keeps probing the node, probes=%d", node.probes)
	}
	all, _ := devices.ListByServer(1)
	if len(all) != 2 || all[0].UtilizationPct == nil {
		t.Errorf("inventory should record cards with their usage, got %+v", all)
	}
}

func TestInventory_RescanReportsProbeFailure(t *testing.T) {
	node := &fakeNode{probeExit: 9, probeOut: "Failed to initialize NVML: Driver/library version mismatch"}
	svc, _ := newTestService(t, node)
	_, err := svc.InventoryNode(context.Background(), 1, true)
	if err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("a rescan must surface the probe's reason, got %v", err)
	}
	node.probeErr = errors.New("could not select device driver \"nvidia\"")
	if _, err := svc.InventoryNode(context.Background(), 1, true); err == nil || !strings.Contains(err.Error(), "NVIDIA Container Toolkit") {
		t.Errorf("a container that cannot get GPUs should point at the toolkit, got %v", err)
	}
}

func enableAll(t *testing.T, devices *repositories.GPUDeviceRepository) {
	t.Helper()
	all, _ := devices.ListByServer(1)
	for i := range all {
		all[i].Enabled = true
		if err := devices.Update(&all[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveDevices_ProvenNodeNeedsNoRuntime(t *testing.T) {
	node := &fakeNode{probeOut: usageSMI}
	svc, devices := newTestService(t, node)
	app := &models.Application{ID: 3, ServerID: 1, GPUCount: 1}

	if _, _, err := svc.ResolveDevices(context.Background(), app, node); err == nil || !strings.Contains(err.Error(), "rescan") {
		t.Fatalf("an unproven node without the runtime is refused with a rescan hint, got %v", err)
	}
	if _, err := svc.InventoryNode(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	enableAll(t, devices)
	reqs, release, err := svc.ResolveDevices(context.Background(), app, node)
	if err != nil || len(reqs) != 1 || len(reqs[0].DeviceIDs) != 1 {
		t.Fatalf("a probe-proven node deploys without the runtime: %v %+v", err, reqs)
	}
	release()
}

func TestNodeViewAndKinds(t *testing.T) {
	node := &fakeNode{probeOut: usageSMI, containers: []docker.Container{
		{Labels: map[string]string{docker.LabelApp: "7", docker.LabelGPUs: "GPU-l4-0"}},
		{Labels: map[string]string{docker.LabelApp: "7", docker.LabelJob: "12", docker.LabelGPUs: "GPU-l4-0"}},
		{Labels: map[string]string{docker.LabelApp: "7", docker.LabelGPUs: "GPU-l4-0"}},
	}}
	svc, devices := newTestService(t, node)
	if _, err := svc.InventoryNode(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Node(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Capable || view.NvidiaRuntime {
		t.Errorf("found cards make the node capable without the runtime: %+v", view)
	}
	if h := view.Devices[0].Holders; len(h) != 2 || h[0].AppID != 7 || h[1].Job != true {
		t.Errorf("card 0 holders should be app 7 once and its job once, got %+v", h)
	}
	if len(view.Devices[1].Holders) != 0 {
		t.Errorf("card 1 is free, got %+v", view.Devices[1].Holders)
	}

	if kinds, _ := svc.Kinds(1); len(kinds) != 0 {
		t.Errorf("disabled cards are not offered as kinds, got %+v", kinds)
	}
	enableAll(t, devices)
	kinds, _ := svc.Kinds(1)
	if len(kinds) != 1 || kinds[0].Model != "NVIDIA L4" || kinds[0].Count != 2 {
		t.Errorf("want one NVIDIA L4 kind with 2 cards, got %+v", kinds)
	}
}

func TestSampleUpdatesUsageOnly(t *testing.T) {
	node := &fakeNode{probeOut: usageSMI}
	svc, devices := newTestService(t, node)
	if _, err := svc.InventoryNode(context.Background(), 1, true); err != nil {
		t.Fatal(err)
	}
	enableAll(t, devices)
	node.probeOut = strings.Replace(usageSMI, "37 %", "91 %", 1)
	if err := svc.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, _ := devices.FindByUUID("GPU-l4-0")
	if d.UtilizationPct == nil || *d.UtilizationPct != 91 || !d.Enabled {
		t.Errorf("sample should refresh usage and keep admin flags, got util=%v enabled=%v", d.UtilizationPct, d.Enabled)
	}
}

type serverRow struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	DisplayName string
	ClusterID   uint
	Cordoned    bool
}

func (serverRow) TableName() string { return "servers" }

func TestCapacityWarningNamesNodesThatFit(t *testing.T) {
	node := &fakeNode{probeOut: usageSMI}
	svc, devices := newTestService(t, node)
	db := devicesDB(t, devices)
	if err := db.AutoMigrate(&serverRow{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&[]serverRow{
		{ID: 1, Name: "gpu-a", ClusterID: 2},
		{ID: 2, Name: "gpu-b", ClusterID: 2},
		{ID: 3, Name: "gpu-c", ClusterID: 2, Cordoned: true},
		{ID: 4, Name: "elsewhere", ClusterID: 9},
	})
	l4 := func(server uint, uuid string) {
		if err := devices.Upsert(server, models.GPUDevice{UUID: uuid, Vendor: models.GPUVendorNvidia, Model: "NVIDIA L4"}); err != nil {
			t.Fatal(err)
		}
		d, _ := devices.FindByUUID(uuid)
		d.Enabled = true
		_ = devices.Update(d)
	}
	l4(1, "a0")
	l4(2, "b0")
	l4(2, "b1")
	l4(3, "c0")
	l4(3, "c1")
	l4(4, "e0")
	l4(4, "e1")

	app := &models.Application{ServerID: 1, GPUCount: 1}
	if w := svc.CapacityWarning(app); w != "" {
		t.Errorf("a request the node holds needs no warning, got %q", w)
	}
	app.GPUCount = 2
	w := svc.CapacityWarning(app)
	if !strings.Contains(w, `node "gpu-a" has 1`) || !strings.Contains(w, "with enough: gpu-b.") {
		t.Errorf("should name only gpu-b (gpu-c is cordoned, elsewhere is another location), got %q", w)
	}
	app.GPUCount, app.GPUKind = 1, "H100"
	if w := svc.CapacityWarning(app); !strings.Contains(w, "No node in this location") {
		t.Errorf("no node has an H100, got %q", w)
	}
}
