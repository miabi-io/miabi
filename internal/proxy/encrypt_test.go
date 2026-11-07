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

package proxy

import (
	"strings"
	"testing"

	"github.com/jkaninda/encryptor"
	"gopkg.in/yaml.v3"
)

const testConfigKey = "shared-goma-miabi-key"

func TestRenderMiddlewareEncryptsRule(t *testing.T) {
	t.Setenv(ConfigEncryptionKeyEnv, testConfigKey)

	out, err := RenderMiddleware(RenderedMiddleware{
		WorkspaceID: 1, Name: "auth", Type: "basicAuth",
		Rule: map[string]interface{}{"realm": "restricted", "users": []string{"admin:secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "-----BEGIN PGP MESSAGE-----") {
		t.Fatalf("rendered rule is not encrypted:\n%s", out)
	}
	if strings.Contains(string(out), "restricted") {
		t.Fatalf("plaintext rule leaked into output:\n%s", out)
	}

	// Parse the rendered file, pull out the encrypted scalar and decrypt it.
	var doc gomaFile
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	enc, ok := doc.Middlewares[0].Rule.(string)
	if !ok || !encryptor.IsEncrypted(enc) {
		t.Fatalf("expected encrypted scalar rule, got %T", doc.Middlewares[0].Rule)
	}
	plain, err := encryptor.DecryptString(enc, testConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	var rule map[string]interface{}
	if err := yaml.Unmarshal(plain, &rule); err != nil {
		t.Fatal(err)
	}
	if rule["realm"] != "restricted" {
		t.Fatalf("decrypted rule mismatch: %+v", rule)
	}
}

func TestRenderTLSEncryptsCert(t *testing.T) {
	t.Setenv(ConfigEncryptionKeyEnv, testConfigKey)

	out, err := RenderRoute(RenderedRoute{
		WorkspaceID: 1, Name: "r1", Path: "/", Hosts: []string{"example.com"},
		Backends: []Backend{{Endpoint: "http://app:8080"}},
		Certs:    []CertPair{{CertPEM: "CERT-PEM", KeyPEM: "KEY-PEM"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "CERT-PEM") {
		t.Fatalf("plaintext cert leaked into output:\n%s", out)
	}
	if !strings.Contains(string(out), "-----BEGIN PGP MESSAGE-----") {
		t.Fatalf("rendered cert is not encrypted:\n%s", out)
	}
}

func TestRenderWithoutKeyStaysPlaintext(t *testing.T) {
	t.Setenv(ConfigEncryptionKeyEnv, "")

	out, err := RenderMiddleware(RenderedMiddleware{
		WorkspaceID: 1, Name: "auth", Type: "basicAuth",
		Rule: map[string]interface{}{"realm": "restricted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "restricted") {
		t.Fatalf("expected plaintext rule, got:\n%s", out)
	}
	if strings.Contains(string(out), "PGP MESSAGE") {
		t.Fatalf("unexpected encryption with no key set:\n%s", out)
	}
}
