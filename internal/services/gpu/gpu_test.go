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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

const sampleSMI = `<?xml version="1.0" ?>
<!DOCTYPE nvidia_smi_log SYSTEM "nvsmi_device_v12.dtd">
<nvidia_smi_log>
  <driver_version>550.54.14</driver_version>
  <attached_gpus>2</attached_gpus>
  <gpu id="00000000:01:00.0">
    <product_name>NVIDIA A100-SXM4-40GB</product_name>
    <uuid>GPU-aaaa1111-bbbb-cccc-dddd-eeeeeeeeeeee</uuid>
    <minor_number>0</minor_number>
    <fb_memory_usage>
      <total>40960 MiB</total>
      <used>0 MiB</used>
    </fb_memory_usage>
  </gpu>
  <gpu id="00000000:02:00.0">
    <product_name>NVIDIA A100-SXM4-40GB</product_name>
    <uuid>GPU-ffff2222-bbbb-cccc-dddd-eeeeeeeeeeee</uuid>
    <minor_number>1</minor_number>
    <fb_memory_usage>
      <total>40960 MiB</total>
    </fb_memory_usage>
  </gpu>
</nvidia_smi_log>`

func TestParseNvidiaSMI(t *testing.T) {
	// A leading warning line (some drivers emit these) must be tolerated.
	devs, err := parseNvidiaSMI("WARNING: infoROM is corrupted\n" + sampleSMI)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}
	d := devs[0]
	if d.UUID != "GPU-aaaa1111-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("uuid: %q", d.UUID)
	}
	if d.Model != "NVIDIA A100-SXM4-40GB" {
		t.Errorf("model: %q", d.Model)
	}
	if d.MemoryMB != 40960 {
		t.Errorf("memory: %d", d.MemoryMB)
	}
	if d.Index != 0 || devs[1].Index != 1 {
		t.Errorf("index: %d, %d", d.Index, devs[1].Index)
	}
	if d.Vendor != models.GPUVendorNvidia {
		t.Errorf("vendor: %q", d.Vendor)
	}
}

func TestParseNvidiaSMI_Empty(t *testing.T) {
	devs, err := parseNvidiaSMI("<nvidia_smi_log></nvidia_smi_log>")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(devs) != 0 {
		t.Fatalf("want 0 devices, got %d", len(devs))
	}
}

func TestFilterByKind(t *testing.T) {
	devices := []models.GPUDevice{
		{UUID: "a", Vendor: models.GPUVendorNvidia, Model: "NVIDIA A100-SXM4-40GB"},
		{UUID: "b", Vendor: models.GPUVendorNvidia, Model: "NVIDIA RTX 4090"},
	}
	cases := []struct {
		kind string
		want int
	}{
		{"", 2},                // any
		{"nvidia", 2},          // vendor match
		{"A100", 1},            // model substring
		{"rtx 4090", 1},        // case-insensitive substring
		{"NVIDIA RTX 4090", 1}, // exact model
		{"amd", 0},             // no match
	}
	for _, tc := range cases {
		if got := len(filterByKind(devices, tc.kind)); got != tc.want {
			t.Errorf("filterByKind(%q) = %d, want %d", tc.kind, got, tc.want)
		}
	}
}

