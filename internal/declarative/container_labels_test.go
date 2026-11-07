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

// An app behind a label-reading proxy is configured by labels, so a label edit must redeploy.
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

// Reserved labels are stripped on write, so diffing them would cause an endless redeploy loop.
func TestReservedContainerLabelDoesNotDrift(t *testing.T) {
	desired := desiredApp(t, "  containerLabels:\n    traefik.enable: \"true\"\n    io.miabi.app: \"999\"\n    com.docker.compose.project: \"mine\"\n")
	live := liveApp(map[string]string{"traefik.enable": "true"})

	plan := d.BuildPlan(desired, live, d.PlanOptions{})
	if _, u, _, _ := plan.Counts(); u != 0 {
		t.Errorf("reserved labels must not drift, got %d updates (%+v)", u, plan.Changes)
	}
}
