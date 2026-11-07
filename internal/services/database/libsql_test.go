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

package database

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/miabi-io/miabi/internal/models"
)

func TestLibsqlEngineSpec(t *testing.T) {
	spec, ok := specs[models.DBEngineLibSQL]
	if !ok {
		t.Fatal("libsql missing from specs")
	}
	if spec.port != 8080 || spec.dataDir != "/var/lib/sqld" || spec.adminUser != "" {
		t.Errorf("unexpected spec: port=%d dir=%s admin=%q", spec.port, spec.dataDir, spec.adminUser)
	}
	if got := spec.image("latest"); got != "ghcr.io/tursodatabase/libsql-server:latest" {
		t.Errorf("image = %q", got)
	}
	// libSQL builds its server env in bringUp (JWT), not via adminEnv.
	if env := spec.adminEnv("", "x"); env != nil {
		t.Errorf("adminEnv should be nil, got %v", env)
	}
}

func TestLibsqlLogicalClassification(t *testing.T) {
	if models.EngineSupportsLogicalDatabases(models.DBEngineLibSQL) {
		t.Error("libsql should not support user-managed logical databases")
	}
	if !models.EngineUsesLogicalDatabaseRecord(models.DBEngineLibSQL) {
		t.Error("libsql should use a logical database record")
	}
}

func TestLibsqlConnectionURI(t *testing.T) {
	d := &models.Database{Name: libsqlDatabaseName, Username: libsqlUsername, PasswordEnc: mustEncrypt(t, "tok123")}
	inst := &models.DatabaseInstance{Engine: models.DBEngineLibSQL, Host: "mb-db-1", Port: 8080}
	svc := &Service{}
	conn, err := svc.DatabaseConnection(inst, d)
	if err != nil {
		t.Fatal(err)
	}
	if conn.URI != "libsql://mb-db-1:8080?authToken=tok123" {
		t.Errorf("uri = %q", conn.URI)
	}
	if conn.Password != "tok123" || conn.Database != libsqlDatabaseName {
		t.Errorf("conn = %+v", conn)
	}
}

func TestLibsqlKeypairMintAndVerify(t *testing.T) {
	privEnc, token, err := libsqlNewKeypair(1)
	if err != nil {
		t.Fatal(err)
	}
	if privEnc == "" || token == "" {
		t.Fatal("empty keypair output")
	}
	priv, err := libsqlPrivateKey(privEnc)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("public key not ed25519")
	}
	// The token must verify with the matching public key and grant read-write.
	parsed, err := jwt.Parse(token, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodEd25519); !ok {
			t.Errorf("unexpected signing method %v", tok.Header["alg"])
		}
		return pub, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("token did not verify: %v", err)
	}
	if claims, ok := parsed.Claims.(jwt.MapClaims); !ok || claims["a"] != "rw" {
		t.Errorf("claims = %v", parsed.Claims)
	}
}

func TestLibsqlServerEnv(t *testing.T) {
	privEnc, _, err := libsqlNewKeypair(1)
	if err != nil {
		t.Fatal(err)
	}
	inst := &models.DatabaseInstance{Engine: models.DBEngineLibSQL, JWTPrivateKeyEnc: privEnc}
	env, err := libsqlServerEnv(inst)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, " ")
	for _, want := range []string{"SQLD_NODE=primary", "SQLD_HTTP_LISTEN_ADDR=0.0.0.0:8080", "SQLD_AUTH_JWT_KEY="} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q: %v", want, env)
		}
	}
	// The advertised key must be the base64url (no pad) of the derived public key.
	priv, _ := libsqlPrivateKey(privEnc)
	pub := priv.Public().(ed25519.PublicKey)
	wantKey := "SQLD_AUTH_JWT_KEY=" + base64.RawURLEncoding.EncodeToString(pub)
	if !strings.Contains(joined, wantKey) {
		t.Errorf("public key env = %v, want %q", env, wantKey)
	}
}

func TestLibsqlUpgradePathInPlace(t *testing.T) {
	if _, _, err := upgradePath(models.DBEngineLibSQL, "latest", "latest"); err != ErrAlreadyOnVersion {
		t.Errorf("same tag err = %v, want ErrAlreadyOnVersion", err)
	}
	path, major, err := upgradePath(models.DBEngineLibSQL, "latest", "v0.24")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if path != PathInPlace || major {
		t.Errorf("path = %q major = %v, want in-place/false", path, major)
	}
}

func TestLibsqlRecreateDatabaseNoop(t *testing.T) {
	// Force restore on libSQL has no DDL and must not touch the repo, which is nil here.
	svc := &Service{}
	inst := &models.DatabaseInstance{Engine: models.DBEngineLibSQL}
	if err := svc.RecreateDatabase(context.Background(), inst, &models.Database{}); err != nil {
		t.Errorf("RecreateDatabase = %v, want nil", err)
	}
}