func TestParseMiB(t *testing.T) {
	cases := map[string]int{
		"40960 MiB":  40960,
		"  8192 MiB": 8192,
		"0 MiB":      0,
		"":           0,
		"garbage":    0,
	}
	for in, want := range cases {
		if got := parseMiB(in); got != want {
			t.Errorf("parseMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestHasRuntime(t *testing.T) {
	if !hasRuntime([]string{"runc", "nvidia"}, "nvidia") {
		t.Error("expected nvidia present")
	}
	if hasRuntime([]string{"runc"}, "nvidia") {
		t.Error("expected nvidia absent")
	}
}

func TestPreflight_NoGPUIsNoop(t *testing.T) {
	s := &Service{cfg: Config{Enabled: false}}
	if err := s.Preflight(&models.Application{GPUCount: 0}); err != nil {
		t.Errorf("no-gpu preflight should be a no-op, got %v", err)
	}
}

func TestPreflight_DisabledPlatform(t *testing.T) {
	s := &Service{cfg: Config{Enabled: false}}
	if err := s.Preflight(&models.Application{GPUCount: 1}); err != ErrGPUDisabled {
		t.Errorf("want ErrGPUDisabled, got %v", err)
	}
}

func dev(uuid string, index int, shared bool) models.GPUDevice {
	return models.GPUDevice{UUID: uuid, Index: index, Vendor: models.GPUVendorNvidia, Model: "NVIDIA A10", Enabled: true, Shared: shared}
}

func gpuContainer(appID, uuids string) docker.Container {
	return docker.Container{Labels: map[string]string{docker.LabelApp: appID, docker.LabelGPUs: uuids}}
}

func uuids(devs []models.GPUDevice) []string {
	out := make([]string, len(devs))
	for i, d := range devs {
		out[i] = d.UUID
	}
	return out
}

func TestGPUHolders(t *testing.T) {
	h := gpuHolders([]docker.Container{
		gpuContainer("7", "GPU-a,GPU-b"),
		gpuContainer("7", "GPU-a"),
		gpuContainer("8", " GPU-a "),
		gpuContainer("9", "GPU-c"),
		{Labels: map[string]string{docker.LabelApp: "10"}},
	}, 9)
	if h.others["GPU-a"] != 2 {
		t.Errorf("GPU-a held by apps 7 and 8 should count 2 distinct apps, got %d", h.others["GPU-a"])
	}
	if h.others["GPU-b"] != 1 {
		t.Errorf("GPU-b others = %d, want 1", h.others["GPU-b"])
	}
	if h.others["GPU-c"] != 0 || !h.own["GPU-c"] {
		t.Errorf("GPU-c is the requesting app's own card, got others=%d own=%v", h.others["GPU-c"], h.own["GPU-c"])
	}
}

func TestPickDevices_SpreadsAcrossCards(t *testing.T) {
	devs := []models.GPUDevice{dev("GPU-0", 0, true), dev("GPU-1", 1, true), dev("GPU-2", 2, true)}
	h := gpuHolders([]docker.Container{gpuContainer("1", "GPU-0"), gpuContainer("2", "GPU-0"), gpuContainer("3", "GPU-1")}, 99)
	picked, busy := pickDevices(devs, 2, h)
	if got := uuids(picked); len(got) != 2 || got[0] != "GPU-2" || got[1] != "GPU-1" {
		t.Errorf("want least-loaded [GPU-2 GPU-1], got %v", got)
	}
	if busy != 0 {
		t.Errorf("shared cards are never busy, got %d", busy)
	}
}

func TestPickDevices_DedicatedHeldByOtherAppIsSkipped(t *testing.T) {
	devs := []models.GPUDevice{dev("GPU-0", 0, false), dev("GPU-1", 1, false)}
	h := gpuHolders([]docker.Container{gpuContainer("1", "GPU-0")}, 2)
	picked, busy := pickDevices(devs, 2, h)
	if got := uuids(picked); len(got) != 1 || got[0] != "GPU-1" {
		t.Errorf("want only the free dedicated card GPU-1, got %v", got)
	}
	if busy != 1 {
		t.Errorf("busy = %d, want 1", busy)
	}
}

func TestPickDevices_OwnDedicatedCardIsReusable(t *testing.T) {
	devs := []models.GPUDevice{dev("GPU-0", 0, false), dev("GPU-1", 1, false)}
	h := gpuHolders([]docker.Container{gpuContainer("5", "GPU-1")}, 5)
	picked, busy := pickDevices(devs, 1, h)
	if got := uuids(picked); len(got) != 1 || got[0] != "GPU-1" {
		t.Errorf("a canary or redeploy should stay on the app's own card GPU-1, got %v", got)
	}
	if busy != 0 {
		t.Errorf("busy = %d, want 0", busy)
	}
}

func TestNoDeviceError_MentionsBusyCards(t *testing.T) {
	msg := (&NoDeviceError{Node: "gpu-1", Requested: 2, Available: 1, Busy: 1}).Error()
	if !strings.Contains(msg, `kind "any"`) || !strings.Contains(msg, "1 dedicated GPU(s) are held by other apps") {
		t.Errorf("unexpected message: %s", msg)
	}
}
