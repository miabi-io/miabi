// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

func databaseResource(res *declarative.DatabaseResourcesSpec) declarative.Resource {
	return declarative.Resource{
		APIVersion: declarative.APIVersion, Kind: declarative.KindDatabase, Metadata: declarative.Meta{Name: "pg"},
		Database: &declarative.DatabaseSpec{Engine: "postgres", Instance: "auto", Resources: res},
	}
}

// The snapshot's spelling of an instance's limits must match the manifest's, or every plan would resize it.
func TestDatabaseResourcesSnapshotMatchesManifest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inst    models.DatabaseInstance
		desired declarative.DatabaseResourcesSpec
	}{
		{"sized", models.DatabaseInstance{MemoryBytes: 1 << 30, NanoCPUs: 500_000_000}, declarative.DatabaseResourcesSpec{Memory: "1Gi", CPU: "0.5"}},
		{"unlimited", models.DatabaseInstance{}, declarative.DatabaseResourcesSpec{Memory: "0", CPU: "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := databaseResource(databaseResourcesSpec(&tc.inst))
			desired := databaseResource(&tc.desired)
			for _, ch := range declarative.BuildPlan(setOf(desired), setOf(live), declarative.PlanOptions{}).Changes {
				for _, f := range ch.Fields {
					if strings.HasPrefix(f.Field, "resources.") {
						t.Errorf("drifted on %s: %q → %q", f.Field, f.From, f.To)
					}
				}
			}
		})
	}
}

func TestDatabaseResourcesFromManifest(t *testing.T) {
	got := databaseResources(&declarative.DatabaseSpec{Resources: &declarative.DatabaseResourcesSpec{Memory: "512Mi", CPU: "2"}})
	if got.MemoryBytes != 512<<20 || got.NanoCPUs != 2_000_000_000 {
		t.Errorf("resources = %+v", got)
	}
	if got := databaseResources(&declarative.DatabaseSpec{}); got.MemoryBytes != 0 || got.NanoCPUs != 0 {
		t.Errorf("unsized = %+v", got)
	}
}
