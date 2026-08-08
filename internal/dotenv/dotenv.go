// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dotenv parses .env-style KEY=VALUE text into ordered pairs, for bulk
// importing application and stack environment variables.
package dotenv

import "strings"

// KeyValue is one parsed environment entry.
type KeyValue struct {
	Key   string
	Value string
}

// Parse reads .env-style content and returns the variables in file order. It skips blank lines
// and # comments, tolerates an optional `export ` prefix, and strips matching quotes. Duplicate
// keys are returned in order for the caller to resolve; lines without an `=` are ignored.
func Parse(content string) []KeyValue {
	var out []KeyValue
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, KeyValue{Key: key, Value: unquote(strings.TrimSpace(value))})
	}
	return out
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
