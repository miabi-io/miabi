// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/placement"
)

func roundTripFields(t *testing.T, app *models.Application, wants ...string) []declarative.FieldDiff {
	t.Helper()
	live := appResource(app, nil, nil, nil, nil, nil)
	b, err := declarative.Marshal(setOf(live))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range wants {
		if !strings.Contains(string(b), want) {
			t.Errorf("bundle is missing %q:\n%s", want, b)
		}
	}
	desired, err := declarative.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	var fields []declarative.FieldDiff
	for _, ch := range declarative.BuildPlan(desired, setOf(live), declarative.PlanOptions{}).Changes {
		fields = append(fields, ch.Fields...)
	}
	return fields
}

// An exported service must apply as itself: re-applying the bundle leaves its replicas and constraints alone.
func TestServiceAppRoundTrips(t *testing.T) {
	app := &models.Application{
		Name: "web", SourceType: models.AppSourceImage, Image: "nginx", Tag: "1.27",
		RuntimeKind: models.RuntimeService, Replicas: 3, PlacementConstraints: []string{"node.labels.disk==ssd"},
		UpdateConfig: &models.ServiceUpdateConfig{Parallelism: 1, DelaySeconds: 10},
	}
	for _, f := range roundTripFields(t, app, "runtime: service", "replicas: 3", "node.labels.disk==ssd", "delaySeconds: 10") {
		if strings.HasPrefix(f.Field, "deployment.") || strings.HasPrefix(f.Field, "placement.") {
			t.Errorf("round trip drifted on %s: %q → %q", f.Field, f.From, f.To)
		}
	}
}

func TestSecurityRoundTrips(t *testing.T) {
	app := &models.Application{
		Name: "web", SourceType: models.AppSourceImage, Image: "nginx", RunAsUser: "1000:1000",
		ReadOnlyRootFilesystem: true, NoNewPrivileges: true,
		AddCapabilities: []string{"NET_BIND_SERVICE"}, DropCapabilities: []string{"ALL"},
	}
	fields := roundTripFields(t, app, "runAsUser: 1000:1000", "readOnlyRootFilesystem: true", "noNewPrivileges: true", "- ALL")
	for _, f := range fields {
		if strings.HasPrefix(f.Field, "security.") {
			t.Errorf("round trip drifted on %s: %q → %q", f.Field, f.From, f.To)
		}
	}
	if sec := securitySpecOf(&models.Application{}); sec != nil {
		t.Errorf("an app on the defaults exported a security block: %+v", sec)
	}
}

func TestLiveAppStatesItsDefaults(t *testing.T) {
	spec := appResource(&models.Application{Name: "web", Image: "nginx", Replicas: 1}, nil, nil, nil, nil, nil).Application
	if spec.Runtime() != "container" || spec.Strategy() != "rolling" || spec.Replicas() != 0 || spec.Constraints() != nil || spec.Update() != nil {
		t.Errorf("deployment = %+v, placement = %+v", spec.Deployment, spec.Placement)
	}
}

func TestExportTrimsDefaults(t *testing.T) {
	spec := appResource(&models.Application{Name: "web", Image: "nginx"}, nil, nil, nil, nil, nil).Application
	spec.Placement = &declarative.ApplicationPlacementSpec{Location: "eu"}
	trimDefaults(spec, "eu")
	if spec.Placement != nil || spec.Deployment != nil {
		t.Errorf("placement = %+v, deployment = %+v, want both trimmed away", spec.Placement, spec.Deployment)
	}
	canary := appResource(&models.Application{Name: "web", Image: "nginx", DeployStrategy: models.DeployCanary}, nil, nil, nil, nil, nil).Application
	trimDefaults(canary, "eu")
	if canary.Strategy() != "canary" || canary.Runtime() != "" {
		t.Errorf("deployment = %+v, want canary kept and the container runtime trimmed", canary.Deployment)
	}
}

func TestSharedCluster(t *testing.T) {
	nodes := map[string]appPlacement{"pg": {cluster: 2}, "cache": {cluster: 2}, "legacy": {cluster: 1}}
	for _, tc := range []struct {
		refs []string
		want uint
	}{
		{[]string{"pg", "cache"}, 2},
		{[]string{"pg", "unknown"}, 2},
		{[]string{"pg", "legacy"}, 0},
		{[]string{"unknown"}, 0},
	} {
		refs := map[string]bool{}
		for _, r := range tc.refs {
			refs[r] = true
		}
		if got := sharedCluster(refs, nodes); got != tc.want {
			t.Errorf("sharedCluster(%v) = %d, want %d", tc.refs, got, tc.want)
		}
	}
}

type fakePlacer struct{ clusters map[string]*models.Cluster }

func (fakePlacer) Place(placement.Request) (placement.Result, error) { return placement.Result{}, nil }
func (fakePlacer) LocationName(uint) string                          { return "" }

func (p fakePlacer) ResolveLocation(_ uint, location string, admin bool) (*models.Cluster, error) {
	c, ok := p.clusters[location]
	switch {
	case !ok:
		return nil, placement.ErrLocationNotFound
	case c.Visibility == models.ClusterVisibilityRestricted && !admin:
		return nil, placement.ErrLocationNotAllowed
	}
	return c, nil
}

type fakeCap struct{ swarm map[uint]bool }

func (c fakeCap) IsSwarm(id uint) bool       { return c.swarm[id] }
func (fakeCap) ClusterOfServer(uint) uint    { return 1 }
func (fakeCap) LocationLabel(id uint) string { return "" }

func planLocations(t *testing.T, manifest string, admin bool) error {
	t.Helper()
	s := &Service{
		placer: fakePlacer{clusters: map[string]*models.Cluster{
			"eu":  {ID: 1, Name: "eu"},
			"us":  {ID: 2, Name: "us"},
			"gpu": {ID: 3, Name: "gpu", Visibility: models.ClusterVisibilityRestricted},
		}},
		cluster: fakeCap{swarm: map[uint]bool{1: true}},
	}
	desired, err := declarative.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	plan := declarative.BuildPlan(desired, declarative.NewResourceSet(), declarative.PlanOptions{})
	return s.checkLocations(7, plan, desired, admin)
}

const webApp = "apiVersion: miabi.io/v1\nkind: Application\nmetadata:\n  name: web\nspec:\n  image: nginx\n"

func located(location string) string { return "  placement:\n    location: " + location + "\n" }

const asService = "  deployment:\n    runtime: service\n"

func TestPlanRefusesAnUnusableLocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		admin    bool
		want     string
	}{
		{"unknown", webApp + located("mars"), false, `location "mars"`},
		{"restricted", webApp + located("gpu"), false, "cannot use that location"},
		{"service off swarm", webApp + located("us") + asService, false, "runs no swarm"},
		{"stack elsewhere", "apiVersion: miabi.io/v1\nkind: Stack\nmetadata:\n  name: shop\nspec:\n" + located("eu") + "---\n" +
			webApp + "  stack: shop\n" + located("us"), false, `its stack "shop" is in "eu"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := planLocations(t, tc.manifest, tc.admin)
			if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want an invalid manifest mentioning %q", err, tc.want)
			}
		})
	}
}

func TestPlanAcceptsUsableLocations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		admin    bool
	}{
		{"unstated", webApp, false},
		{"service on swarm", webApp + located("eu") + asService, false},
		{"restricted for an admin", webApp + located("gpu"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := planLocations(t, tc.manifest, tc.admin); err != nil {
				t.Errorf("err = %v", err)
			}
		})
	}
}
