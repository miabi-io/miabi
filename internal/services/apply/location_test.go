// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
)

func TestRefuseMove(t *testing.T) {
	moved := declarative.Change{Kind: declarative.KindDatabase, Name: "pg", Fields: []declarative.FieldDiff{
		{Field: "version", From: "16", To: "17"},
		{Field: "location", From: "eu-central", To: "eu-east"},
	}}
	err := refuseMove(moved)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
	for _, want := range []string{`database "pg"`, "eu-central", "eu-east", "Delete it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q: %v", want, err)
		}
	}
	stay := declarative.Change{Kind: declarative.KindStack, Name: "shop", Fields: []declarative.FieldDiff{{Field: "description"}}}
	if err := refuseMove(stay); err != nil {
		t.Errorf("an update that keeps its location was refused: %v", err)
	}
}

// References are read from the raw env: once rendered, the templates are gone.
func TestRefsAreReadBeforeRendering(t *testing.T) {
	refs := refsOf(&declarative.ApplicationSpec{Env: map[string]string{
		"API":   "{{ .applications.api.url }}",
		"DB":    "postgres://{{ .databases.pg.user }}@{{ .databases.pg.host }}",
		"PLAIN": "http://api:8080",
	}})
	if !refs.apps["api"] || !refs.dbs["pg"] || len(refs.apps) != 1 || len(refs.dbs) != 1 {
		t.Errorf("refs = %+v", refs)
	}
	if got := refsOf(nil); len(got.apps)+len(got.dbs) != 0 {
		t.Errorf("nil spec refs = %+v", got)
	}
}
