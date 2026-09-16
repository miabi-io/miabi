// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"strings"
	"testing"
)

func controlPlaneEnv(m *Manifest) []string {
	return controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi).Env
}

func hasKey(env []string, key string) bool {
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			return true
		}
	}
	return false
}

// An emitted variable PINS the console's field, so a setting the manifest never mentions must emit
// nothing at all — otherwise a fresh install silently locks the UI to a default nobody chose.
func TestAnUnstatedSettingEmitsNoVariable(t *testing.T) {
	m := testManifest()
	if got := installEnv(m); len(got) != 0 {
		t.Errorf("installEnv = %v, want nothing for a manifest that states none of it", got)
	}
	for _, key := range []string{
		"MIABI_HOST_PORT_MIN", "MIABI_NETWORK_POOL_CIDR", "MIABI_EXTERNAL_BASE_DOMAIN",
		"MIABI_PLATFORM_BACKUP_SCHEDULE", "MIABI_PLATFORM_BACKUP_ENCRYPT", "MIABI_LICENSE_FILE",
	} {
		if hasKey(controlPlaneEnv(m), key) {
			t.Errorf("%s reached the control plane from a manifest that never set it", key)
		}
	}
}

func TestStatedSettingsReachTheControlPlane(t *testing.T) {
	no := false
	m := testManifest()
	m.Install.Pool = PoolSpec{CIDR: "10.80.0.0/12", SubnetPrefix: 26}
	m.Install.HostPorts = HostPortsSpec{Min: 2000, Max: 3000}
	m.Install.External = ExternalSpec{BaseDomain: "apps.example.com"}
	m.Install.DNS = DNSSpec{ReconcileMinutes: 45}
	m.Install.License = LicenseSpec{File: "/etc/miabi/license.jwt"}
	m.Install.Backup = &BackupSpec{
		Schedule:    "0 3 * * *",
		Destination: BackupDestination{Bucket: "b", AccessKey: "ak", SecretKey: "sk"},
		Encryption:  BackupEncryption{Encrypt: &no},
	}

	env := controlPlaneEnv(m)
	for kv, want := range map[string]string{
		"MIABI_NETWORK_POOL_CIDR=10.80.0.0/12":        "the managed subnet pool",
		"MIABI_NETWORK_SUBNET_PREFIX=26":              "the subnet prefix",
		"MIABI_HOST_PORT_MIN=2000":                    "the host port floor",
		"MIABI_EXTERNAL_BASE_DOMAIN=apps.example.com": "one-click app URLs",
		"MIABI_DNS_RECONCILE_MINUTES=45":              "the managed-DNS sweep",
		"MIABI_LICENSE_FILE=/etc/miabi/license.jwt":   "the license",
		"MIABI_PLATFORM_BACKUP_SCHEDULE=0 3 * * *":    "the backup schedule",
		"MIABI_PLATFORM_BACKUP_S3_BUCKET=b":           "the backup bucket",
	} {
		found := false
		for _, e := range env {
			if e == kv {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q did not reach the control plane, so %s is configured and inert", kv, want)
		}
	}

	// A false that the manifest states explicitly must be emitted; only an ABSENT field stays silent.
	if !hasKey(env, "MIABI_PLATFORM_BACKUP_ENCRYPT") {
		t.Error("an explicit encrypt: false emitted nothing — the console would keep deciding it")
	}
}

// normalizeEnv derives the refused set from what the spec emits, so this holds with no list to keep
// in step: state a field in the spec and the raw variable is refused.
func TestASpecFieldIsRefusedInRawEnv(t *testing.T) {
	m := testManifest()
	m.Install.HostPorts = HostPortsSpec{Min: 2000, Max: 3000}
	m.Env["MIABI_HOST_PORT_MIN"] = "4000"

	err := m.Normalize()
	if err == nil {
		t.Fatal("a raw env var was allowed to contradict the spec field that owns it")
	}
	if !strings.Contains(err.Error(), "MIABI_HOST_PORT_MIN") {
		t.Errorf("error = %q, want it to name the variable", err)
	}
}

// The escape hatch stays open: ~140 variables have no spec field, and a manifest silent about one
// must still be able to set it through env.
func TestRawEnvStaysAvailableWhileTheSpecIsSilent(t *testing.T) {
	m := testManifest()
	m.Env["MIABI_HOST_PORT_MIN"] = "4000"

	if err := m.Normalize(); err != nil {
		t.Fatalf("refused a variable the spec does not claim: %v", err)
	}
}

// The control plane overrides the stored destination only when all three are present, so a partial
// block pins nothing and says nothing.
func TestBackupDestinationIsAllOrNothing(t *testing.T) {
	m := testManifest()
	m.Install.Backup = &BackupSpec{Destination: BackupDestination{Bucket: "b"}}

	if err := m.Normalize(); err == nil {
		t.Error("a half-configured backup destination was accepted — it would silently do nothing")
	}

	m.Install.Backup.Destination.AccessKey = "ak"
	m.Install.Backup.Destination.SecretKey = "sk"
	if err := m.Normalize(); err != nil {
		t.Errorf("a complete destination was refused: %v", err)
	}
}

func TestInstallSettingsAreValidated(t *testing.T) {
	cases := map[string]func(*Manifest){
		"an unknown registry driver": func(m *Manifest) { m.Install.RegistryStorage = "nfs" },
		"a subnet prefix out of range": func(m *Manifest) {
			m.Install.Pool = PoolSpec{CIDR: "10.64.0.0/12", SubnetPrefix: 42}
		},
		"a host port range that is inverted": func(m *Manifest) {
			m.Install.HostPorts = HostPortsSpec{Min: 9000, Max: 80}
		},
		"a host port above the maximum": func(m *Manifest) {
			m.Install.HostPorts = HostPortsSpec{Min: 1024, Max: 70000}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			mutate(m)
			if err := m.Normalize(); err == nil {
				t.Error("accepted a value the control plane would reject at boot, or silently ignore")
			}
		})
	}
}

