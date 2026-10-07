// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package locationmigration

import (
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestStrategies(t *testing.T) {
	cases := []struct {
		name                        string
		engine                      models.DBEngine
		exclusive, sameArch, hasDBs bool
		want                        []string
	}{
		{"exclusive postgres", models.DBEnginePostgres, true, true, true,
			[]string{models.DBStrategyMove, models.DBStrategyNewInstance, models.DBStrategyExistingInstance}},
		{"shared postgres cannot move", models.DBEnginePostgres, false, true, true,
			[]string{models.DBStrategyNewInstance, models.DBStrategyExistingInstance}},
		{"other architecture cannot move", models.DBEngineMySQL, true, false, true,
			[]string{models.DBStrategyNewInstance, models.DBStrategyExistingInstance}},
		{"exclusive redis moves", models.DBEngineRedis, true, true, false, []string{models.DBStrategyMove}},
		{"shared redis has no way", models.DBEngineRedis, false, true, false, nil},
		{"libsql on another architecture has no way", models.DBEngineLibSQL, true, false, true, nil},
		{"instance used only by env cannot be restored", models.DBEnginePostgres, false, true, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strategies(c.engine, c.exclusive, c.sameArch, c.hasDBs); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLinkedBy(t *testing.T) {
	cases := []struct {
		name                string
		apps                []uint
		wantMine, wantOther bool
	}{
		{"unlinked", nil, false, false},
		{"only this app", []uint{1}, true, false},
		{"only another app", []uint{2}, false, true},
		{"shared", []uint{2, 1}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var links []models.DatabaseInstanceLink
			for _, id := range c.apps {
				links = append(links, models.DatabaseInstanceLink{InstanceID: 9, ApplicationID: id})
			}
			mine, others := linkedBy(links, 1)
			if mine != c.wantMine || others != c.wantOther {
				t.Errorf("got (%v, %v), want (%v, %v)", mine, others, c.wantMine, c.wantOther)
			}
		})
	}
}

func TestEnvMentions(t *testing.T) {
	env := []models.AppEnvVar{
		{Key: "DB_HOST", Value: "mb-db-abcd1234-7"},
		{Key: "SECRET", Value: "mb-db-ffff0000-9", IsSecret: true},
	}
	if !envMentions(env, "mb-db-abcd1234-7") {
		t.Error("a plain value naming the host is a reference")
	}
	if envMentions(env, "mb-db-ffff0000-9") {
		t.Error("secret values are ciphertext and must not be read")
	}
	if envMentions(env, "") {
		t.Error("an empty host matches nothing")
	}
}

func TestEstimate(t *testing.T) {
	if got := estimate(100<<20, 10<<20); got != 10+deploySeconds {
		t.Errorf("estimate = %d", got)
	}
	if got := estimate(1<<30, 0); got != deploySeconds {
		t.Errorf("unknown throughput estimate = %d", got)
	}
}

func TestTerminalStatuses(t *testing.T) {
	for _, s := range []models.MigrationStatus{models.MigrationRunning, models.MigrationAwaitingCutover, models.MigrationCutOver} {
		if s.Terminal() {
			t.Errorf("%s holds a source copy and must stay open", s)
		}
	}
	for _, s := range []models.MigrationStatus{models.MigrationFinalized, models.MigrationRolledBack, models.MigrationFailed, models.MigrationCancelled} {
		if !s.Terminal() {
			t.Errorf("%s is terminal", s)
		}
	}
}
