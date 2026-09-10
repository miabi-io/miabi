// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeCapabilitiesAcceptsTheAllowList(t *testing.T) {
	got, err := NormalizeCapabilities([]string{" net_admin ", "CAP_SYS_PTRACE", "NET_ADMIN"})
	if err != nil {
		t.Fatalf("NormalizeCapabilities: %v", err)
	}
	// De-duplicated, CAP_-stripped, upper-cased and sorted, so a no-op save reads
	// as a no-op rather than as a change.
	want := []string{"NET_ADMIN", "SYS_PTRACE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Anything not on the list is refused. A capability the platform has not
// considered is not one it can reason about.
func TestNormalizeCapabilitiesRefusesUnknown(t *testing.T) {
	for _, bad := range []string{"SYS_BOOT", "ALL", "CAP_ALL", "NET_ADMIN; DROP", "sys admin"} {
		if _, err := NormalizeCapabilities([]string{bad}); !errors.Is(err, ErrCapabilityUnknown) {
			t.Errorf("%q was accepted (err = %v)", bad, err)
		}
	}
}

func TestCapabilityTiersMatchTheirBlastRadius(t *testing.T) {
	// Mounting is escape, so anything that permits it is elevated.
	for _, name := range []string{"SYS_ADMIN", "SYS_MODULE", "SYS_RAWIO", "DAC_READ_SEARCH", "MKNOD"} {
		c, ok := LookupCapability(name)
		if !ok {
			t.Fatalf("%s is missing from the allow-list", name)
		}
		if c.Tier != TierElevated {
			t.Errorf("%s is tier %d, want elevated", name, c.Tier)
		}
	}
	// The everyday ones must not require the system workspace, or nobody can use
	// the feature for the thing it was built for.
	for _, name := range []string{"NET_ADMIN", "SYS_PTRACE", "NET_BIND_SERVICE"} {
		c, _ := LookupCapability(name)
		if c.Tier != TierCommon {
			t.Errorf("%s is tier %d, want common", name, c.Tier)
		}
	}
	if got := HighestCapabilityTier([]string{"NET_ADMIN", "SYS_ADMIN"}); got != TierElevated {
		t.Errorf("mixed set reported tier %d, want elevated — the highest decides", got)
	}
	if got := HighestCapabilityTier(nil); got != TierCommon {
		t.Errorf("empty set reported tier %d, want common", got)
	}
}

func TestEveryCapabilityIsExplained(t *testing.T) {
	for _, c := range Capabilities() {
		if len(c.Help) < 20 {
			t.Errorf("%s has no usable explanation (%q) — it is shown next to the control", c.Name, c.Help)
		}
	}
}

func TestNormalizeDevicesAcceptsTheAllowList(t *testing.T) {
	got, err := NormalizeDevices([]string{"/dev/ttyUSB0", " /dev/net/tun ", "/dev/net/tun"})
	if err != nil {
		t.Fatalf("NormalizeDevices: %v", err)
	}
	want := []string{"/dev/net/tun", "/dev/ttyUSB0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A raw block device is the host's filesystem. Granting one defeats the entire
// point of granting a capability instead of the host.
func TestNormalizeDevicesRefusesBlockDevices(t *testing.T) {
	for _, bad := range []string{"/dev/sda", "/dev/sda1", "/dev/nvme0n1", "/dev/vda", "/dev/dm-0", "/dev/mapper/root", "/dev/mem", "/dev/kmsg"} {
		if _, err := NormalizeDevices([]string{bad}); !errors.Is(err, ErrDeviceBlockDevice) {
			t.Errorf("%q was not refused as a block device (err = %v)", bad, err)
		}
	}
}

func TestNormalizeDevicesRefusesEscapes(t *testing.T) {
	cases := []string{
		"/etc/shadow",               // not under /dev at all
		"/dev/../etc/shadow",        // climbing out
		"/dev/net/tun:/dev/sda:rwm", // a triplet smuggling a different target
		"/dev/net",                  // a prefix of an allowed path, not the path
		"/dev/ttyUSB",               // the bare prefix with no unit
		"/dev/random",               // real, harmless, and still not on the list
	}
	for _, bad := range cases {
		if _, err := NormalizeDevices([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestDeviceTiers(t *testing.T) {
	if got := HighestDeviceTier([]string{"/dev/net/tun"}); got != TierCommon {
		t.Errorf("tun reported tier %d, want common", got)
	}
	// A whole USB bus is every device on it, which is not the same as one device.
	if got := HighestDeviceTier([]string{"/dev/bus/usb/001/002"}); got != TierElevated {
		t.Errorf("usb bus reported tier %d, want elevated", got)
	}
}

func TestLimitsAreEnforced(t *testing.T) {
	all := []string{}
	for _, c := range Capabilities() {
		all = append(all, c.Name)
	}
	if len(all) <= MaxCapabilities {
		t.Skip("allow-list is shorter than the cap; nothing to prove")
	}
	if _, err := NormalizeCapabilities(all); !errors.Is(err, ErrTooManyCapabilities) {
		t.Errorf("asking for every capability was allowed (err = %v)", err)
	}
}

func TestEmptyIsAlwaysValid(t *testing.T) {
	caps, err := NormalizeCapabilities(nil)
	if err != nil || len(caps) != 0 {
		t.Errorf("nil capabilities = %v, %v; want empty and no error", caps, err)
	}
	devs, err := NormalizeDevices([]string{"", "  "})
	if err != nil || len(devs) != 0 {
		t.Errorf("blank devices = %v, %v; want empty and no error", devs, err)
	}
}
