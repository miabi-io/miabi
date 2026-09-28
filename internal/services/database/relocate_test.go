// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestStreamInvocation(t *testing.T) {
	db := &models.Database{Name: "shop", Username: "u_ab12"}
	cases := []struct {
		engine        models.DBEngine
		dump, load    string
		loadHas       []string
		passwordInEnv bool
	}{
		{models.DBEnginePostgres, "pg_dump", "pg_restore", []string{"--role=u_ab12", "--single-transaction", "--exit-on-error"}, true},
		{models.DBEngineMySQL, "mysqldump", "mysql", []string{"shop"}, true},
		{models.DBEngineMariaDB, "mariadb-dump", "mariadb", []string{"shop"}, true},
		{models.DBEngineMongoDB, "mongodump", "mongorestore", []string{"--nsInclude", "shop.*", "--archive"}, false},
	}
	for _, c := range cases {
		inst := &models.DatabaseInstance{Engine: c.engine, Host: "mb-db-x-1", Port: 5432, AdminUser: "admin"}
		dump, denv := streamInvocation(inst, db, "s3cret", true)
		load, _ := streamInvocation(inst, db, "s3cret", false)
		if dump[0] != c.dump || load[0] != c.load {
			t.Errorf("%s: got %s | %s", c.engine, dump[0], load[0])
		}
		for _, w := range c.loadHas {
			if !slices.Contains(load, w) {
				t.Errorf("%s restore lacks %q: %v", c.engine, w, load)
			}
		}
		if c.passwordInEnv {
			if strings.Contains(strings.Join(dump, " "), "s3cret") {
				t.Errorf("%s: the password belongs in the environment, not the command line", c.engine)
			}
			if len(denv) == 0 {
				t.Errorf("%s: no password in the environment", c.engine)
			}
		}
	}
}

func TestCanRestoreInto(t *testing.T) {
	pg := func(v string) *models.DatabaseInstance {
		return &models.DatabaseInstance{Engine: models.DBEnginePostgres, Version: v}
	}
	if err := CanRestoreInto(pg("16"), pg("17")); err != nil {
		t.Errorf("newer target: %v", err)
	}
	if err := CanRestoreInto(pg("17"), pg("17")); err != nil {
		t.Errorf("same version: %v", err)
	}
	if err := CanRestoreInto(pg("17"), pg("16")); !errors.Is(err, ErrTargetOlder) {
		t.Errorf("older target: %v", err)
	}
	if err := CanRestoreInto(pg("17"), &models.DatabaseInstance{Engine: models.DBEngineMySQL, Version: "8.4"}); err == nil {
		t.Error("another engine must be refused")
	}
}
