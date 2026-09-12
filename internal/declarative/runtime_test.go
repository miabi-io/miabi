// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func appManifest(spec string) []byte {
	return []byte("apiVersion: miabi.io/v1\nkind: Application\nmetadata:\n  name: web\nspec:\n  image: nginx\n" + spec)
}

func TestServiceRuntimeParses(t *testing.T) {
	set, err := d.Parse(appManifest("  placement:\n    location: eu\n    constraints: [\"node.labels.disk==ssd\"]\n" +
		"  deployment:\n    runtime: service\n    replicas: 3\n    update:\n      parallelism: 1\n      delaySeconds: 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Application/web")
	a := r.Application
	if a.Runtime() != "service" || a.Replicas() != 3 || len(a.Constraints()) != 1 || a.Location() != "eu" ||
		a.Update() == nil || a.Update().DelaySeconds != 10 {
		t.Errorf("spec = %+v", a)
	}
}

func TestRuntimeValidation(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"unknown runtime", "  deployment:\n    runtime: vm\n", "must be container or service"},
		{"replicas on a container", "  deployment:\n    replicas: 3\n", "need deployment.runtime: service"},
		{"constraints on a container", "  deployment:\n    runtime: container\n  placement:\n    constraints: [\"node.role==worker\"]\n", "need deployment.runtime: service"},
		{"too many replicas", "  deployment:\n    runtime: service\n    replicas: 101\n", "between 1 and 100"},
		{"malformed constraint", "  deployment:\n    runtime: service\n  placement:\n    constraints: [\"node.role worker\"]\n", "must read"},
		{"negative delay", "  deployment:\n    runtime: service\n    update:\n      delaySeconds: -1\n", "cannot be negative"},
		{"devices on a service", "  deployment:\n    runtime: service\n  security:\n    devices: [/dev/net/tun]\n", "replicated service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse(appManifest(tc.spec))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func appSet(spec d.ApplicationSpec) *d.ResourceSet {
	spec.Image = "nginx"
	set := d.NewResourceSet()
	set.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "web"}, Application: &spec})
	return set
}

func appFields(desired, actual d.ApplicationSpec) []d.FieldDiff {
	for _, c := range d.BuildPlan(appSet(desired), appSet(actual), d.PlanOptions{}).Changes {
		if c.Kind == d.KindApplication && c.Name == "web" {
			return c.Fields
		}
	}
	return nil
}

// A service scaled in the console must not be scaled back by a manifest that never mentioned replicas.
func TestUnstatedServiceSettingsAreNotDrift(t *testing.T) {
	live := d.ApplicationSpec{
		Deployment: &d.DeploymentSpec{Runtime: "service", Replicas: 4, Update: &d.UpdateSpec{Parallelism: 2}},
		Placement:  &d.ApplicationPlacementSpec{Constraints: []string{"node.role==worker"}},
	}
	if fields := appFields(d.ApplicationSpec{}, live); len(fields) != 0 {
		t.Errorf("fields = %+v, want no drift", fields)
	}
	if fields := appFields(d.ApplicationSpec{Deployment: &d.DeploymentSpec{Runtime: "service"}}, live); len(fields) != 0 {
		t.Errorf("fields = %+v, want no drift", fields)
	}
}

func TestStatedServiceSettingsConverge(t *testing.T) {
	live := d.ApplicationSpec{
		Deployment: &d.DeploymentSpec{Runtime: "service", Replicas: 4},
		Placement:  &d.ApplicationPlacementSpec{Constraints: []string{"node.role==worker"}},
	}
	desired := d.ApplicationSpec{
		Deployment: &d.DeploymentSpec{Runtime: "service", Replicas: 2},
		Placement:  &d.ApplicationPlacementSpec{Constraints: []string{}},
	}
	got := map[string]d.FieldDiff{}
	fields := appFields(desired, live)
	for _, f := range fields {
		got[f.Field] = f
	}
	if len(fields) != 2 || got["deployment.replicas"].To != "2" ||
		got["placement.constraints"].From != "node.role==worker" || got["placement.constraints"].To != "" {
		t.Errorf("fields = %+v, want replicas 4 → 2 and constraints cleared", fields)
	}
	fields = appFields(d.ApplicationSpec{Deployment: &d.DeploymentSpec{Runtime: "container"}}, live)
	if len(fields) != 1 || fields[0].Field != "deployment.runtime" {
		t.Errorf("fields = %+v, want the runtime change alone", fields)
	}
}
