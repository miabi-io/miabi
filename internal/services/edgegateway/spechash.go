// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// configHashLabel carries the fingerprint of the rendered goma.yml the gateway was started with. The config
// lives in a volume rather than in the run spec, so without this a config edit would look like "no change".
const configHashLabel = "io.miabi.gateway-config-hash"

// hashSpec fingerprints the run spec a gateway container was created from, so a reconnecting agent can tell
// "same gateway, nothing to do" from "the spec changed, recreate it". Same idea as the platform stack's
// converge in pkg/stack, which skips a component whose hash and state still match.
func hashSpec(spec docker.RunSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "image=%s\ncmd=%q\nentrypoint=%q\n", spec.Image, spec.Cmd, spec.Entrypoint)
	// Sorted: Go map and slice order would otherwise vary between runs, and an unstable hash turns every
	// reconnect back into the recreate this exists to avoid.
	writeSorted(&b, "env", spec.Env)
	writeSorted(&b, "networks", spec.Networks)
	writeSortedMap(&b, "mounts", spec.Mounts)
	writeSortedMap(&b, "ports", spec.Ports)
	writeSortedMap(&b, "labels", spec.Labels) // carries the config fingerprint
	if h := spec.Healthcheck; h != nil {
		fmt.Fprintf(&b, "health=%q interval=%s timeout=%s retries=%d start=%s\n",
			h.Test, h.Interval, h.Timeout, h.Retries, h.StartPeriod)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// configFingerprint identifies a rendered config without storing it in a label.
func configFingerprint(cfg string) string {
	sum := sha256.Sum256([]byte(cfg))
	return hex.EncodeToString(sum[:])[:16]
}

func writeSorted(b *strings.Builder, key string, values []string) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	for _, v := range sorted {
		fmt.Fprintf(b, "%s=%s\n", key, v)
	}
}

func writeSortedMap(b *strings.Builder, key string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s=%s:%s\n", key, k, m[k])
	}
}
