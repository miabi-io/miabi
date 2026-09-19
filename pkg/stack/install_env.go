// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"sort"
	"strconv"
)

// installEnv renders the settings only the kinded document models into the control plane's
// environment. Everything here already has a variable and an existing "the environment pins it, the
// console owns the rest" mechanism, so the manifest needs no reconciler of its own — it compiles.
//
// A field the manifest does not state emits NOTHING. An emitted variable pins the console's field
// (registryserver.Locks, platformbackup's EnvLocked, the cluster's external-access pins), so writing
// a zero value would lock the UI to a default nobody asked for.
//
// Emitting a key here also makes it un-settable through `env:` for free: normalizeEnv derives the
// refused set by building this spec with Env nil and reading back what it emits. So a manifest that
// states spec.networking.hostPorts.min refuses a raw MIABI_HOST_PORT_MIN — the two can never
// disagree — while one that stays silent leaves the raw variable available as the escape hatch.
func installEnv(m *Manifest) []string {
	in := m.Install
	var env []string

	str := func(key, v string) {
		if v != "" {
			env = append(env, key+"="+v)
		}
	}
	num := func(key string, v int) {
		if v != 0 {
			env = append(env, key+"="+strconv.Itoa(v))
		}
	}
	flag := func(key string, v *bool) {
		if v != nil {
			env = append(env, key+"="+strconv.FormatBool(*v))
		}
	}

	str("MIABI_ACME_DIRECTORY_URL", in.ACMEDirectoryURL)
	str("MIABI_REGISTRY_STORAGE", in.RegistryStorage)
	str("MIABI_REGISTRY_PLATFORM_TOKEN", in.RegistryPlatformToken)

	str("MIABI_NETWORK_POOL_CIDR", in.Pool.CIDR)
	num("MIABI_NETWORK_SUBNET_PREFIX", in.Pool.SubnetPrefix)
	flag("MIABI_NETWORK_IPV6", m.Network.IPv6)
	str("MIABI_NETWORK_IPV6_ULA_PREFIX", in.Pool.IPv6ULAPrefix)
	num("MIABI_HOST_PORT_MIN", in.HostPorts.Min)
	num("MIABI_HOST_PORT_MAX", in.HostPorts.Max)
	str("MIABI_EXTERNAL_BASE_DOMAIN", in.External.BaseDomain)
	str("MIABI_EXTERNAL_BASE_PROVIDER", in.External.CertProvider)
	num("MIABI_DNS_RECONCILE_MINUTES", in.DNS.ReconcileMinutes)

	str("MIABI_LICENSE_FILE", in.License.File)

	if b := in.Backup; b != nil {
		str("MIABI_PLATFORM_BACKUP_SCHEDULE", b.Schedule)
		num("MIABI_PLATFORM_BACKUP_MAX", b.Retention.Max)
		num("MIABI_PLATFORM_BACKUP_RETENTION_DAYS", b.Retention.Days)

		d := b.Destination
		str("MIABI_PLATFORM_BACKUP_S3_ENDPOINT", d.Endpoint)
		str("MIABI_PLATFORM_BACKUP_S3_BUCKET", d.Bucket)
		str("MIABI_PLATFORM_BACKUP_S3_REGION", d.Region)
		str("MIABI_PLATFORM_BACKUP_S3_ACCESS_KEY", d.AccessKey)
		str("MIABI_PLATFORM_BACKUP_S3_SECRET_KEY", d.SecretKey)
		flag("MIABI_PLATFORM_BACKUP_S3_USE_SSL", d.UseSSL)
		flag("MIABI_PLATFORM_BACKUP_S3_FORCE_PATH_STYLE", d.ForcePathStyle)
		str("MIABI_PLATFORM_BACKUP_PATH", d.Path)
		str("MIABI_PLATFORM_BACKUP_DATABASE_PATH", d.DatabasePath)
		str("MIABI_PLATFORM_BACKUP_VOLUME_PATH", d.VolumePath)

		str("MIABI_PLATFORM_BACKUP_PASSPHRASE", b.Encryption.Passphrase)
		flag("MIABI_PLATFORM_BACKUP_ENCRYPT", b.Encryption.Encrypt)
		flag("MIABI_PLATFORM_BACKUP_INCLUDE_IDENTITY", b.Encryption.IncludeIdentity)
		flag("MIABI_PLATFORM_BACKUP_INCLUDE_TENANT_DATA", b.IncludeTenantData)
	}

	// Sorted for the same reason the rest of the spec is: Go map and slice order would otherwise
	// vary, and an unstable specHash recreates the whole stack on every converge.
	sort.Strings(env)
	return env
}
