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

package models

import (
	"encoding/json"
	"strings"
)

// PlanConsoleSettings are the non-limit plan fields a billing console may set. Everything else on a
// plan — placement, storage classes, platform runners — names things of one instance and stays local.
var PlanConsoleSettings = []string{
	"description", "is_active", "security_profile", "default_cpu_millicores", "default_memory_mb",
	"require_resource_limits", "database_sizes",
}

// ConsoleManaged reports whether a billing console owns this plan.
func (p *Plan) ConsoleManaged() bool { return p != nil && p.ManagedBy == ManagedByConsole }

// IsPlanConsoleField reports whether a plan's JSON field belongs to the console on a managed plan.
func IsPlanConsoleField(key string) bool {
	if key == "name" || strings.HasPrefix(key, "max_") || strings.HasPrefix(key, "allow_") {
		return true
	}
	for _, k := range PlanConsoleSettings {
		if k == key {
			return true
		}
	}
	return false
}

// KeepConsoleFields copies the console-owned fields of from onto p, so a local edit of a managed plan
// changes only its instance-local fields.
func (p *Plan) KeepConsoleFields(from *Plan) error {
	src, err := planFields(from)
	if err != nil {
		return err
	}
	keep := map[string]any{}
	for k, v := range src {
		if IsPlanConsoleField(k) {
			keep[k] = v
		}
	}
	raw, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, p)
}

func planFields(p *Plan) (map[string]any, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}
