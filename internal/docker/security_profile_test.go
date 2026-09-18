// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

// The regression this guards: a one-off Job had its security profile computed, applied to the spec,
// and then dropped on the floor by createOneShot — so it ran as the image's root, with default
// capabilities, on the app's own volumes and networks.
func TestApplyContainerSecurityHardensBothCreatePaths(t *testing.T) {
	spec := RunSpec{
		User:            "100000:0",
		CapDrop:         []string{"NET_RAW"},
		CapAdd:          []string{"NET_BIND_SERVICE"},
		GroupAdd:        []string{"1000"},
		ReadOnlyRootfs:  true,
		NoNewPrivileges: true,
	}
	cfg := &container.Config{}
	hostCfg := &container.HostConfig{}
	applyContainerSecurity(cfg, hostCfg, spec)

	if cfg.User != "100000:0" {
		t.Errorf("User = %q, want 100000:0", cfg.User)
	}
	if len(hostCfg.CapDrop) != 1 || hostCfg.CapDrop[0] != "NET_RAW" {
		t.Errorf("CapDrop = %v, want [NET_RAW]", hostCfg.CapDrop)
	}
	if len(hostCfg.CapAdd) != 1 || hostCfg.CapAdd[0] != "NET_BIND_SERVICE" {
		t.Errorf("CapAdd = %v", hostCfg.CapAdd)
	}
	if len(hostCfg.GroupAdd) != 1 || hostCfg.GroupAdd[0] != "1000" {
		t.Errorf("GroupAdd = %v", hostCfg.GroupAdd)
	}
	if !hostCfg.ReadonlyRootfs {
		t.Error("ReadonlyRootfs was not applied")
	}
	var nnp bool
	for _, o := range hostCfg.SecurityOpt {
		if o == "no-new-privileges" {
			nnp = true
		}
	}
	if !nnp {
		t.Errorf("SecurityOpt = %v, want no-new-privileges", hostCfg.SecurityOpt)
	}
}

// A platform helper (backup, DDL, netcheck, the storage-class and host-stats probes) passes none of
// these, and must keep running as the image's own user.
func TestApplyContainerSecurityLeavesPlatformHelpersAlone(t *testing.T) {
	cfg := &container.Config{}
	hostCfg := &container.HostConfig{}
	applyContainerSecurity(cfg, hostCfg, RunSpec{Image: "busybox:1.36"})

	if cfg.User != "" {
		t.Errorf("User = %q, want empty (the image's own user)", cfg.User)
	}
	if hostCfg.ReadonlyRootfs {
		t.Error("a helper must not be given a read-only rootfs it did not ask for")
	}
	if len(hostCfg.SecurityOpt) != 0 {
		t.Errorf("SecurityOpt = %v, want none", hostCfg.SecurityOpt)
	}
}
