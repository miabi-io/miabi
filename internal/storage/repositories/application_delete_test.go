// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Deleting an app removes its CronJobs and Job runs, so no schedule outlives
// the app it would run in.
func TestApplicationDeleteRemovesJobs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE applications (id INTEGER PRIMARY KEY, workspace_id INTEGER)")
	db.Exec("CREATE TABLE application_networks (application_id INTEGER, network_id INTEGER)")
	for _, tbl := range []string{"app_env_vars", "app_ports", "deployments", "releases", "app_events", "metric_samples", "jobs", "cron_jobs", "job_definitions"} {
		if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, application_id INTEGER)", tbl)).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO applications (id, workspace_id) VALUES (1, 1), (2, 1)")
	db.Exec("INSERT INTO cron_jobs (application_id) VALUES (1), (2)")
	db.Exec("INSERT INTO jobs (application_id) VALUES (1), (1), (2)")

	if err := NewApplicationRepository(db).Delete(1); err != nil {
		t.Fatal(err)
	}
	for tbl, want := range map[string]int64{"cron_jobs": 1, "jobs": 1} {
		var n int64
		db.Table(tbl).Count(&n)
		if n != want {
			t.Errorf("%s: %d rows left, want %d (the other app's)", tbl, n, want)
		}
	}
}
