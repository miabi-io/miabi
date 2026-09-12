// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"strings"
	"testing"
)

const sizedDatabaseYAML = `
apiVersion: miabi.io/v1
kind: Template
metadata:
  name: shop
  displayName: Shop
  version: 1.0.0
databases:
  - name: db
    engine: postgres
    placement: %s
    resources:
      memory: %s
applications:
  - name: shop
    image: nginx
`

func sizedDatabase(placement, memory string) []byte {
	return []byte(strings.NewReplacer("placement: %s", "placement: "+placement, "memory: %s", "memory: "+memory).Replace(sizedDatabaseYAML))
}

func TestDatabaseResourcesParse(t *testing.T) {
	m, err := Parse(sizedDatabase("auto", "1Gi"))
	if err != nil {
		t.Fatal(err)
	}
	if r := m.Databases[0].Resources; r == nil || r.Memory != "1Gi" {
		t.Errorf("resources = %+v", r)
	}
}

func TestDatabaseResourcesValidate(t *testing.T) {
	for _, tc := range []struct{ name, placement, memory, want string }{
		{"bad memory", "auto", "plenty", "invalid memory"},
		{"shared", "shared", "1Gi", "instance of its own"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(sizedDatabase(tc.placement, tc.memory)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
