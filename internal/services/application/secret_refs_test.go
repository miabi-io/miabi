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

package application

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

type fakeStackEnv map[uint][]models.StackEnvVar

func (f fakeStackEnv) ListByStack(id uint) ([]models.StackEnvVar, error) { return f[id], nil }

// A secret reaches an app through its stack's shared env as much as its own, and a manifest may mark a
// pure reference secret; missing either leaves rotation and the delete guard blind to it.
func TestAppReferencesSecretCoversStackAndSecretEnv(t *testing.T) {
	crypto.Init("test-master-key-for-secret-refs")
	t.Cleanup(func() { crypto.Init("") })
	enc, err := crypto.EncryptWS(1, "${{ secrets.enc }}")
	if err != nil {
		t.Fatal(err)
	}
	stackID := uint(7)
	s := &Service{stackEnv: fakeStackEnv{stackID: {
		{Key: "JWT", Value: "${{ secrets.jwt }}"},
		{Key: "SHADOWED", Value: "${{ secrets.shadowed }}"},
	}}}
	app := &models.Application{WorkspaceID: 1, StackID: &stackID, EnvVars: []models.AppEnvVar{
		{Key: "OWN", Value: "${{ secrets.own }}"},
		{Key: "ENC", Value: enc, IsSecret: true},
		{Key: "SHADOWED", Value: "literal"},
	}}
	cases := map[string]bool{"own": true, "enc": true, "jwt": true, "shadowed": false, "other": false}
	for name, want := range cases {
		if got := s.appReferencesSecret(app, name, map[uint][]models.StackEnvVar{}); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}
