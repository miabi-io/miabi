// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"sort"
	"strconv"
)

// An unstated field emits nothing, since emitted vars pin console fields; stated ones refuse the raw variable.
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

	// An unstable order would change specHash and recreate the whole stack on every converge.
	sort.Strings(env)
	return env
}
