// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const registryBundle = `apiVersion: miabi.io/v1
kind: Registry
metadata: { name: ghcr }
spec:
  server: ghcr.io
  username: my-org
  password: tok-1
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec:
  image: ghcr.io/my-org/api
  registry: ghcr`

func TestRegistryKindParsesAndLinks(t *testing.T) {
	set, err := d.Parse([]byte(registryBundle))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	regs := set.ByKind(d.KindRegistry)
	if len(regs) != 1 {
		t.Fatalf("want 1 registry, got %d", len(regs))
	}
	if regs[0].Registry.Server != "ghcr.io" || regs[0].Registry.Username != "my-org" {
		t.Errorf("registry spec mismatch: %+v", regs[0].Registry)
	}
	apps := set.ByKind(d.KindApplication)
	if apps[0].Application.Registry != "ghcr" {
		t.Errorf("app registry = %q, want ghcr", apps[0].Application.Registry)
	}

	// The credential must exist before the app that pulls through it.
	plan := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	var order []string
	for _, ch := range plan.Changes {
		order = append(order, string(ch.Kind))
	}
	if len(order) != 2 || order[0] != "Registry" || order[1] != "Application" {
		t.Errorf("apply order = %v, want [Registry Application]", order)
	}

	// The dependency is drawn only when both ends are in the bundle.
	var found bool
	for _, e := range d.Edges(set) {
		if e.From == "Application/api" && e.To == "Registry/ghcr" && e.Type == d.EdgeRegistry {
			found = true
		}
	}
	if !found {
		t.Errorf("missing Application->Registry edge in %+v", d.Edges(set))
	}
}

// A registry's password is normally "{{ .secrets.x }}", and the apply engine renders it strictly
// at execute time, so a Secret declared in the same bundle must be created first. Same-rank
// changes order by kind name, which would put Registry before Secret; the ranks keep them apart.
func TestSecretIsCreatedBeforeRegistry(t *testing.T) {
	src := `apiVersion: miabi.io/v1
kind: Registry
metadata: { name: ghcr }
spec:
  server: ghcr.io
  username: my-org
  password: "{{ .secrets.tok }}"
