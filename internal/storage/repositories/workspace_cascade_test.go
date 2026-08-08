// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The real models carry Postgres-specific defaults sqlite can't migrate, so the cascade is
// exercised against minimal stand-in tables sharing the real table names and filter columns.
// Every table the cascade touches must exist, or its DELETE would error.
var wsScoped = []string{
	"applications", "database_instances", "volumes", "domains", "stacks",
	"pipeline_runs", "pipeline_definitions", "images", "port_bindings", "jobs",
	"backup_schedules", "webhooks", "webhook_deliveries", "environments",
	"release_approvals", "template_sources", "template_installs", "routes", "certificates",
	"dns_providers", "registries", "git_repositories", "git_sources", "networks",
	"secrets", "middlewares", "notification_channels", "workspace_keys",
	"workspace_backup_settings", "workspace_members", "workspace_invitations",
}

// child table -> FK column and the parent table whose IDs it references.
var wsChildren = map[string][2]string{
	"app_env_vars":       {"application_id", "applications"},
	"app_ports":          {"application_id", "applications"},
	"deployments":        {"application_id", "applications"},
	"releases":           {"application_id", "applications"},
	"app_events":         {"application_id", "applications"},
	"metric_samples":     {"application_id", "applications"},
	"databases":          {"instance_id", "database_instances"},
	"volume_backups":     {"volume_id", "volumes"},
	"dns_records":        {"domain_id", "domains"},
	"pipeline_step_runs": {"pipeline_run_id", "pipeline_runs"},
	"stack_env_vars":     {"stack_id", "stacks"},
	// Templates have no workspace_id — they hang off a (workspace-owned) source.
	"templates": {"source_id", "template_sources"},
}

func newWorkspaceCascadeDB(t *testing.T) *WorkspaceRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	exec := func(sql string) {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	exec("CREATE TABLE workspaces (id INTEGER PRIMARY KEY)")
	for _, tbl := range wsScoped {
		exec(fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, workspace_id INTEGER)", tbl))
	}
	for tbl, fk := range wsChildren {
		exec(fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, %s INTEGER)", tbl, fk[0]))
	}
	// backups hangs off a logical database (databases.id), not the instance.
	exec("CREATE TABLE backups (id INTEGER PRIMARY KEY, database_id INTEGER)")
	exec("CREATE TABLE application_networks (application_id INTEGER, network_id INTEGER)")
	exec("CREATE TABLE database_instance_networks (database_instance_id INTEGER, network_id INTEGER)")
	return NewWorkspaceRepository(db)
}

// seedWorkspace creates one row in every table for the given workspace, using workspace*1000 as
// the shared id base so ids never collide across workspaces.
func seedWorkspace(t *testing.T, db *gorm.DB, ws uint) {
	t.Helper()
	base := int(ws) * 1000
	ins := func(sql string, args ...any) {
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	ins("INSERT INTO workspaces (id) VALUES (?)", ws)
	for _, tbl := range wsScoped {
		ins(fmt.Sprintf("INSERT INTO %s (id, workspace_id) VALUES (?, ?)", tbl), base, ws)
	}
	// Children point at this workspace's parent rows (all seeded at id=base).
	for tbl, fk := range wsChildren {
		ins(fmt.Sprintf("INSERT INTO %s (id, %s) VALUES (?, ?)", tbl, fk[0]), base, base)
	}
	// A logical database was seeded above with id=base; its backup references it.
	ins("INSERT INTO backups (id, database_id) VALUES (?, ?)", base, base)
	ins("INSERT INTO application_networks (application_id, network_id) VALUES (?, ?)", base, base)
	ins("INSERT INTO database_instance_networks (database_instance_id, network_id) VALUES (?, ?)", base, base)
}

func countRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestWorkspaceDeleteCascade(t *testing.T) {
	repo := newWorkspaceCascadeDB(t)
	seedWorkspace(t, repo.db, 1) // target
	seedWorkspace(t, repo.db, 2) // bystander

	if err := repo.Delete(1); err != nil {
		t.Fatalf("Delete(1): %v", err)
	}

	allTables := append([]string{"workspaces", "backups", "application_networks", "database_instance_networks"}, wsScoped...)
	for tbl := range wsChildren {
		allTables = append(allTables, tbl)
	}
	for _, tbl := range allTables {
		if n := countRows(t, repo.db, tbl); n != 1 {
			// Workspace 1 gone, workspace 2 intact -> exactly one row remains.
			t.Errorf("%s: got %d rows after delete, want 1 (bystander only)", tbl, n)
		}
	}
}
