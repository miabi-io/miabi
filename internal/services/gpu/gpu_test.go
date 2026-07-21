// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpu

import (
	"testing"

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
	// An app requesting no GPU must pass preflight even with GPU support off and no
	// quota wired.
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