---
apiVersion: miabi.io/v1
kind: Secret
metadata: { name: tok }
spec: { generate: true }`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	var order []string
	for _, ch := range plan.Changes {
		order = append(order, string(ch.Kind))
	}
	if len(order) != 2 || order[0] != "Secret" || order[1] != "Registry" {
		t.Errorf("apply order = %v, want [Secret Registry]", order)
	}
}

// A registry referenced but not declared is legal: it resolves against the
// workspace's existing credentials at apply time.
func TestRegistryReferenceNeedNotBeDeclared(t *testing.T) {
	src := `apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec:
  image: ghcr.io/my-org/api
  registry: created-in-the-ui`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("an undeclared registry reference must parse: %v", err)
	}
	if e := d.Edges(set); len(e) != 0 {
		t.Errorf("no edge should be drawn to a resource outside the set, got %+v", e)
	}
}

func TestRegistryServerNormalizedAndValidated(t *testing.T) {
	// A scheme is stripped and an omitted host defaults to Docker Hub, so a
	// manifest converges against what the registry service stores.
	for _, tc := range []struct{ in, want string }{
		{"", d.DefaultRegistryServer},
		{"https://ghcr.io", "ghcr.io"},
		{"registry.example.com:5000/", "registry.example.com:5000"},
	} {
		src := "apiVersion: miabi.io/v1\nkind: Registry\nmetadata: { name: r }\nspec:\n  username: u\n  password: p\n  server: \"" + tc.in + "\""
		set, err := d.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse server %q: %v", tc.in, err)
		}
		if got := set.ByKind(d.KindRegistry)[0].Registry.Server; got != tc.want {
			t.Errorf("server %q normalized to %q, want %q", tc.in, got, tc.want)
		}
	}

	bad := "apiVersion: miabi.io/v1\nkind: Registry\nmetadata: { name: r }\nspec: { server: \"ghcr.io/org/repo\" }"
	if _, err := d.Parse([]byte(bad)); err == nil {
		t.Error("a server with a path must be rejected")
	}
	noUser := "apiVersion: miabi.io/v1\nkind: Registry\nmetadata: { name: r }\nspec: { password: p }"
	if _, err := d.Parse([]byte(noUser)); err == nil {
		t.Error("a password without a username must be rejected")
	}
}

func TestRegistryDiffHidesThePassword(t *testing.T) {
	desired := d.NewResourceSet()
	desired.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindRegistry, Metadata: d.Meta{Name: "ghcr"},
		Registry: &d.RegistrySpec{Server: "ghcr.io", Username: "my-org", Password: "tok-2", PasswordFP: "bbbb"},
	})
	live := func(fp string) *d.ResourceSet {
		s := d.NewResourceSet()
		s.Add(d.Resource{
			APIVersion: d.APIVersion, Kind: d.KindRegistry, Metadata: d.Meta{Name: "ghcr"},
			Registry: &d.RegistrySpec{Server: "ghcr.io", Username: "my-org", PasswordFP: fp},
		})
		return s
	}

	// Same fingerprint: converged, no churn.
	if _, u, _, _ := d.BuildPlan(desired, live("bbbb"), d.PlanOptions{}).Counts(); u != 0 {
		t.Errorf("an unchanged credential must be a no-op, got %d updates", u)
	}

	// Rotated: one update, reported without anything derived from the token.
	plan := d.BuildPlan(desired, live("aaaa"), d.PlanOptions{})
	if _, u, _, _ := plan.Counts(); u != 1 {
		t.Fatalf("a rotated password must be 1 update, got %d (%+v)", u, plan.Changes)
	}
	fields := plan.Changes[0].Fields
	if len(fields) != 1 || fields[0].Field != "password" {
		t.Fatalf("want a single password field diff, got %+v", fields)
	}
	if fields[0].From != "(current)" || fields[0].To != "(rotated)" {
		t.Errorf("password diff = %+v, want masked labels", fields[0])
	}
	for _, f := range fields {
		if strings.Contains(f.From+f.To, "tok-") || strings.Contains(f.From+f.To, "aaaa") || strings.Contains(f.From+f.To, "bbbb") {
			t.Errorf("plan leaked password material: %+v", f)
		}
	}
}

// A credential whose token is managed out-of-band (declared in git, pasted in the
// UI) must not read as drift on every plan.
func TestRegistryWithoutPasswordDoesNotDrift(t *testing.T) {
	desired := d.NewResourceSet()
	desired.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindRegistry, Metadata: d.Meta{Name: "ghcr"},
		Registry: &d.RegistrySpec{Server: "ghcr.io", Username: "my-org"},
	})
	live := d.NewResourceSet()
	live.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindRegistry, Metadata: d.Meta{Name: "ghcr"},
		Registry: &d.RegistrySpec{Server: "ghcr.io", Username: "my-org", PasswordFP: "aaaa"},
	})
	if _, u, _, _ := d.BuildPlan(desired, live, d.PlanOptions{}).Counts(); u != 0 {
		t.Errorf("an out-of-band token must not drift, got %d updates", u)
	}
}

func TestApplicationRegistryChangeIsDrift(t *testing.T) {
	desired, _ := d.Parse([]byte(registryBundle))
	live := d.NewResourceSet()
	live.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindRegistry, Metadata: d.Meta{Name: "ghcr"},
		Registry: &d.RegistrySpec{Server: "ghcr.io", Username: "my-org"},
	})
	live.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "api"},
		Application: &d.ApplicationSpec{Image: "ghcr.io/my-org/api", Registry: "dockerhub"},
	})
	plan := d.BuildPlan(desired, live, d.PlanOptions{})
	for _, ch := range plan.Changes {
		if ch.Kind != d.KindApplication {
			continue
		}
		for _, f := range ch.Fields {
			if f.Field == "registry" && f.From == "dockerhub" && f.To == "ghcr" {
				return
			}
		}
	}
	t.Errorf("re-pointing an app at another credential must show as drift: %+v", plan.Changes)
}

// A manifest may carry either secret form: "{{ .secrets.x }}" is rendered at apply time into a
// stored copy, while "${{ secrets.X }}" is stored as a live reference resolved at every pull. The
// runtime form must survive parsing untouched — Go's template engine would choke on it.
func TestRegistryRuntimeSecretReferenceIsNotATemplate(t *testing.T) {
	src := `apiVersion: miabi.io/v1
kind: Registry
metadata: { name: ghcr }
spec:
  server: ghcr.io
  username: my-org
  password: "${{ secrets.GHCR_TOKEN }}"`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := set.ByKind(d.KindRegistry)[0].Registry.Password
	if got != "${{ secrets.GHCR_TOKEN }}" {
		t.Errorf("password = %q, want the reference verbatim", got)
	}
	// And it must survive the Marshal round trip GitOps canonicalizes through.
	out, err := d.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := d.Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := back.ByKind(d.KindRegistry)[0].Registry.Password; got != "${{ secrets.GHCR_TOKEN }}" {
		t.Errorf("password after round trip = %q, want the reference verbatim", got)
	}
}

// GitOps canonicalizes a bundle through Marshal before applying it, so a
// registry (password included) has to survive the round trip.
func TestRegistryMarshalRoundTrips(t *testing.T) {
	set, err := d.Parse([]byte(registryBundle))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := d.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := d.Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	got := back.ByKind(d.KindRegistry)
	if len(got) != 1 {
		t.Fatalf("want 1 registry after round trip, got %d", len(got))
	}
	if got[0].Registry.Password != "tok-1" {
		t.Errorf("password lost in round trip: %+v", got[0].Registry)
	}
	if apps := back.ByKind(d.KindApplication); apps[0].Application.Registry != "ghcr" {
		t.Errorf("app registry lost in round trip: %+v", apps[0].Application)
	}
}
