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

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

type gatewayAuthRecorder struct{ name, token string }

func (g *gatewayAuthRecorder) AuthenticateGateway(name, token string) (*models.Cluster, error) {
	g.name, g.token = name, token
	return nil, errors.New("invalid cluster gateway token")
}

func TestClusterProviderRejectsWithoutAValidClusterToken(t *testing.T) {
	get := func(h *ProviderHandler, auth string) int {
		app := okapi.New()
		app.Get("/api/v1/providers/{cluster}", h.ClusterFull)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/paris", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := get(&ProviderHandler{}, "Bearer mbgw_x"); code != http.StatusUnauthorized {
		t.Errorf("no cluster authenticator: status %d, want 401", code)
	}
	auth := &gatewayAuthRecorder{}
	h := &ProviderHandler{}
	h.SetClusterGateways(auth)
	if code := get(h, "Bearer mbgw_x"); code != http.StatusUnauthorized {
		t.Errorf("bad token: status %d, want 401", code)
	}
	if auth.name != "paris" || auth.token != "mbgw_x" {
		t.Errorf("authenticated %q with %q, want paris with the bearer token", auth.name, auth.token)
	}
}
