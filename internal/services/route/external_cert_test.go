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
package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
)

type fakeExternalCerts struct {
	usable map[uint]bool
	calls  int
}

func (f *fakeExternalCerts) ExternalCertificatePair(clusterID uint) (string, string, bool) {
	f.calls++
	if f.usable[clusterID] {
		return "WILDCARD", "KEY", true
	}
	return "", "", false
}

func TestGeneratedRoutesUseTheClusterCertificate(t *testing.T) {
	certs := &fakeExternalCerts{usable: map[uint]bool{1: true}}
	s := &Service{externalCerts: certs}
	cache := map[uint]externalCert{}
	app := &models.Application{ClusterID: 1}

	generated := proxy.RenderedRoute{TLSProvider: "letsencrypt"}
	s.withExternalCert(&generated, &models.Route{Generated: true}, app, cache)
	if len(generated.Certs) != 1 || generated.Certs[0].CertPEM != "WILDCARD" || generated.TLSProvider != "" {
		t.Errorf("generated route = %+v, want the wildcard and no per-host provider", generated)
	}

	// A route the tenant created keeps its own TLS; the platform certificate is never handed to it.
	custom := proxy.RenderedRoute{TLSProvider: "letsencrypt"}
	s.withExternalCert(&custom, &models.Route{}, app, cache)
	if len(custom.Certs) != 0 || custom.TLSProvider != "letsencrypt" {
		t.Errorf("tenant route = %+v, want it untouched", custom)
	}

	s.withExternalCert(&proxy.RenderedRoute{}, &models.Route{Generated: true}, app, cache)
	if certs.calls != 1 {
		t.Errorf("looked the certificate up %d times in one render, want 1", certs.calls)
	}
}

func TestGeneratedRoutesFallBackToACertificatePerApp(t *testing.T) {
	s := &Service{externalCerts: &fakeExternalCerts{}}
	rr := proxy.RenderedRoute{TLSProvider: "letsencrypt"}
	s.withExternalCert(&rr, &models.Route{Generated: true}, &models.Application{ClusterID: 2}, map[uint]externalCert{})
	if len(rr.Certs) != 0 || rr.TLSProvider != "letsencrypt" {
		t.Errorf("route = %+v, want the cluster's certManager provider kept", rr)
	}
}
