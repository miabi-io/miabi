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

package stack

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestComposeVarsExpand(t *testing.T) {
	v := newComposeVars("TAG=1.25\nEMPTY=\nexport HOST=db\n# comment\nQUOTED=\"a b\"\n")
	cases := []struct{ in, want string }{
		{"nginx:${TAG}", "nginx:1.25"},
		{"nginx:$TAG", "nginx:1.25"},
		{"$HOST:5432", "db:5432"},
		{"${QUOTED}", "a b"},
		{"$$HOME and $${TAG}", "$HOME and ${TAG}"},
		{"${MISSING:-fallback}", "fallback"},
		{"${EMPTY:-fallback}", "fallback"},
		{"${EMPTY-fallback}", ""},
		{"${MISSING-${TAG}}", "1.25"},
		{"${TAG:+set}", "set"},
		{"${EMPTY:+set}", ""},
		{"${EMPTY+set}", "set"},
		{"${MISSING+set}", ""},
		{"cost: $5", "cost: $5"},
		{"trailing $", "trailing $"},
	}
	for _, c := range cases {
		got, err := v.expand(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if len(v.unset) != 0 {
		t.Errorf("defaults must not count as unset: %v", v.unset)
	}
}

func TestComposeVarsErrors(t *testing.T) {
	v := newComposeVars("EMPTY=\n")
	for in, want := range map[string]string{
		"${DB_PASSWORD:?set it in .env}": "DB_PASSWORD: set it in .env",
		"${EMPTY:?}":                     "EMPTY: required variable is missing a value",
		"${OPEN":                         "unclosed",
		"${1BAD}":                        "invalid interpolation",
		"${A/b}":                         "invalid interpolation",
	} {
		if _, err := v.expand(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to contain %q", in, err, want)
		}
	}
	if _, err := v.expand("${EMPTY?}"); err != nil {
		t.Errorf("? accepts a set but empty variable: %v", err)
	}
}

func TestComposeVarsWarnsOnUnset(t *testing.T) {
	v := newComposeVars("")
	_, _ = v.expand("${B} $A ${A}")
	if got := v.warnings(); len(got) != 2 || !strings.Contains(got[0], "A ") || !strings.Contains(got[1], "B ") {
		t.Errorf("warnings = %v", got)
	}
}

type recordingApp struct {
	fakeApp
	nextID  uint
	created map[string]application.CreateInput
	env     map[uint]map[string]string
	names   map[uint]string
}

func (r *recordingApp) Create(_ uint, in application.CreateInput) (*models.Application, error) {
	r.nextID++
	r.created[in.Handle] = in
	r.names[r.nextID] = in.Handle
	return &models.Application{ID: r.nextID, Name: in.Handle}, nil
}

func (r *recordingApp) SetEnvVar(id uint, k, v string, _ bool) error {
	if r.env[id] == nil {
		r.env[id] = map[string]string{}
	}
	r.env[id][k] = v
	return nil
}

func (r *recordingApp) envOf(handle string) map[string]string {
	for id, n := range r.names {
		if n == handle {
			return r.env[id]
		}
	}
	return nil
}

func newImportService(t *testing.T) (*Service, *recordingApp) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&stackRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	app := &recordingApp{created: map[string]application.CreateInput{}, env: map[uint]map[string]string{}, names: map[uint]string{}}
	return NewService(repositories.NewStackRepository(db), nil, nil, nil, app, fakeVols{}, nil, nil), app
}

func TestImportComposeWithDotEnv(t *testing.T) {
	s, app := newImportService(t)
	const compose = `
services:
  web:
    image: nginx:${TAG:-latest}
    ports:
      - "${WEB_PORT}:80"
    env_file: .env
    environment:
      MODE: production
      GREETING: "hello ${NAME}"
  worker:
    image: busybox
    command: ["sh", "-c", "echo $${HOME}"]
    env_file:
      - path: ./.env
      - path: ./worker.env
        required: false
      - other.env
`
	res, err := s.ImportCompose(context.Background(), 1, 1, "site", compose, "TAG=1.27\nWEB_PORT=8080\nMODE=dev\nAPI_KEY=k\n", ImportPlacement{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := app.created["web"].Image; got != "nginx:1.27" {
		t.Errorf("web image = %q", got)
	}
	if got := app.created["web"].Ports; len(got) != 1 || got[0].ContainerPort != 80 {
		t.Errorf("web ports = %+v", got)
	}
	if got := app.created["worker"].Command; !slices.Equal(got, []string{"sh", "-c", "echo ${HOME}"}) {
		t.Errorf("worker command = %q", got)
	}
	web := app.envOf("web")
	if web["MODE"] != "production" || web["API_KEY"] != "k" || web["GREETING"] != "hello " {
		t.Errorf("web env = %v (environment must beat env_file)", web)
	}
	if app.envOf("worker")["API_KEY"] != "k" {
		t.Errorf("worker env = %v", app.envOf("worker"))
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "NAME") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	var reasons []string
	for _, sk := range res.Skipped {
		reasons = append(reasons, sk.Service+": "+sk.Reason)
	}
	if !slices.Equal(reasons, []string{"worker: env_file other.env: only .env can be provided"}) {
		t.Errorf("skipped = %v", reasons)
	}
}

func TestImportComposeEnvFileWithoutDotEnv(t *testing.T) {
	s, app := newImportService(t)
	res, err := s.ImportCompose(context.Background(), 1, 1, "site", "services:\n  web:\n    image: nginx\n    env_file: .env\n", "", ImportPlacement{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(app.envOf("web")) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "no .env was provided") {
		t.Errorf("env = %v, skipped = %+v", app.envOf("web"), res.Skipped)
	}
}

func TestImportComposeRequiredVariableFailsBeforeCreating(t *testing.T) {
	s, app := newImportService(t)
	const compose = "services:\n  db:\n    image: postgres\n    environment:\n      POSTGRES_PASSWORD: ${DB_PASSWORD:?set DB_PASSWORD in .env}\n"
	_, err := s.ImportCompose(context.Background(), 1, 1, "site", compose, "", ImportPlacement{})
	if !errors.Is(err, ErrComposeInvalid) || !strings.Contains(err.Error(), "set DB_PASSWORD in .env") {
		t.Fatalf("err = %v", err)
	}
	if len(app.created) != 0 {
		t.Errorf("nothing may be created on a failed interpolation: %v", app.created)
	}
	if _, err := s.ImportCompose(context.Background(), 1, 1, "site", "services:\n  db:\n    image: postgres\n", "", ImportPlacement{}); err != nil {
		t.Errorf("the stack name must still be free after a failed import: %v", err)
	}
}

func TestImportComposeValuesCannotInjectYAML(t *testing.T) {
	s, app := newImportService(t)
	// Spliced into the raw text, this would turn the flow mapping into {A: x, B: injected}.
	const dotEnv = "EVIL=x, B: injected\n"
	_, err := s.ImportCompose(context.Background(), 1, 1, "site", "services:\n  web:\n    image: nginx\n    environment: {A: \"${EVIL}\"}\n", dotEnv, ImportPlacement{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := app.envOf("web"); len(got) != 1 || got["A"] != "x, B: injected" {
		t.Errorf("env = %v", got)
	}
}
