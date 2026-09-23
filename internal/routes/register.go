// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"strings"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
)

// register attaches the API-key scope guard to every route definition and then registers them.
//
// Central rather than per route: the scope a route needs is derived from its method and path (see
// scopeFor), so a route added tomorrow is guarded without anyone remembering to guard it — the
// failure mode that left RequireScope with no callers at all. The guard is appended last, after the
// route's own auth and role middlewares, and is a no-op for session callers, so it is safe on
// public routes too.
func (r *Router) register(defs ...okapi.RouteDefinition) {
	for i := range defs {
		path := routePath(&defs[i])
		required := scopeFor(strings.ToUpper(defs[i].Method), path)
		defs[i].Middlewares = append(defs[i].Middlewares,
			middlewares.RequireScope(r.scopeMode, required, r.recordScopeViolation))
	}
	r.app.Register(defs...)
}

// routePath is the full template path a definition registers at, mirroring okapi's own
// group-prefix join so the scope is derived from what clients actually call.
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

// recordScopeViolation writes the violation to the audit log so an operator running in warn mode
// can find every key that would break before switching to enforce. Best-effort by design: auditing
// must never be what fails a request.
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
