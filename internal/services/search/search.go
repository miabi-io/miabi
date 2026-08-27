// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const (
	DefaultLimit = 20
	MaxLimit     = 50
	perKind      = 6
	MinQueryLen  = 2
)

type Result struct {
	Kind        string `json:"kind"`
	ID          uint   `json:"id"`
	UID         string `json:"uid,omitempty"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Score       int    `json:"-"`
}

type Response struct {
	Query   string   `json:"query"`
	Kind    string   `json:"kind,omitempty"`
	Results []Result `json:"results"`
	Kinds   []string `json:"kinds"`
}

var kindAliases = map[string]string{
	"app":             "application",
	"apps":            "application",
	"application":     "application",
	"applications":    "application",
	"stack":           "stack",
	"stacks":          "stack",
	"db":              "database",
	"database":        "database",
	"databases":       "database",
	"vol":             "volume",
	"volume":          "volume",
	"volumes":         "volume",
	"net":             "network",
	"network":         "network",
	"networks":        "network",
	"domain":          "domain",
	"domains":         "domain",
	"route":           "route",
	"routes":          "route",
	"cert":            "certificate",
	"certificate":     "certificate",
	"certificates":    "certificate",
	"secret":          "secret",
	"secrets":         "secret",
	"config":          "config",
	"configs":         "config",
	"pipeline":        "pipeline",
	"pipelines":       "pipeline",
	"gitops":          "gitsource",
	"gitsource":       "gitsource",
	"env":             "environment",
	"environment":     "environment",
	"environments":    "environment",
	"registry":        "registry",
	"registries":      "registry",
	"repo":            "gitrepository",
	"gitrepository":   "gitrepository",
	"gitrepositories": "gitrepository",
}

var kindRank = map[string]int{
	"application":   0,
	"stack":         1,
	"database":      2,
	"route":         3,
	"domain":        4,
	"volume":        5,
	"pipeline":      6,
	"gitsource":     7,
	"secret":        8,
	"config":        9,
	"network":       10,
	"certificate":   11,
	"environment":   12,
	"registry":      13,
	"gitrepository": 14,
}

type Service struct {
	repo *repositories.SearchRepository
}

func NewService(repo *repositories.SearchRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Search(workspaceID uint, raw string, limit int) (*Response, error) {
	kind, query := splitKind(raw)
	resp := &Response{Query: query, Kind: kind, Kinds: repositories.SearchKinds(), Results: []Result{}}
	if len(query) < MinQueryLen || workspaceID == 0 {
		return resp, nil
	}
	if limit <= 0 || limit > MaxLimit {
		limit = DefaultLimit
	}

	var kinds []string
	if kind != "" {
		kinds = []string{kind}
	}
	hits, err := s.repo.Search(workspaceID, query, kinds, perKind)
	if err != nil {
		return nil, err
	}

	term := strings.ToLower(query)
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		out = append(out, Result{
			Kind:        h.Kind,
			ID:          h.ID,
			UID:         h.UID,
			Name:        h.Name,
			DisplayName: h.DisplayName,
			Detail:      cleanDetail(h.Kind, h.Detail),
			Score:       score(term, h),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if kindRank[out[i].Kind] != kindRank[out[j].Kind] {
			return kindRank[out[i].Kind] < kindRank[out[j].Kind]
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	resp.Results = out
	return resp, nil
}

func splitKind(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	idx := strings.Index(raw, ":")
	if idx <= 0 {
		return "", raw
	}
	kind, ok := kindAliases[strings.ToLower(raw[:idx])]
	if !ok {
		return "", raw
	}
	return kind, strings.TrimSpace(raw[idx+1:])
}

func score(term string, h repositories.SearchHit) int {
	best := 0
	for _, field := range []string{h.Name, h.DisplayName} {
		v := strings.ToLower(field)
		switch {
		case v == "":
		case v == term:
			best = max(best, 100)
		case strings.HasPrefix(v, term):
			best = max(best, 70)
		case strings.Contains(v, term):
			best = max(best, 40)
		}
	}
	if best == 0 {
		best = 10
	}
	return best
}

func cleanDetail(kind, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	if kind == "route" && strings.HasPrefix(detail, "[") {
		var hosts []string
		if err := json.Unmarshal([]byte(detail), &hosts); err == nil {
			return strings.Join(hosts, ", ")
		}
	}
	if len(detail) > 120 {
		return detail[:120] + "…"
	}
	return detail
}
