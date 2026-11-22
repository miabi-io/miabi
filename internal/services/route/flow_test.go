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
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
)

func uintp(v uint) *uint { return &v }

func flowFixture() flowInput {
	app := &models.Application{ID: 7, WorkspaceID: 1, Name: "shop-api", Port: 8080, Status: models.AppStatusRunning, CurrentReleaseID: uintp(1)}
	return flowInput{
		rt: &models.Route{ID: 1, WorkspaceID: 1, Name: "api", ApplicationID: 7, Path: "/api", Hosts: []string{"api.example.com"},
			TLSMode: models.RouteTLSACME, Enabled: true},
		app:      app,
		domains:  []models.Domain{{Name: "example.com", Verified: true}},
		gated:    true,
		tls:      FlowTLS{Mode: "acme"},
		gateway:  FlowGateway{Kind: "platform"},
		appNames: map[uint]string{7: "shop-api", 8: "admin"},
		releases: map[uint]*models.Release{1: {ID: 1, Version: 1, Image: "registry/shop-api:2.3.1"}, 2: {ID: 2, Version: 2, Image: "registry/shop-api:2.4.0"}},
		health: func(rel *models.Release) *models.ReleaseHealth {
			if rel == nil {
				return &models.ReleaseHealth{Status: "no_container"}
			}
			return &models.ReleaseHealth{Status: "running", Running: 1, Desired: 1}
		},
	}
}

func TestFlowCanaryChainAndSiblings(t *testing.T) {
	in := flowFixture()
	in.app.CanaryReleaseID = uintp(2)
	in.app.CanaryWeight = 10
	in.app.CanaryExclusive = true
	in.app.CanaryMatch = []models.CanaryMatchRule{{Source: "header", Name: "X-Canary", Operator: "equals", Value: "1"}}
	in.rt.Middlewares = []string{"force-https", "api-auth", "gone"}
	in.middlewares = []models.Middleware{
		{Name: "force-https", Type: "redirectScheme", Rule: map[string]any{"scheme": "https", "permanent": true}},
		{Name: "api-auth", Type: "jwtAuth", Paths: []string{"/api/.*"}},
	}
	in.routes = []models.Route{
		*in.rt,
		{ID: 2, Name: "admin", ApplicationID: 8, Path: "/api/admin", Hosts: []string{"api.example.com"}},
		{ID: 3, Name: "web", ApplicationID: 7, Path: "/", Hosts: []string{"API.example.com", "www.example.com"}},
		{ID: 4, Name: "docs", ApplicationID: 7, Path: "/docs", Hosts: []string{"api.example.com"}},
		{ID: 5, Name: "elsewhere", ApplicationID: 7, Path: "/api", Hosts: []string{"other.example.com"}},
	}

	f := buildFlow(in)
	if !f.Route.Serving || f.Route.ServingReason != "" {
		t.Errorf("serving = %v (%q)", f.Route.Serving, f.Route.ServingReason)
	}
	if len(f.Hosts) != 1 || !f.Hosts[0].Verified || f.Hosts[0].Domain != "example.com" {
		t.Errorf("hosts = %+v", f.Hosts)
	}

	if len(f.Chain) != 3 {
		t.Fatalf("chain = %+v", f.Chain)
	}
	if f.Chain[0].Name != "force-https" || !reflect.DeepEqual(f.Chain[0].EarlyStatus, []int{301}) {
		t.Errorf("chain[0] = %+v", f.Chain[0])
	}
	if !reflect.DeepEqual(f.Chain[1].EarlyStatus, []int{401}) || !reflect.DeepEqual(f.Chain[1].Paths, []string{"/api/.*"}) {
		t.Errorf("chain[1] = %+v", f.Chain[1])
	}
	if !f.Chain[2].Missing {
		t.Errorf("chain[2] = %+v", f.Chain[2])
	}

	rel := map[string]string{}
	for _, s := range f.Siblings {
		rel[s.Name] = s.Relation
	}
	want := map[string]string{"admin": "narrower", "web": "broader", "docs": "separate"}
	if !reflect.DeepEqual(rel, want) {
		t.Errorf("siblings = %v, want %v", rel, want)
	}
	if f.Siblings[0].AppName != "admin" {
		t.Errorf("sibling app = %q", f.Siblings[0].AppName)
	}

	if len(f.Backends) != 2 {
		t.Fatalf("backends = %+v", f.Backends)
	}
	st, cn := f.Backends[0], f.Backends[1]
	if st.Role != "stable" || st.Weight != 90 || st.Release == nil || st.Release.Version != 1 || st.Health == nil || st.Health.Running != 1 {
		t.Errorf("stable = %+v", st)
	}
	if cn.Role != "canary" || cn.Weight != 10 || !cn.Exclusive || len(cn.Match) != 1 || cn.Release == nil || cn.Release.Version != 2 {
		t.Errorf("canary = %+v", cn)
	}
	if st.Endpoint == cn.Endpoint {
		t.Errorf("stable and canary share %s", st.Endpoint)
	}
}

