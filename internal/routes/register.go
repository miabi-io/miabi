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

package routes

import (
	"strings"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
)

// Central so routes added later are guarded automatically; a no-op for session callers, so safe on public routes.
func (r *Router) register(defs ...okapi.RouteDefinition) {
	for i := range defs {
		path := routePath(&defs[i])
		required := scopeFor(strings.ToUpper(defs[i].Method), path)
		defs[i].Middlewares = append(defs[i].Middlewares,
			middlewares.RequireScope(r.scopeMode, required, r.recordScopeViolation), middlewares.SuspensionGuard())
	}
	r.app.Register(defs...)
}

// Mirrors okapi's group-prefix join so the scope is derived from what clients actually call.
func routePath(def *okapi.RouteDefinition) string {
	prefix := ""
	if def.Group != nil {
		prefix = def.Group.Prefix
	}
	p := strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(def.Path, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// Lets warn-mode operators find breaking keys before enforcing; best-effort, auditing must never fail a request.
func (r *Router) recordScopeViolation(v middlewares.ScopeViolation) {
	logger.Warn("api key scope violation",
		"user", v.UserID, "key", v.KeyID, "required", v.Required, "granted", strings.Join(v.Granted, ","),
		"method", v.Method, "path", v.Path, "enforced", v.Enforced)
	if r.audit == nil {
		return
	}
	actor := v.UserID
	e := audit.Entry{
		Action:     "api_key.scope_violation",
		TargetType: "api_key",
		IP:         v.IP,
		Metadata: map[string]any{
			"required": v.Required,
			"granted":  v.Granted,
			"method":   v.Method,
			"path":     v.Path,
			"enforced": v.Enforced,
			"key_id":   v.KeyID,
		},
	}
	if actor != 0 {
		e.ActorID = &actor
	}
	r.audit.Record(e)
}
