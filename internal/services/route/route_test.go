// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestBackendsForNoCanary(t *testing.T) {
	app := &models.Application{ID: 5}
	b := aliasBackends(app, 8080, "http")
	if len(b) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(b))
	}
	if b[0].Endpoint != "http://mb-app-5:8080" || b[0].Weight != 0 {
		t.Errorf("unexpected stable backend: %+v", b[0])
	}
}

func TestBackendsForHTTPSScheme(t *testing.T) {
	app := &models.Application{ID: 5}
	b := aliasBackends(app, 8443, "https")
	if b[0].Endpoint != "https://mb-app-5:8443" {
		t.Errorf("https scheme not applied: %+v", b[0])
	}
}

func TestBackendsForCanarySplit(t *testing.T) {
	relID := uint(9)
	app := &models.Application{ID: 5, CanaryReleaseID: &relID, CanaryWeight: 20}
	b := aliasBackends(app, 80, "http")
	if len(b) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(b))
	}
	if b[0].Endpoint != "http://mb-app-5:80" || b[0].Weight != 80 {
		t.Errorf("unexpected stable backend: %+v", b[0])
	}
	if b[1].Endpoint != "http://mb-app-5-canary:80" || b[1].Weight != 20 {
		t.Errorf("unexpected canary backend: %+v", b[1])
	}
}

func TestBackendsForCanaryZeroWeightIgnored(t *testing.T) {
	relID := uint(9)
	app := &models.Application{ID: 5, CanaryReleaseID: &relID, CanaryWeight: 0}
	if b := aliasBackends(app, 80, "http"); len(b) != 1 {
		t.Fatalf("expected single backend when weight is 0, got %d", len(b))
	}
}

func TestPortScheme(t *testing.T) {
	app := &models.Application{Ports: []models.AppPort{
		{ContainerPort: 8080, Scheme: "http"},
		{ContainerPort: 8443, Scheme: "https"},
	}}
	if s := portScheme(app, 8443); s != "https" {
		t.Errorf("portScheme(8443) = %q, want https", s)
	}
	if s := portScheme(app, 8080); s != "http" {
		t.Errorf("portScheme(8080) = %q, want http", s)
	}
	if s := portScheme(app, 9999); s != "http" { // undeclared → default http
		t.Errorf("portScheme(undeclared) = %q, want http", s)
	}
}

