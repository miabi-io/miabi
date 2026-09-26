// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
)

func TestTrustedProxyRealIP(t *testing.T) {
	proxies, err := parseTrustedProxies("10.62.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	app := okapi.New()
	app.WithTrustedProxies("10.62.0.0/16")
	app.Use(preferProxyRealIP(proxies))
	app.Get("/ip", func(c *okapi.Context) error { return c.String(http.StatusOK, c.RealIP()) })

	tests := []struct {
		name, peer, xff, xRealIP, want string
	}{
		{"gateway facing clients", "10.62.0.2:4000", "198.51.100.7, 198.51.100.7", "198.51.100.7", "198.51.100.7"},
		{"gateway behind a load balancer", "10.62.0.2:4000", "198.51.100.7, 203.0.113.1", "198.51.100.7", "198.51.100.7"},
		{"client spoofing past the gateway", "203.0.113.9:4000", "1.2.3.4", "1.2.3.4", "203.0.113.9"},
		{"gateway without X-Real-IP", "10.62.0.2:4000", "1.2.3.4, 198.51.100.7", "", "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ip", nil)
			r.RemoteAddr = tt.peer
			r.Header.Set("X-Forwarded-For", tt.xff)
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, r)
			if got := rec.Body.String(); got != tt.want {
				t.Fatalf("RealIP = %q, want %q", got, tt.want)
			}
		})
	}
}