func TestFlowMatchesRenderedBackends(t *testing.T) {
	in := flowFixture()
	in.app.CanaryReleaseID = uintp(2)
	in.app.CanaryWeight = 25
	f := buildFlow(in)
	rendered := gatewayBackends(in.app, routePort(in.rt, in.app))
	if len(f.Backends) != len(rendered) {
		t.Fatalf("flow has %d backends, gateway gets %d", len(f.Backends), len(rendered))
	}
	for i := range rendered {
		if f.Backends[i].Endpoint != rendered[i].Endpoint || f.Backends[i].Weight != rendered[i].Weight {
			t.Errorf("backend %d: flow %s/%d, gateway %s/%d", i, f.Backends[i].Endpoint, f.Backends[i].Weight, rendered[i].Endpoint, rendered[i].Weight)
		}
	}
}

func TestFlowNotServed(t *testing.T) {
	in := flowFixture()
	in.domains = []models.Domain{{Name: "example.com", Verified: false}}
	in.rt.Maintenance = models.RouteMaintenance{Enabled: true, HTML: "<h1>x</h1>"}
	f := buildFlow(in)
	if f.Route.Serving || f.Route.ServingReason != "domain not verified: api.example.com" {
		t.Errorf("serving = %v (%q)", f.Route.Serving, f.Route.ServingReason)
	}
	if m := f.Route.Maintenance; !m.Enabled || m.StatusCode != 503 || !m.HasHTML || m.HasMessage {
		t.Errorf("maintenance = %+v", m)
	}
	if len(f.Backends) != 1 || f.Backends[0].Weight != 100 {
		t.Errorf("backends = %+v", f.Backends)
	}

	in = flowFixture()
	in.app.CurrentReleaseID = nil
	f = buildFlow(in)
	if f.App.Deployed || f.Route.Serving || f.Route.ServingReason != "app not deployed" || len(f.Backends) != 0 {
		t.Errorf("undeployed app: %+v, backends = %+v", f.Route, f.Backends)
	}
}

func TestFlowGeneratedHostIsExternal(t *testing.T) {
	in := flowFixture()
	in.rt.Generated = true
	in.rt.Hosts = []string{"shop-api-x1.apps.example.com"}
	f := buildFlow(in)
	if h := f.Hosts[0]; !h.External || h.Registered || h.Domain != "" {
		t.Errorf("host = %+v", h)
	}
	if !f.Route.Serving {
		t.Errorf("generated route not serving: %q", f.Route.ServingReason)
	}
}

func TestFlowCustomCertificate(t *testing.T) {
	in := flowFixture()
	in.rt.Hosts = []string{"api.example.com", "a.b.example.com"}
	in.tls = FlowTLS{Mode: "custom", CertificateID: uintp(9)}
	in.cert = &models.Certificate{Name: "wild", DNSNames: []string{"*.example.com"}}
	f := buildFlow(in)
	if f.TLS.CertificateName != "wild" || f.TLS.NotAfter == nil {
		t.Errorf("tls = %+v", f.TLS)
	}
	if c := f.Hosts[0].CertCovers; c == nil || !*c {
		t.Errorf("api.example.com covers = %v", c)
	}
	if c := f.Hosts[1].CertCovers; c == nil || *c {
		t.Errorf("a.b.example.com covers = %v", c)
	}
}

func TestPathRelation(t *testing.T) {
	cases := []struct{ mine, other, want string }{
		{"/api", "/api/admin", "narrower"},
		{"/api", "/", "broader"},
		{"/", "/anything", "narrower"},
		{"/api", "/apidocs", "separate"},
		{"/api", "/docs", "separate"},
		{"/api/", "/api/v2", "narrower"},
	}
	for _, c := range cases {
		if got := pathRelation(c.mine, c.other); got != c.want {
			t.Errorf("pathRelation(%q, %q) = %q, want %q", c.mine, c.other, got, c.want)
		}
	}
}

func TestCertCovers(t *testing.T) {
	cert := &models.Certificate{CommonName: "example.com", DNSNames: []string{"*.example.com"}}
	for host, want := range map[string]bool{"example.com": true, "a.example.com": true, "a.b.example.com": false, "example.org": false} {
		if got := certCovers(cert, host); got != want {
			t.Errorf("certCovers(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestEarlyStatusOf(t *testing.T) {
	cases := []struct {
		typ  string
		rule map[string]any
		want []int
	}{
		{"redirectScheme", map[string]any{"permanent": true}, []int{301}},
		{"redirectScheme", nil, []int{302}},
		{"access", map[string]any{"statusCode": float64(451)}, []int{451}},
		{"access", nil, []int{403}},
		{"rateLimit", nil, []int{429}},
		{"compress", nil, nil},
		{"unknown", nil, nil},
	}
	for _, c := range cases {
		if got := mwcatalog.EarlyStatusOf(c.typ, c.rule); !reflect.DeepEqual(got, c.want) {
			t.Errorf("EarlyStatusOf(%s, %v) = %v, want %v", c.typ, c.rule, got, c.want)
		}
	}
}

func TestFlowCanaryState(t *testing.T) {
	in := flowFixture()
	if buildFlow(in).Canary != nil {
		t.Fatal("no canary should give no canary block")
	}
	in.app.CanaryReleaseID = uintp(2)
	in.app.CanaryWeight = 90
	in.app.CanaryStepWeight = 20
	f := buildFlow(in)
	if c := f.Canary; c == nil || c.Mode != "auto" || c.NextWeight != 100 || !c.PromotesNext || c.IntervalSeconds != 60 {
		t.Errorf("canary = %+v", f.Canary)
	}
}
