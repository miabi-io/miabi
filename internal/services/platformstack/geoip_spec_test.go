// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformstack

import "testing"

// The GeoIP database is bound into the gateway (at Goma's GOMA_GEOIP_DB default
// path) only when it has been provisioned — binding a missing host path would
// make Docker create a directory there.
func TestGatewaySpecBindsGeoIPOnlyWhenProvisioned(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")

	bind := func() (source string, readOnly, found bool) {
		for _, b := range gatewaySpec(m, ContainerGateway, m.Images.Gateway).Binds {
			if b.Target == gomaGeoIPFile {
				return b.Source, b.ReadOnly, true
			}
		}
		return "", false, false
	}

	if _, _, found := bind(); found {
		t.Fatal("GeoIP database bound when none was provisioned")
	}

	const host = "/etc/miabi/GeoLite2-Country.mmdb"
	m.gatewayHostGeoIP = host
	source, readOnly, found := bind()
	if !found {
		t.Fatal("GeoIP database not bound after provisioning")
	}
	if source != host || !readOnly {
		t.Fatalf("GeoIP bind wrong: source=%q readOnly=%v, want %q read-only", source, readOnly, host)
	}
}
