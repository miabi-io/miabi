// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// sanitizeIdent reduces a user-supplied name to a safe SQL identifier
// (lowercase letters, digits, underscores; must start with a letter or
// underscore). Returns "" if nothing usable remains.
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

// createDDL returns the statements that create a logical database and its
// scoped user. Names/credentials are platform-generated identifiers and hex
// tokens, so they are safe to embed directly.
func createDDL(engine models.DBEngine, name, user, pass string) []string {
	switch engine {
	case models.DBEnginePostgres:
		return []string{
			fmt.Sprintf(`CREATE USER "%s" WITH PASSWORD '%s'`, user, pass),
			fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, name, user),
		}
	case models.DBEngineMongoDB:
		// MongoDB has no DDL: a scoped user is created on the target database (so authSource = the database), and
		// a marker collection makes the otherwise-empty database durable (Mongo drops databases with no
		// collections). Statements are mongosh JS, joined and run via one --eval.
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
		// dropUser before dropDatabase; both are idempotent enough for teardown
		// (a missing user/db throws, but the surrounding job tolerates it).
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

// queryInvocation builds the engine client command to run a single query and
// emit raw, parseable output (tuples-only / batch / redis INFO).
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

// clientInvocation builds the engine client command and environment to run the
// given statements as admin against the instance.
func clientInvocation(inst *models.DatabaseInstance, statements []string, adminPass string) (cmd []string, env []string) {
	port := strconv.Itoa(inst.Port)
	switch inst.Engine {
	case models.DBEnginePostgres:
		cmd = []string{"psql", "-h", inst.Host, "-p", port, "-U", inst.AdminUser, "-d", inst.AdminUser, "-v", "ON_ERROR_STOP=1"}
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

// mongoshCmd builds a `mongosh --eval` invocation authenticating as the instance
// admin against the `admin` database. The password is passed as a flag (mongosh
// has no PGPASSWORD/MYSQL_PWD env equivalent); it is a platform-generated token.
func mongoshCmd(inst *models.DatabaseInstance, port, adminPass, script string) []string {
	return []string{
		"mongosh", "--quiet",
		"--host", inst.Host, "--port", port,
		"--username", inst.AdminUser, "--password", adminPass,
		"--authenticationDatabase", "admin",
		"--eval", script,
	}
}

// readinessProbe returns a trivial admin query used to confirm the engine accepts
// connections (it returns success, not data). MongoDB is not SQL, so it pings via
// mongosh; every SQL engine uses SELECT 1.
func readinessProbe(engine models.DBEngine) string {
	if engine == models.DBEngineMongoDB {
		return "db.adminCommand({ping:1})"
	}
	return "SELECT 1"
}
