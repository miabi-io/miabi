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

package appref

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func route(id uint, host string, port int, generated bool) models.Route {
	r := models.Route{Hosts: []string{host}, TargetPort: port, Generated: generated, Enabled: true, Path: "/", TLSMode: models.RouteTLSACME}
	r.ID = id
	return r
}

func TestAddressOf(t *testing.T) {
	app := &models.Application{Port: 8080}
	disabled := route(1, "old.example.com", 8080, false)
	disabled.Enabled = false
	sub := route(2, "api.example.com", 8080, false)
	sub.Path = "/api"
	plain := route(3, "plain.example.com", 0, false)
	plain.TLSMode = models.RouteTLSNone

	tests := []struct {
		name   string
		routes []models.Route
		url    string
		host   string
		ext    string
	}{
		{"none", nil, "", "", ""},
		{"generated only", []models.Route{route(1, "shop-a1.apps.example.com", 8080, true)},
			"https://shop-a1.apps.example.com", "shop-a1.apps.example.com", "https://shop-a1.apps.example.com"},
		{"custom wins over generated", []models.Route{route(1, "shop-a1.apps.example.com", 8080, true), route(2, "shop.example.com", 8080, false)},
			"https://shop.example.com", "shop.example.com", "https://shop-a1.apps.example.com"},
		{"oldest custom wins", []models.Route{route(9, "b.example.com", 8080, false), route(4, "a.example.com", 0, false)},
			"https://a.example.com", "a.example.com", ""},
		{"disabled and non-root custom ignored", []models.Route{disabled, sub, route(5, "shop-a1.apps.example.com", 8080, true)},
			"https://shop-a1.apps.example.com", "shop-a1.apps.example.com", "https://shop-a1.apps.example.com"},
		{"custom on another port ignored", []models.Route{route(1, "metrics.example.com", 9090, false)}, "", "", ""},
		{"tls none is http", []models.Route{plain}, "http://plain.example.com", "plain.example.com", ""},
		{"generated primary is the app port", []models.Route{route(1, "shop-a1-9090.apps.example.com", 9090, true), route(2, "shop-a1.apps.example.com", 8080, true)},
			"https://shop-a1.apps.example.com", "shop-a1.apps.example.com", "https://shop-a1.apps.example.com"},
		{"generated falls back to lowest port", []models.Route{route(1, "shop-a1-9090.apps.example.com", 9090, true), route(2, "shop-a1.apps.example.com", 3000, true)},
			"https://shop-a1.apps.example.com", "shop-a1.apps.example.com", "https://shop-a1.apps.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AddressOf(app, tc.routes)
			if got.URL != tc.url || got.Host != tc.host || got.ExternalURL != tc.ext {
				t.Fatalf("got %+v, want url=%q host=%q ext=%q", got, tc.url, tc.host, tc.ext)
			}
		})
	}
}

func TestRefsAndValidate(t *testing.T) {
	refs := Refs("${{ applications.Web.public_url }}/cb and ${{apps.api.public_host}}")
	if len(refs) != 2 || refs[0] != (Ref{App: "web", Field: "public_url"}) || refs[1] != (Ref{App: "api", Field: "public_host"}) {
		t.Fatalf("refs = %+v", refs)
	}
	if !References("x ${{ apps.web.public_url }}", "WEB") || References("${{ apps.web.public_url }}", "api") {
		t.Fatal("References mismatch")
	}
	if err := ValidateFields("${{ apps.web.pubic_url }}"); err == nil || !strings.Contains(err.Error(), "pubic_url") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
	if err := ValidateFields("${{ applications.not-created-yet.public_url }} ${{ secrets.x }}"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if Token("web", FieldPublicURL) != "${{ applications.web.public_url }}" {
		t.Fatal("token form changed")
	}
}

type fakeApps map[string]*models.Application

func (f fakeApps) FindByName(_ uint, name string) (*models.Application, error) {
	if a, ok := f[name]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}

type fakeRoutes map[uint][]models.Route

func (f fakeRoutes) ListByApp(id uint) ([]models.Route, error) { return f[id], nil }

func TestResolveAll(t *testing.T) {
	web := &models.Application{Name: "web", Port: 8080}
	web.ID = 1
	bare := &models.Application{Name: "bare", Port: 8080}
	bare.ID = 2
	r := NewResolver(fakeApps{"web": web, "bare": bare}, fakeRoutes{1: {route(1, "shop.example.com", 8080, false)}})

	out, err := r.ResolveAll(7, []string{
		"PUBLIC=${{ applications.web.public_url }}",
		"CB=${{ apps.web.public_url }}/auth/callback",
		"HOST=${{ apps.web.public_host }}",
		"KEEP=${{ secrets.token }}",
		"PLAIN=x",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PUBLIC=https://shop.example.com", "CB=https://shop.example.com/auth/callback", "HOST=shop.example.com", "KEEP=${{ secrets.token }}", "PLAIN=x"}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}

	for _, tc := range []struct{ env, want string }{
		{"A=${{ apps.missing.public_url }}", "does not exist"},
		{"A=${{ apps.bare.public_url }}", "no public URL"},
		{"A=${{ apps.web.external_url }}", "no external-access URL"},
		{"A=${{ apps.web.url }}", `no field "url"`},
	} {
		if _, err := r.ResolveAll(7, []string{tc.env}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", tc.env, err, tc.want)
		}
	}

	var nilResolver *Resolver
	if _, err := nilResolver.ResolveAll(7, []string{"A=${{ apps.web.public_url }}"}); err == nil {
		t.Fatal("a nil resolver must refuse references")
	}
	if out, err := nilResolver.ResolveAll(7, []string{"A=b"}); err != nil || out[0] != "A=b" {
		t.Fatal("a nil resolver must pass env without references through")
	}
}
