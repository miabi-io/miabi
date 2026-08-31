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

// A git-source app must round-trip: an app exported from the console and re-applied has to come back
// as the same app, not as an image pull of whatever its last build produced.
func TestSourceBlockParses(t *testing.T) {
	set, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  source:
    git: https://github.com/acme/web
    ref: main
    buildMethod: buildpack
    builder: paketobuildpacks/builder-jammy-base
    buildEnv: { BP_GO_VERSION: "1.26" }
  ports: [{ container: 8080 }]
`))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Application/web")
	src := r.Application.Source
	if src == nil || src.Git != "https://github.com/acme/web" || src.Ref != "main" {
		t.Fatalf("source = %+v", src)
	}
	if src.BuildMethod != "buildpack" || src.BuildEnv["BP_GO_VERSION"] != "1.26" {
		t.Errorf("build config lost: %+v", src)
	}
}

func TestSourceAndImageAreMutuallyExclusive(t *testing.T) {
	_, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  image: nginx
  source: { git: https://github.com/acme/web }
`))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %v, want the conflict named", err)
	}
}

func TestApplicationNeedsImageOrSource(t *testing.T) {
	_, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { tag: "1.0" }
`))
	if err == nil || !strings.Contains(err.Error(), "one of image or source") {
		t.Errorf("error = %v", err)
	}
}

func TestSourceValidation(t *testing.T) {
	cases := []struct{ name, spec, want string }{
		{"git required", "source: { ref: main }", "source.git is required"},
		{"bad build method", "source: { git: https://x/y, buildMethod: kaniko }", "auto, dockerfile or buildpack"},
		{"buildpack settings on a dockerfile build", "source: { git: https://x/y, buildMethod: dockerfile, builder: paketo }", "not buildMethod: dockerfile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse([]byte("apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  " + tc.spec + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Repointing an app at another repo or ref is a real change and must converge, not sit as drift the
// plan cannot see.
func TestSourceChangesConverge(t *testing.T) {
	mk := func(ref string) *d.ResourceSet {
		set, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec:
  source: { git: https://github.com/acme/web, ref: ` + ref + ` }
  ports: [{ container: 8080 }]
`))
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	if !d.BuildPlan(mk("next"), mk("main"), d.PlanOptions{}).HasChanges() {
		t.Error("changing the built ref did not plan a change")
	}
	if d.BuildPlan(mk("main"), mk("main"), d.PlanOptions{}).HasChanges() {
		t.Error("an unchanged source planned a change")
	}
}

// The declarative package does not import the model's build-method constants, so the lists can
// drift. This is what stops that.
func TestManifestBuildMethodsMatchTheModel(t *testing.T) {
	for _, m := range []models.AppBuildMethod{models.BuildAuto, models.BuildDockerfile, models.BuildBuildpack} {
		y := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  source: { git: https://x/y, buildMethod: " + string(m) + " }\n"
		if _, err := d.Parse([]byte(y)); err != nil {
			t.Errorf("the model accepts %q but the manifest does not: %v", m, err)
		}
	}
}