// Changing one of these must recreate the control plane, or the manifest and the running stack
// disagree until something else happens to trigger a recreate.
func TestChangingABackupFieldRecreatesTheControlPlane(t *testing.T) {
	m := testManifest()
	before := specHash(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi))

	m.Install.Backup = &BackupSpec{Schedule: "0 3 * * *"}
	if after := specHash(controlPlaneSpec(m, ContainerControlPlane, m.Images.Miabi)); after == before {
		t.Error("the spec hash did not change, so the new setting would never reach a running stack")
	}
}

// Fresh installs only. On an existing host an imported gateway would never receive the key, and its
// routes would fail with no obvious cause.
func TestTheGatewayKeyIsMintedOnlyWhenAsked(t *testing.T) {
	m := testManifest()
	if m.Gateway.Env[gomaConfigEncryptionKey] != "" {
		t.Fatal("Normalize minted a gateway config key — that would enable encryption on every upgrade")
	}

	if err := m.GenerateGatewayConfigKey(); err != nil {
		t.Fatal(err)
	}
	key := m.Gateway.Env[gomaConfigEncryptionKey]
	if key == "" {
		t.Fatal("no key was generated for a fresh install")
	}

	if err := m.GenerateGatewayConfigKey(); err != nil {
		t.Fatal(err)
	}
	if m.Gateway.Env[gomaConfigEncryptionKey] != key {
		t.Error("a second call rotated the key — Miabi would encrypt what the gateway can no longer read")
	}
}

// Miabi encrypts what Goma decrypts, so one value has to land in both containers.
func TestTheGatewayKeyReachesBothContainers(t *testing.T) {
	m := testManifest()
	if err := m.GenerateGatewayConfigKey(); err != nil {
		t.Fatal(err)
	}
	want := gomaConfigEncryptionKey + "=" + m.Gateway.Env[gomaConfigEncryptionKey]

	for name, env := range map[string][]string{
		"control plane": controlPlaneEnv(m),
		"gateway":       gatewaySpec(m, ContainerGateway, m.Images.Gateway).Env,
	} {
		found := false
		for _, e := range env {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the %s did not receive the config encryption key — set on one side only, routing breaks silently", name)
		}
	}
}
