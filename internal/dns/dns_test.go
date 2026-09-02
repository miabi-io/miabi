// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dns

import (
	"encoding/json"
	"testing"

	"github.com/miabi-io/miabi/internal/dnscatalog"
	"github.com/miabi-io/miabi/internal/models"
)

func TestBuild(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		creds   Credentials
		wantErr bool
	}{
		{"cloudflare ok", models.DNSProviderCloudflare, Credentials{"api_token": "t"}, false},
		{"cloudflare missing token", models.DNSProviderCloudflare, Credentials{}, true},
		{"cloudflare blank token", models.DNSProviderCloudflare, Credentials{"api_token": "  "}, true},
		{"digitalocean ok", models.DNSProviderDigitalOcean, Credentials{"api_token": "t"}, false},
		{"route53 ok", models.DNSProviderRoute53, Credentials{"access_key_id": "a", "secret_access_key": "s", "region": "us-east-1"}, false},
		{"route53 without region", models.DNSProviderRoute53, Credentials{"access_key_id": "a", "secret_access_key": "s"}, false},
		{"route53 missing secret", models.DNSProviderRoute53, Credentials{"access_key_id": "a"}, true},
		{"unknown type", "googledns", Credentials{"api_token": "t"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Build(tc.typ, tc.creds)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got provider %v", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("expected a provider, got nil")
			}
		})
	}
}

func TestToRRRelativizesName(t *testing.T) {
	a := &adapter{}
	rr := a.toRR("example.com.", Record{Type: "TXT", Name: "_miabi-challenge.example.com", Value: "v"})
	if rr.Name != "_miabi-challenge" {
		t.Errorf("name = %q, want %q (relative to zone)", rr.Name, "_miabi-challenge")
	}
	if rr.Type != "TXT" || rr.Data != "v" {
		t.Errorf("unexpected RR %+v", rr)
	}
}

func TestCanonicalZone(t *testing.T) {
	for in, want := range map[string]string{"example.com": "example.com.", "example.com.": "example.com.", "": "."} {
		if got := canonicalZone(in); got != want {
			t.Errorf("canonicalZone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildAcceptsStoredBlobShape(t *testing.T) {
	fixtures := map[string]string{
		models.DNSProviderCloudflare:   `{"api_token":"cf-token"}`,
		models.DNSProviderDigitalOcean: `{"api_token":"do-token"}`,
		models.DNSProviderRoute53:      `{"access_key_id":"AKIA","secret_access_key":"secret","region":"eu-west-1"}`,
	}
	for typ, blob := range fixtures {
		t.Run(typ, func(t *testing.T) {
			var creds Credentials
			if err := json.Unmarshal([]byte(blob), &creds); err != nil {
				t.Fatalf("stored blob no longer unmarshals: %v", err)
			}
			p, err := Build(typ, creds)
			if err != nil || p == nil {
				t.Fatalf("Build from the stored blob failed: %v", err)
			}
		})
	}
}

func TestCredentialsRoundTripToStoredShape(t *testing.T) {
	raw, err := json.Marshal(Credentials{"access_key_id": "AKIA", "secret_access_key": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"access_key_id":"AKIA","secret_access_key":"s"}` {
		t.Fatalf("serialized as %s, want the flat key/value shape older builds read", got)
	}
}

func TestEveryCataloguedTypeBuilds(t *testing.T) {
	for _, d := range dnscatalog.All() {
		t.Run(d.Type, func(t *testing.T) {
			creds := Credentials{}
			for _, f := range d.Fields {
				creds[f.Key] = "x"
			}
			if _, err := Build(d.Type, creds); err != nil {
				t.Fatalf("catalogued type does not build: %v", err)
			}
			for _, f := range d.Fields {
				if !f.Required {
					continue
				}
				partial := Credentials{}
				for k, v := range creds {
					partial[k] = v
				}
				delete(partial, f.Key)
				if _, err := Build(d.Type, partial); err == nil {
					t.Fatalf("Build succeeded without required field %q", f.Key)
				}
			}
		})
	}
}
