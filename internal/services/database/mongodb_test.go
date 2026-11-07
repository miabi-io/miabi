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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// With no key configured, crypto.Encrypt/Decrypt round-trip via base64.
func mustEncrypt(t *testing.T, v string) string {
	t.Helper()
	enc, err := crypto.Encrypt(v)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestMongoEngineSpec(t *testing.T) {
	spec, ok := specs[models.DBEngineMongoDB]
	if !ok {
		t.Fatal("mongodb missing from specs")
	}
	if spec.port != 27017 || spec.dataDir != "/data/db" || spec.adminUser != "admin" {
		t.Errorf("unexpected spec: port=%d dir=%s admin=%s", spec.port, spec.dataDir, spec.adminUser)
	}
	if got := spec.image("7.0"); got != "mongo:7.0" {
		t.Errorf("image = %q, want mongo:7.0", got)
	}
	env := spec.adminEnv(spec.adminUser, "secret")
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "MONGO_INITDB_ROOT_USERNAME=admin") || !strings.Contains(joined, "MONGO_INITDB_ROOT_PASSWORD=secret") {
		t.Errorf("adminEnv = %v", env)
	}
}

func TestMongoCreateDropDDL(t *testing.T) {
	create := strings.Join(createDDL(models.DBEngineMongoDB, "shop", "shop_user", "pw"), "\n")
	for _, want := range []string{`getSiblingDB("shop")`, "createUser", `"shop_user"`, "dbOwner", "_miabi_init"} {
		if !strings.Contains(create, want) {
			t.Errorf("createDDL missing %q:\n%s", want, create)
		}
	}
	drop := strings.Join(dropDDL(models.DBEngineMongoDB, "shop", "shop_user"), "\n")
	if !strings.Contains(drop, "dropUser") || !strings.Contains(drop, "dropDatabase") {
		t.Errorf("dropDDL = %s", drop)
	}
}

func TestMongoInvocationUsesMongosh(t *testing.T) {
	inst := &models.DatabaseInstance{Engine: models.DBEngineMongoDB, Host: "mb-db-1", Port: 27017, AdminUser: "admin"}
	cmd, env := queryInvocation(inst, "db.adminCommand({ping:1})", "secret")
	if len(env) != 0 {
		t.Errorf("mongosh should pass no env, got %v", env)
	}
	joined := strings.Join(cmd, " ")
	for _, want := range []string{"mongosh", "--username admin", "--password secret", "--authenticationDatabase admin", "--eval db.adminCommand({ping:1})"} {
		if !strings.Contains(joined, want) {
			t.Errorf("query cmd missing %q: %v", want, cmd)
		}
	}
	// clientInvocation joins multiple statements into one --eval script.
	cmd2, _ := clientInvocation(inst, []string{"a()", "b()"}, "secret")
	if j := strings.Join(cmd2, "\x00"); !strings.Contains(j, "a()\nb()") {
		t.Errorf("clientInvocation did not join statements: %v", cmd2)
	}
}

func TestReadinessProbe(t *testing.T) {
	if got := readinessProbe(models.DBEngineMongoDB); got != "db.adminCommand({ping:1})" {
		t.Errorf("mongo probe = %q", got)
	}
	if got := readinessProbe(models.DBEnginePostgres); got != "SELECT 1" {
		t.Errorf("sql probe = %q", got)
	}
}

func TestMongoConnectionURIs(t *testing.T) {
	// Logical database URI: authSource is the database itself.
	d := &models.Database{Name: "shop", Username: "shop_user", PasswordEnc: mustEncrypt(t, "pw")}
	inst := &models.DatabaseInstance{Engine: models.DBEngineMongoDB, Host: "mb-db-1", Port: 27017}
	svc := &Service{}
	conn, err := svc.DatabaseConnection(inst, d)
	if err != nil {
		t.Fatal(err)
	}
	if conn.URI != "mongodb://shop_user:pw@mb-db-1:27017/shop?authSource=shop" {
		t.Errorf("db URI = %q", conn.URI)
	}
	// Admin/instance URI: authSource=admin.
	inst.AdminUser = "admin"
	inst.AdminPasswordEnc = mustEncrypt(t, "adminpw")
	ic, err := svc.InstanceConnection(inst)
	if err != nil {
		t.Fatal(err)
	}
	if ic.URI != "mongodb://admin:adminpw@mb-db-1:27017/?authSource=admin" {
		t.Errorf("admin URI = %q", ic.URI)
	}
}
