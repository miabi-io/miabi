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

package worker

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type stubAppRefs struct{ value string }

func (s stubAppRefs) ResolveAll(_ uint, env []string) ([]string, error) {
	out := make([]string, len(env))
	for i, kv := range env {
		out[i] = strings.ReplaceAll(kv, "${{ apps.web.public_url }}", s.value)
	}
	return out, nil
}

func TestBuildEnvResolvesAppRefs(t *testing.T) {
	app := &models.Application{MiabiEnvDisabled: true, EnvVars: []models.AppEnvVar{{Key: "PUBLIC", Value: "${{ apps.web.public_url }}/cb"}}}

	b := &runtimeBuilder{appRefs: stubAppRefs{value: "https://shop.example.com"}}
	env, err := b.buildEnv(app, managedRun{})
	if err != nil || len(env) != 1 || env[0] != "PUBLIC=https://shop.example.com/cb" {
		t.Fatalf("env = %v, err = %v", env, err)
	}

	// A process without a resolver must refuse rather than start the app with the literal token.
	if _, err := (&runtimeBuilder{}).buildEnv(app, managedRun{}); err == nil || !strings.Contains(err.Error(), "PUBLIC") {
		t.Fatalf("want a refusal naming PUBLIC, got %v", err)
	}
}
