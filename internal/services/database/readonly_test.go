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
)

func TestReadOnlyDDL(t *testing.T) {
	server := strings.Join(readOnlyServerDDL("shop", "r_ab12", "secret"), "\n")
	for _, want := range []string{
		`CREATE ROLE "r_ab12" LOGIN PASSWORD 'secret'`,
		// A recreate or restore re-applies it: an existing role is re-passworded, not an error.
		`EXCEPTION WHEN duplicate_object THEN ALTER ROLE "r_ab12" LOGIN PASSWORD 'secret'`,
		`GRANT CONNECT ON DATABASE "shop" TO "r_ab12"`,
	} {
		if !strings.Contains(server, want) {
			t.Errorf("server DDL lacks %q:\n%s", want, server)
		}
	}
	grants := strings.Join(readOnlyGrantDDL("u_owner", "r_ab12"), "\n")
	for _, want := range []string{
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO "r_ab12"`,
		// Tables the owner creates later are readable too.
		`ALTER DEFAULT PRIVILEGES FOR ROLE "u_owner" IN SCHEMA public GRANT SELECT ON TABLES TO "r_ab12"`,
	} {
		if !strings.Contains(grants, want) {
			t.Errorf("grant DDL lacks %q:\n%s", want, grants)
		}
	}
	if strings.Contains(grants, "INSERT") || strings.Contains(grants, "UPDATE") || strings.Contains(grants, "ALL PRIVILEGES") {
		t.Errorf("a read-only user must not be granted writes:\n%s", grants)
	}
	if got := strings.Join(readOnlyDropDDL("shop", "r_ab12"), "\n"); !strings.Contains(got, `DROP ROLE IF EXISTS "r_ab12"`) {
		t.Errorf("drop DDL: %s", got)
	}
}

func TestClientInvocationInTargetsTheDatabase(t *testing.T) {
	inst := &models.DatabaseInstance{Engine: models.DBEnginePostgres, Host: "mb-db-x", Port: 5432, AdminUser: "postgres"}
	cmd, _ := clientInvocationIn(inst, "shop", []string{"SELECT 1"}, "pw")
	if !strings.Contains(strings.Join(cmd, " "), "-d shop") {
		t.Errorf("grants must run inside the target database: %v", cmd)
	}
	cmd, _ = clientInvocation(inst, []string{"SELECT 1"}, "pw")
	if !strings.Contains(strings.Join(cmd, " "), "-d postgres") {
		t.Errorf("server DDL runs on the admin database: %v", cmd)
	}
}
