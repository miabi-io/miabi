// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

func appYAML(strategy string) string {
	body := `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: ghcr.io/org/web
  ports: [{ container: 8080 }]
`
	if strategy != "" {
		body += "  strategy: " + strategy + "\n"
	}
	return body
}

func TestStrategyParses(t *testing.T) {
	for _, st := range []string{"recreate", "rolling", "canary"} {
		set, err := d.Parse([]byte(appYAML(st)))
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		r, _ := set.Get("Application/web")
		if r.Application.Strategy != st {
			t.Errorf("strategy = %q, want %q", r.Application.Strategy, st)
		}
	}
}

func TestUnknownStrategyIsRejected(t *testing.T) {
	_, err := d.Parse([]byte(appYAML("blue-green")))
	if err == nil || !strings.Contains(err.Error(), "recreate, rolling or canary") {
		t.Errorf("error = %v, want the accepted values named", err)
	}
}

// The declarative package deliberately does not import the model's strategy constants, so the two
// lists can drift. This is what stops that: adding a strategy to the model without teaching the
// manifest about it fails here rather than in a user's bundle.
func TestManifestStrategiesMatchTheModel(t *testing.T) {
	for _, st := range []models.DeployStrategy{models.DeployRecreate, models.DeployRolling, models.DeployCanary} {
		if _, err := d.Parse([]byte(appYAML(string(st)))); err != nil {
			t.Errorf("the model accepts %q but the manifest does not: %v", st, err)
		}
	}
}

// A manifest that says nothing about rollout must not diff against an app configured in the console,
// or every apply would reset it.
func TestOmittedStrategyIsNotDrift(t *testing.T) {
	desired, err := d.Parse([]byte(appYAML("")))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := d.Parse([]byte(appYAML("canary")))
	if err != nil {
		t.Fatal(err)
	}
	if d.BuildPlan(desired, actual, d.PlanOptions{}).HasChanges() {
		t.Error("a manifest silent about strategy planned a change against a canary app")
	}
}

// ...but a manifest that does state one has to converge it.
func TestDeclaredStrategyConverges(t *testing.T) {
	desired, err := d.Parse([]byte(appYAML("canary")))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := d.Parse([]byte(appYAML("rolling")))
	if err != nil {
		t.Fatal(err)
	}
	plan := d.BuildPlan(desired, actual, d.PlanOptions{})
	for _, c := range plan.Changes {
		for _, f := range c.Fields {
			if f.Field == "strategy" && f.From == "rolling" && f.To == "canary" {
				return
			}
		}
	}
	t.Errorf("a strategy change did not plan: %+v", plan.Changes)
}
