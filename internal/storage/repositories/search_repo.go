// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"

	"gorm.io/gorm"
)

type SearchHit struct {
	Kind        string `json:"kind"`
	ID          uint   `json:"id"`
	UID         string `json:"uid,omitempty"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type SearchKind struct {
	Kind    string
	table   string
	columns []string
	detail  string
	uid     bool
}

var searchKinds = []SearchKind{
	{Kind: "application", table: "applications", columns: []string{"name", "display_name"}, detail: "status", uid: true},
	{Kind: "stack", table: "stacks", columns: []string{"name", "display_name"}, uid: true},
	{Kind: "database", table: "database_instances", columns: []string{"name", "display_name"}, detail: "engine", uid: true},
	{Kind: "volume", table: "volumes", columns: []string{"name", "display_name"}, uid: true},
	{Kind: "network", table: "networks", columns: []string{"name", "display_name"}},
	{Kind: "domain", table: "domains", columns: []string{"name"}},
	{Kind: "route", table: "routes", columns: []string{"name", "display_name", "hosts"}, detail: "hosts", uid: true},
	{Kind: "certificate", table: "certificates", columns: []string{"name", "display_name", "common_name"}, detail: "common_name", uid: true},
	{Kind: "secret", table: "secrets", columns: []string{"name", "display_name", "description"}, detail: "description", uid: true},
	{Kind: "config", table: "configs", columns: []string{"name", "display_name", "description"}, detail: "description", uid: true},
	{Kind: "pipeline", table: "pipeline_definitions", columns: []string{"name", "display_name"}, uid: true},
	{Kind: "gitsource", table: "git_sources", columns: []string{"name", "display_name", "repo_url"}, detail: "repo_url", uid: true},
	{Kind: "environment", table: "environments", columns: []string{"name", "display_name"}, uid: true},
	{Kind: "registry", table: "registries", columns: []string{"name", "display_name", "server"}, detail: "server", uid: true},
	{Kind: "gitrepository", table: "git_repositories", columns: []string{"name", "display_name", "url"}, detail: "url", uid: true},
}

func SearchKinds() []string {
	out := make([]string, 0, len(searchKinds))
	for _, k := range searchKinds {
		out = append(out, k.Kind)
	}
	return out
}

type SearchRepository struct {
	db *gorm.DB
}

func NewSearchRepository(db *gorm.DB) *SearchRepository {
	return &SearchRepository{db: db}
}

func (r *SearchRepository) Search(workspaceID uint, query string, kinds []string, perKind int) ([]SearchHit, error) {
	term := strings.ToLower(strings.TrimSpace(query))
	if term == "" || workspaceID == 0 {
		return nil, nil
	}
	if perKind <= 0 {
		perKind = 5
	}
	like := "%" + term + "%"
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}

	var out []SearchHit
	for _, spec := range searchKinds {
		if len(want) > 0 && !want[spec.Kind] {
			continue
		}
		hits, err := r.searchOne(spec, workspaceID, like, perKind)
		if err != nil {
			return nil, err
		}
		out = append(out, hits...)
	}
	return out, nil
}

func (r *SearchRepository) searchOne(spec SearchKind, workspaceID uint, like string, limit int) ([]SearchHit, error) {
	sel := []string{"id"}
	if spec.uid {
		sel = append(sel, "uid")
	} else {
		sel = append(sel, "'' AS uid")
	}
	sel = append(sel, "name")
	if hasColumn(spec.columns, "display_name") {
		sel = append(sel, "display_name")
	} else {
		sel = append(sel, "'' AS display_name")
	}
	if spec.detail != "" {
		sel = append(sel, spec.detail+" AS detail")
	} else {
		sel = append(sel, "'' AS detail")
	}

	where := make([]string, 0, len(spec.columns))
	args := make([]any, 0, len(spec.columns)+1)
	for _, col := range spec.columns {
		where = append(where, "LOWER("+col+") LIKE ?")
		args = append(args, like)
	}

	q := r.db.Table(spec.table).
		Select(strings.Join(sel, ", ")).
		Where("workspace_id = ?", workspaceID).
		Where(strings.Join(where, " OR "), args...).
		Order("name").
		Limit(limit)

	var hits []SearchHit
	if err := q.Scan(&hits).Error; err != nil {
		if isMissingRelation(err) {
			return nil, nil
		}
		return nil, err
	}
	for i := range hits {
		hits[i].Kind = spec.Kind
	}
	return hits, nil
}

func hasColumn(cols []string, name string) bool {
	for _, c := range cols {
		if c == name {
			return true
		}
	}
	return false
}

func isMissingRelation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "no such column") || strings.Contains(msg, "undefined column")
}
