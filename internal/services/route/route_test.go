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

type fakeCluster struct {
	on      bool
	gateway *models.Cluster
}

func (f fakeCluster) IsSwarm(uint) bool { return f.on }

func (f fakeCluster) OwnGateway(id uint) (*models.Cluster, bool) {
	return f.gateway, f.gateway != nil && f.gateway.ID == id
}

// Regression: the old host-port upstream could only name one container, so a canary received 0% of traffic
// while the UI reported its weight. The alias upstream must carry the split.
func TestCanaryWeightSplitsTheAliasUpstream(t *testing.T) {
	rel := uint(9)
	app := &models.Application{Alias: "mb-app-tok-7", CanaryReleaseID: &rel, CanaryWeight: 20}

	b := aliasBackends(app, 80, "http")
	if len(b) != 2 {
		t.Fatalf("want a weighted stable+canary split, got %d backend(s): %+v", len(b), b)
	}
	if b[0].Weight != 80 || b[1].Weight != 20 {
		t.Errorf("canary weights lost: got stable=%d canary=%d, want 80/20", b[0].Weight, b[1].Weight)
	}
}
