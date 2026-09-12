// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Capability and device errors. Surfaced to the user, so they read as guidance.
var (
	ErrGrantsDisabled       = errors.New("attaching capabilities and devices is disabled on this platform (MIABI_CONTAINER_GRANTS_ENABLED)")
	ErrCapabilityUnknown    = errors.New("that Linux capability is not one Miabi can grant")
	ErrCapabilityElevated   = errors.New("that capability is close to full host access and is limited to the platform's system workspace")
	ErrCapabilityRestricted = errors.New("this workspace runs the restricted security profile, which does not allow extra capabilities")
	ErrDeviceUnknown        = errors.New("that host device is not one Miabi can expose")
	ErrDeviceBlockDevice    = errors.New("raw block devices expose the host's filesystem and are never granted")
	ErrTooManyCapabilities  = errors.New("too many capabilities requested")
	ErrTooManyDevices       = errors.New("too many devices requested")
	ErrDevicesOnService     = errors.New("host devices cannot be attached to a replicated service; run the app as a container instead")
	ErrCapabilityNotLinux   = errors.New("that is not a Linux capability")
	ErrCapabilityConflict   = errors.New("a capability cannot be both added and dropped")
)

// CapabilityAll names every capability in a drop.
const CapabilityAll = "ALL"

// linuxCapabilities is every capability the kernel defines. Any may be dropped; only the allow-list may be added.
var linuxCapabilities = map[string]bool{
	"AUDIT_CONTROL": true, "AUDIT_READ": true, "AUDIT_WRITE": true, "BLOCK_SUSPEND": true, "BPF": true,
	"CHECKPOINT_RESTORE": true, "CHOWN": true, "DAC_OVERRIDE": true, "DAC_READ_SEARCH": true, "FOWNER": true,
	"FSETID": true, "IPC_LOCK": true, "IPC_OWNER": true, "KILL": true, "LEASE": true, "LINUX_IMMUTABLE": true,
	"MAC_ADMIN": true, "MAC_OVERRIDE": true, "MKNOD": true, "NET_ADMIN": true, "NET_BIND_SERVICE": true,
	"NET_BROADCAST": true, "NET_RAW": true, "PERFMON": true, "SETFCAP": true, "SETGID": true, "SETPCAP": true,
	"SETUID": true, "SYSLOG": true, "SYS_ADMIN": true, "SYS_BOOT": true, "SYS_CHROOT": true, "SYS_MODULE": true,
	"SYS_NICE": true, "SYS_PACCT": true, "SYS_PTRACE": true, "SYS_RAWIO": true, "SYS_RESOURCE": true,
	"SYS_TIME": true, "SYS_TTY_CONFIG": true, "WAKE_ALARM": true,
}