func TestIsRemotePortForward(t *testing.T) {
	cases := []struct {
		name string
		srv  *models.Server
		want bool
	}{
		{"nil", nil, false},
		{"local manager", &models.Server{IsLocal: true, Connectivity: models.ConnectivityPortForward}, false},
		{"remote port-forward", &models.Server{Connectivity: models.ConnectivityPortForward}, true},
		{"remote edge-gateway", &models.Server{Connectivity: models.ConnectivityEdgeGateway}, false},
	}
	for _, c := range cases {
		if got := isRemotePortForward(c.srv); got != c.want {
			t.Errorf("%s: isRemotePortForward = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPrivateBindIP(t *testing.T) {
	cases := []struct {
		addr, want string
	}{
		{"10.0.0.7", "10.0.0.7"},       // private IPv4 → bind to it
		{"203.0.113.5", "203.0.113.5"}, // any IPv4 is returned (privacy via firewall)
		{"node.example.com", ""},       // hostname → all interfaces
		{"", ""},                       // unset → all interfaces
		{"fd00::1", ""},                // IPv6 → all interfaces (avoid host:port ambiguity)
	}
	for _, c := range cases {
		if got := privateBindIP(&models.Server{Address: c.addr}); got != c.want {
			t.Errorf("privateBindIP(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestSanitizeBase(t *testing.T) {
	cases := map[string]string{
		"apps.example.com":    "apps.example.com",
		"*.apps.example.com":  "apps.example.com",
		".apps.example.com":   "apps.example.com",
		"  Apps.Example.COM ": "apps.example.com",
		"":                    "",
	}
	for in, want := range cases {
		if got := sanitizeBase(in); got != want {
			t.Errorf("sanitizeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAliasToken(t *testing.T) {
	if got := aliasToken("mb-app-eqi3tlf2-11"); got != "eqi3tlf2" {
		t.Errorf("aliasToken = %q, want eqi3tlf2", got)
	}
	if got := aliasToken(""); got != "" {
		t.Errorf("aliasToken(\"\") = %q, want \"\"", got)
	}
}

func TestPrimaryPort(t *testing.T) {
	// app's declared port wins when selected.
	if p := primaryPort(8080, map[int]bool{8080: true, 9000: true}); p != 8080 {
		t.Errorf("primaryPort = %d, want 8080", p)
	}
	// else the lowest selected port.
	if p := primaryPort(80, map[int]bool{9000: true, 8080: true}); p != 8080 {
		t.Errorf("primaryPort = %d, want 8080", p)
	}
}

func TestPartitionGeneratedOrphans(t *testing.T) {
	app := &models.Application{
		Port:  8080,
		Ports: []models.AppPort{{ContainerPort: 8080}, {ContainerPort: 9090}},
	}
	routes := []models.Route{
		{ID: 1, TargetPort: 8080, Generated: true},  // valid generated (primary)
		{ID: 2, TargetPort: 9090, Generated: true},  // valid generated (declared)
		{ID: 3, TargetPort: 3000, Generated: true},  // orphan: port no longer exposed
		{ID: 4, TargetPort: 3000, Generated: false}, // user route on same dead port: kept
	}
	keep, orphans := partitionGeneratedOrphans(app, routes)

	if len(orphans) != 1 || orphans[0].ID != 3 {
		t.Fatalf("orphans = %v, want only route 3", ids(orphans))
	}
	gotKeep := ids(keep)
	if len(gotKeep) != 3 || gotKeep[0] != 1 || gotKeep[1] != 2 || gotKeep[2] != 4 {
		t.Fatalf("kept = %v, want [1 2 4]", gotKeep)
	}
}

func TestPartitionGeneratedOrphansNoPorts(t *testing.T) {
	// An app with no exposed ports: every generated route is an orphan; user
	// routes still survive.
	app := &models.Application{}
	routes := []models.Route{
		{ID: 1, TargetPort: 8080, Generated: true},
		{ID: 2, TargetPort: 8080, Generated: false},
	}
	keep, orphans := partitionGeneratedOrphans(app, routes)
	if len(orphans) != 1 || orphans[0].ID != 1 {
		t.Fatalf("orphans = %v, want only route 1", ids(orphans))
	}
	if got := ids(keep); len(got) != 1 || got[0] != 2 {
		t.Fatalf("kept = %v, want [2]", got)
	}
}

func ids(rs []models.Route) []uint {
	out := make([]uint, len(rs))
	for i := range rs {
		out[i] = rs[i].ID
	}
	return out
}

// fakeCluster stubs swarm detection for the upstream-selection tests.
type fakeCluster struct{ on bool }

func (f fakeCluster) CapCluster() bool { return f.on }

// A port-forward node is reached by a published host port — until cluster mode is
// on, at which point the app is on the shared ingress overlay and the gateway
// dials its DNS alias instead, on any node, with nothing published.
func TestUseAliasUpstream(t *testing.T) {
	local := &models.Server{IsLocal: true}
	edge := &models.Server{Connectivity: models.ConnectivityEdgeGateway}
	portFwd := &models.Server{Connectivity: models.ConnectivityPortForward}

	tests := []struct {
		name      string
		srv       *models.Server
		clusterOn bool
		want      bool
	}{
		{name: "local node always uses its alias", srv: local, want: true},
		{name: "edge-gateway node shares a network with its own gateway", srv: edge, want: true},
		{name: "port-forward node without cluster publishes a host port", srv: portFwd, want: false},
		{name: "port-forward node with cluster reaches the alias over the ingress overlay", srv: portFwd, clusterOn: true, want: true},
		{name: "unknown node falls back to the alias", srv: nil, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			if tt.clusterOn {
				s.SetCluster(fakeCluster{on: true})
			}
			if got := s.useAliasUpstream(tt.srv); got != tt.want {
				t.Errorf("useAliasUpstream = %v, want %v", got, tt.want)
			}
		})
	}
}

// Regression: on a port-forward node the host-port upstream can only name one
// container, so a canary there received 0% of traffic while the UI reported its
// weight. In cluster mode the alias upstream must carry the split.
func TestCanaryWeightSurvivesOnAPortForwardNodeInClusterMode(t *testing.T) {
	rel := uint(9)
	app := &models.Application{Alias: "mb-app-tok-7", CanaryReleaseID: &rel, CanaryWeight: 20}

	s := &Service{}
	s.SetCluster(fakeCluster{on: true})
	if !s.useAliasUpstream(&models.Server{Connectivity: models.ConnectivityPortForward}) {
		t.Fatal("cluster mode must route a port-forward node over its alias")
	}
	b := aliasBackends(app, 80, "http")
	if len(b) != 2 {
		t.Fatalf("want a weighted stable+canary split, got %d backend(s): %+v", len(b), b)
	}
	if b[0].Weight != 80 || b[1].Weight != 20 {
		t.Errorf("canary weights lost: got stable=%d canary=%d, want 80/20", b[0].Weight, b[1].Weight)
	}
}
