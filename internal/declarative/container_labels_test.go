// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func liveApp(labels map[string]string) *d.ResourceSet {
	set := d.NewResourceSet()
	set.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindApplication, Metadata: d.Meta{Name: "web"},
		Application: &d.ApplicationSpec{Image: "nginx", ContainerLabels: labels},
	})
	return set
}

func desiredApp(t *testing.T, labels string) *d.ResourceSet {
	t.Helper()
	src := "apiVersion: miabi.io/v1\nkind: Application\nmetadata: { name: web }\nspec:\n  image: nginx\n" + labels
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return set
}

// An app exposed through a label-reading proxy is configured entirely by its
// labels, so editing a routing rule must redeploy the container. Before this was
// diffed, such an edit planned as nothing at all.
func TestContainerLabelChangeConverges(t *testing.T) {
	desired := desiredApp(t, "  containerLabels:\n    traefik.enable: \"true\"\n    traefik.http.routers.web.rule: \"Host(`new.example.com`)\"\n")
	live := liveApp(map[string]string{
		"traefik.enable":                "true",
		"traefik.http.routers.web.rule": "Host(`old.example.com`)",
	})

	plan := d.BuildPlan(desired, live, d.PlanOptions{})
	if _, u, _, _ := plan.Counts(); u != 1 {
		t.Fatalf("an edited label must plan 1 update, got %d (%+v)", u, plan.Changes)
	}
	fields := plan.Changes[0].Fields
	if len(fields) != 1 {
		t.Fatalf("want only the changed label in the diff, got %+v", fields)
	}
	f := fields[0]
	if f.Field != "containerLabels.traefik.http.routers.web.rule" {
		t.Errorf("field = %q, want the label named per key", f.Field)
	}
	if f.From != "Host(`old.example.com`)" || f.To != "Host(`new.example.com`)" {
		t.Errorf("diff = %+v, want old → new", f)
	}
}

func TestContainerLabelsUnchangedIsNoop(t *testing.T) {
	labels := map[string]string{"traefik.enable": "true", "com.example.team": "platform"}
	desired := desiredApp(t, "  containerLabels:\n    traefik.enable: \"true\"\n    com.example.team: \"platform\"\n")
	if _, u, _, _ := d.BuildPlan(desired, liveApp(labels), d.PlanOptions{}).Counts(); u != 0 {
		t.Errorf("identical labels must be a no-op, got %d updates", u)
	}
}

func TestRemovedContainerLabelConverges(t *testing.T) {
	desired := desiredApp(t, "  containerLabels:\n    traefik.enable: \"true\"\n")
	live := liveApp(map[string]string{"traefik.enable": "true", "traefik.http.routers.web.rule": "Host(`x`)"})

	plan := d.BuildPlan(desired, live, d.PlanOptions{})
	if _, u, _, _ := plan.Counts(); u != 1 {
		t.Fatalf("a dropped label must plan 1 update, got %d (%+v)", u, plan.Changes)
	}
	if f := plan.Changes[0].Fields[0]; f.To != "" || f.Field != "containerLabels.traefik.http.routers.web.rule" {
		t.Errorf("dropped label diff = %+v, want the key going to empty", f)
	}
}

// A platform-reserved key is stripped when the app is written, so it can never
// come back in live state. Diffing it would report drift the apply can never
// resolve — an apply loop that redeploys on every sync.
func TestReservedContainerLabelDoesNotDrift(t *testing.T) {
	desired := desiredApp(t, "  containerLabels:\n    traefik.enable: \"true\"\n    io.miabi.app: \"999\"\n    com.docker.compose.project: \"mine\"\n")
	live := liveApp(map[string]string{"traefik.enable": "true"})

	plan := d.BuildPlan(desired, live, d.PlanOptions{})
	if _, u, _, _ := plan.Counts(); u != 0 {
		t.Errorf("reserved labels must not drift, got %d updates (%+v)", u, plan.Changes)
	}
}