// NormalizeDropCapabilities cleans, de-duplicates and sorts the capabilities to drop. ALL already covers
// every other name, so it is kept alone.
func NormalizeDropCapabilities(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		name := NormalizeCapability(raw)
		switch {
		case name == "" || seen[name]:
			continue
		case name != CapabilityAll && !linuxCapabilities[name]:
			return nil, fmt.Errorf("%w: %s", ErrCapabilityNotLinux, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	if seen[CapabilityAll] {
		return []string{CapabilityAll}, nil
	}
	sort.Strings(out)
	return out, nil
}

// CheckCapabilityConflict refuses a capability both added and dropped by name. Dropping ALL while adding
// some is the intended way to keep only those, so it is no conflict. Both sets must be normalized.
func CheckCapabilityConflict(add, drop []string) error {
	for _, d := range drop {
		for _, a := range add {
			if a == d {
				return fmt.Errorf("%w: %s", ErrCapabilityConflict, a)
			}
		}
	}
	return nil
}

// CapabilityTier decides which workspaces may grant a capability or device.
type CapabilityTier int

const (
	// TierCommon widens what a container may do to itself, not to the host.
	TierCommon CapabilityTier = iota
	// TierElevated is close enough to full host access to need the system workspace.
	TierElevated
)

// Capability describes one grantable Linux capability. Help is shown by the control.
type Capability struct {
	Name string         `json:"name"`
	Tier CapabilityTier `json:"tier"`
	Help string         `json:"help"`
}

// capabilities is the allow-list; anything absent is refused. The elevated tier
// is what permits mounting, and mounting the host's disk is a container escape.
var capabilities = []Capability{
	{"NET_ADMIN", TierCommon, "Configure interfaces, routes and firewall rules. Needed by VPN containers such as WireGuard or Tailscale."},
	{"NET_RAW", TierCommon, "Use raw and packet sockets — ping, traceroute and packet capture."},
	{"NET_BIND_SERVICE", TierCommon, "Bind to ports below 1024 without running as root."},
	{"SYS_PTRACE", TierCommon, "Trace other processes in the container. Needed by debuggers and profilers."},
	{"SYS_NICE", TierCommon, "Change process scheduling priority."},
	{"SYS_TIME", TierCommon, "Set the system clock. Only useful for a container running its own time sync."},
	{"IPC_LOCK", TierCommon, "Lock memory so it is never swapped. Used by databases and key stores."},
	{"CHOWN", TierCommon, "Change file ownership inside the container."},
	{"DAC_OVERRIDE", TierCommon, "Bypass file permission checks inside the container."},
	{"FOWNER", TierCommon, "Bypass ownership checks on files inside the container."},
	{"SETUID", TierCommon, "Change the process user id — how a container drops privileges after start."},
	{"SETGID", TierCommon, "Change the process group id."},
	{"KILL", TierCommon, "Signal processes it does not own inside the container."},
	{"AUDIT_WRITE", TierCommon, "Write to the kernel audit log."},

	{"SYS_ADMIN", TierElevated, "Mount filesystems and much else. Close to full host access — a container that can mount can reach the host's disk."},
	{"SYS_MODULE", TierElevated, "Load and unload kernel modules. This is the host's kernel, not the container's."},
	{"SYS_RAWIO", TierElevated, "Direct I/O to hardware, including raw disk access."},
	{"DAC_READ_SEARCH", TierElevated, "Bypass file read and directory permission checks. Combined with a host mount it reads anything."},
	{"MKNOD", TierElevated, "Create device nodes, which is a route to reaching devices that were never granted."},
}

// MaxCapabilities and MaxDevices bound one application's request, so a manifest
// cannot ask for the whole list.
const MaxCapabilities = 12
const MaxDevices = 8

// Capabilities returns the allow-list.
func Capabilities() []Capability {
	out := make([]Capability, len(capabilities))
	copy(out, capabilities)
	return out
}

// LookupCapability finds a capability in the allow-list by its normalized name.
func LookupCapability(name string) (Capability, bool) {
	for _, c := range capabilities {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// NormalizeCapability upper-cases a name and strips the CAP_ prefix people type.
func NormalizeCapability(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	return strings.TrimPrefix(v, "CAP_")
}

// NormalizeCapabilities cleans, de-duplicates and sorts a set. Sorted so a no-op
// save does not read as a change.
func NormalizeCapabilities(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		name := NormalizeCapability(raw)
		if name == "" {
			continue
		}
		if _, ok := LookupCapability(name); !ok {
			return nil, fmt.Errorf("%w: %s", ErrCapabilityUnknown, name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) > MaxCapabilities {
		return nil, ErrTooManyCapabilities
	}
	sort.Strings(out)
	return out, nil
}

// HighestCapabilityTier reports the most privileged tier in a set, to gate once.
func HighestCapabilityTier(names []string) CapabilityTier {
	tier := TierCommon
	for _, n := range names {
		if c, ok := LookupCapability(n); ok && c.Tier > tier {
			tier = c.Tier
		}
	}
	return tier
}

// devicePrefixes are the exposable host devices. Block devices are absent on
// purpose: /dev/sda is the host's filesystem. See blockedDevicePrefixes.
var devicePrefixes = []struct {
	prefix string
	exact  bool
	tier   CapabilityTier
	help   string
}{
	{prefix: "/dev/net/tun", exact: true, tier: TierCommon, help: "TUN/TAP interface, for VPN containers."},
	{prefix: "/dev/fuse", exact: true, tier: TierCommon, help: "FUSE, for userspace filesystems such as rclone or s3fs."},
	{prefix: "/dev/ttyUSB", tier: TierCommon, help: "USB serial adapter."},
	{prefix: "/dev/ttyACM", tier: TierCommon, help: "USB CDC device — Zigbee and Z-Wave sticks."},
	{prefix: "/dev/serial/", tier: TierCommon, help: "Stable serial device path."},
	{prefix: "/dev/i2c-", tier: TierCommon, help: "I²C bus, for attached sensors."},
	{prefix: "/dev/gpiochip", tier: TierCommon, help: "GPIO controller."},
	{prefix: "/dev/bus/usb/", tier: TierElevated, help: "Raw USB bus access — any device on the bus, not one."},
}

// blockedDevicePrefixes never pass, at any tier. Separate from "not allow-listed"
// so the refusal can say why.
var blockedDevicePrefixes = []string{
	"/dev/sd", "/dev/nvme", "/dev/hd", "/dev/vd", "/dev/xvd", "/dev/dm-", "/dev/loop",
	"/dev/mapper/", "/dev/md", "/dev/mem", "/dev/kmem", "/dev/port", "/dev/kmsg",
}

// NormalizeDevices cleans and de-duplicates a device list against the allow-list.
func NormalizeDevices(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.TrimSpace(raw)
		if d == "" {
			continue
		}
		// A path and nothing else: no ".." out of /dev, no "host:container:rwm"
		// triplet smuggling a second target past the check.
		if !strings.HasPrefix(d, "/dev/") || strings.Contains(d, "..") || strings.Contains(d, ":") {
			return nil, fmt.Errorf("%w: %s", ErrDeviceUnknown, d)
		}
		for _, blocked := range blockedDevicePrefixes {
			if strings.HasPrefix(d, blocked) {
				return nil, fmt.Errorf("%w: %s", ErrDeviceBlockDevice, d)
			}
		}
		if _, ok := lookupDevice(d); !ok {
			return nil, fmt.Errorf("%w: %s", ErrDeviceUnknown, d)
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) > MaxDevices {
		return nil, ErrTooManyDevices
	}
	sort.Strings(out)
	return out, nil
}

func lookupDevice(path string) (CapabilityTier, bool) {
	for _, d := range devicePrefixes {
		if d.exact && path == d.prefix {
			return d.tier, true
		}
		if !d.exact && strings.HasPrefix(path, d.prefix) && len(path) > len(d.prefix) {
			return d.tier, true
		}
	}
	return TierCommon, false
}

// HighestDeviceTier reports the most privileged tier in a normalized device set.
func HighestDeviceTier(paths []string) CapabilityTier {
	tier := TierCommon
	for _, p := range paths {
		if t, ok := lookupDevice(p); ok && t > tier {
			tier = t
		}
	}
	return tier
}

// DeviceCatalog describes an exposable device, for the console's picker.
type DeviceCatalog struct {
	Path  string         `json:"path"`
	Exact bool           `json:"exact"`
	Tier  CapabilityTier `json:"tier"`
	Help  string         `json:"help"`
}

// Devices returns the device allow-list for the API.
func Devices() []DeviceCatalog {
	out := make([]DeviceCatalog, 0, len(devicePrefixes))
	for _, d := range devicePrefixes {
		out = append(out, DeviceCatalog{Path: d.prefix, Exact: d.exact, Tier: d.tier, Help: d.help})
	}
	return out
}
