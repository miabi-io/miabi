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
	"fmt"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

func sanitizeIdent(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if b.Len() > 0 { // identifiers may not start with a digit
				b.WriteRune(r)
			}
		case r == ' ' || r == '-':
			if b.Len() > 0 {
				b.WriteByte('_')
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// Names and credentials are platform-generated identifiers and hex tokens, so they are safe to embed.
func createDDL(engine models.DBEngine, name, user, pass string) []string {
	switch engine {
	case models.DBEnginePostgres:
		return []string{
			fmt.Sprintf(`CREATE USER "%s" WITH PASSWORD '%s'`, user, pass),
			fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, name, user),
		}
	case models.DBEngineMongoDB:
		// Mongo drops databases with no collections, so a marker collection keeps the new one durable.
		return []string{
			fmt.Sprintf(`db.getSiblingDB(%q).createUser({user:%q,pwd:%q,roles:[{role:"dbOwner",db:%q}]})`, name, user, pass, name),
			fmt.Sprintf(`db.getSiblingDB(%q).createCollection("_miabi_init")`, name),
		}
	default: // mysql / mariadb
		return []string{
			fmt.Sprintf("CREATE DATABASE `%s`", name),
			fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", user, pass),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'", name, user),
			"FLUSH PRIVILEGES",
		}
	}
}

func dropDDL(engine models.DBEngine, name, user string) []string {
	switch engine {
	case models.DBEnginePostgres:
		return []string{
			fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, name),
			fmt.Sprintf(`DROP USER IF EXISTS "%s"`, user),
		}
	case models.DBEngineMongoDB:
		// A missing user or db throws, but the surrounding teardown job tolerates it.
		return []string{
			fmt.Sprintf(`db.getSiblingDB(%q).dropUser(%q)`, name, user),
			fmt.Sprintf(`db.getSiblingDB(%q).dropDatabase()`, name),
		}
	default: // mysql / mariadb
		return []string{
			fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name),
			fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user),
		}
	}
}

// Idempotent, so a recreate, restore or retry simply re-applies it.
func readOnlyServerDDL(name, user, pass string) []string {
	return []string{
		fmt.Sprintf(`DO $$ BEGIN CREATE ROLE "%s" LOGIN PASSWORD '%s'; `+
			`EXCEPTION WHEN duplicate_object THEN ALTER ROLE "%s" LOGIN PASSWORD '%s'; END $$`, user, pass, user, pass),
		fmt.Sprintf(`GRANT CONNECT ON DATABASE "%s" TO "%s"`, name, user),
	}
}

// Grants are recorded as the owner's, so the owner can still revoke them.
func readOnlyGrantDDL(owner, user string) []string {
	return []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO "%s"`, user),
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO "%s"`, user),
		fmt.Sprintf(`GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO "%s"`, user),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE "%s" IN SCHEMA public GRANT SELECT ON TABLES TO "%s"`, owner, user),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE "%s" IN SCHEMA public GRANT SELECT ON SEQUENCES TO "%s"`, owner, user),
	}
}

func readOnlyRevokeDDL(user string) []string {
	return []string{fmt.Sprintf(`DROP OWNED BY "%s"`, user)}
}

func readOnlyDropDDL(name, user string) []string {
	return []string{
		fmt.Sprintf(`REVOKE CONNECT ON DATABASE "%s" FROM "%s"`, name, user),
		fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, user),
	}
}

func queryInvocation(inst *models.DatabaseInstance, query, adminPass string) (cmd []string, env []string) {
	port := strconv.Itoa(inst.Port)
	switch inst.Engine {
	case models.DBEnginePostgres:
		cmd = []string{"psql", "-h", inst.Host, "-p", port, "-U", inst.AdminUser, "-d", inst.AdminUser, "-tAc", query}
		env = []string{"PGPASSWORD=" + adminPass}
	case models.DBEngineRedis:
		cmd = append([]string{"redis-cli", "-h", inst.Host, "-p", port, "-a", adminPass, "--no-auth-warning"}, strings.Fields(query)...)
	case models.DBEngineMongoDB:
		cmd = mongoshCmd(inst, port, adminPass, query)
	default: // mysql / mariadb
		client := "mysql"
		if inst.Engine == models.DBEngineMariaDB {
			client = "mariadb"
		}
		cmd = []string{client, "-h", inst.Host, "-P", port, "-u", inst.AdminUser, "-N", "-B", "-e", query}
		env = []string{"MYSQL_PWD=" + adminPass}
	}
	return cmd, env
}

func clientInvocation(inst *models.DatabaseInstance, statements []string, adminPass string) (cmd []string, env []string) {
	return clientInvocationIn(inst, "", statements, adminPass)
}

func clientInvocationIn(inst *models.DatabaseInstance, dbName string, statements []string, adminPass string) (cmd []string, env []string) {
	port := strconv.Itoa(inst.Port)
	switch inst.Engine {
	case models.DBEnginePostgres:
		target := inst.AdminUser
		if dbName != "" {
			target = dbName
		}
		cmd = []string{"psql", "-h", inst.Host, "-p", port, "-U", inst.AdminUser, "-d", target, "-v", "ON_ERROR_STOP=1"}
		for _, st := range statements {
			cmd = append(cmd, "-c", st)
		}
		env = []string{"PGPASSWORD=" + adminPass}
	case models.DBEngineMongoDB:
		// mongosh runs a single script: join the JS statements with newlines.
		cmd = mongoshCmd(inst, port, adminPass, strings.Join(statements, "\n"))
	default: // mysql / mariadb
		client := "mysql"
		if inst.Engine == models.DBEngineMariaDB {
			client = "mariadb"
		}
		cmd = []string{client, "-h", inst.Host, "-P", port, "-u", inst.AdminUser, "-e", strings.Join(statements, "; ")}
		env = []string{"MYSQL_PWD=" + adminPass}
	}
	return cmd, env
}

// mongosh has no password env equivalent, so the platform-generated token is passed as a flag.
func mongoshCmd(inst *models.DatabaseInstance, port, adminPass, script string) []string {
	return []string{
		"mongosh", "--quiet",
		"--host", inst.Host, "--port", port,
		"--username", inst.AdminUser, "--password", adminPass,
		"--authenticationDatabase", "admin",
		"--eval", script,
	}
}

func readinessProbe(engine models.DBEngine) string {
	if engine == models.DBEngineMongoDB {
		return "db.adminCommand({ping:1})"
	}
	return "SELECT 1"
}
