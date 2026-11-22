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

package route

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/canaryroute"
	"github.com/miabi-io/miabi/internal/models"
)

// TraceRequest is one imagined request; nothing is sent, stored or logged.
type TraceRequest struct {
	Scheme  string            `json:"scheme,omitempty"`
	Method  string            `json:"method,omitempty"`
	Host    string            `json:"host,omitempty"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
	Cookies map[string]string `json:"cookies,omitempty"`
	IP      string            `json:"ip,omitempty"`
}

// TraceStep carries a stable Code and Params for clients that word it themselves, and Detail in English.
type TraceStep struct {
	Stage   string            `json:"stage"`
	Code    string            `json:"code"`
	Outcome string            `json:"outcome"`
	Index   *int              `json:"index,omitempty"`
	Status  int               `json:"status,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Detail  string            `json:"detail"`
}

type TraceResult struct {
	// Kind: app, redirect, early_answer, maintenance, other_route, no_route, not_served, no_replica.
	Kind         string `json:"kind"`
	Status       int    `json:"status,omitempty"`
	Backend      string `json:"backend,omitempty"`
	CanaryChance int    `json:"canary_chance,omitempty"`
	UpstreamPath string `json:"upstream_path,omitempty"`
	Location     string `json:"location,omitempty"`
	RouteID      uint   `json:"route_id,omitempty"`
	RouteName    string `json:"route_name,omitempty"`
	Detail       string `json:"detail"`
}

type Trace struct {
	Method string               `json:"method"`
	URL    string               `json:"url"`
	Steps  []TraceStep          `json:"steps"`
	Result TraceResult          `json:"result"`
	Canary *canaryroute.Preview `json:"canary,omitempty"`
}

const (
	TraceStageVisitors   = "visitors"
	TraceStageGateway    = "gateway"
	TraceStageMiddleware = "middleware"
	TraceStageSplit      = "split"
	TraceStageBackend    = "backend"

	TracePass = "pass"
	TraceStop = "stop"
	TraceSkip = "skip"
	TraceNote = "note"
	TraceFork = "fork"
)

func (s *Service) Trace(ctx context.Context, workspaceID, id uint, req TraceRequest) (*Trace, error) {
	f, err := s.Flow(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	rt, err := s.routes.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	app, err := s.apps.FindByID(rt.ApplicationID)
	if err != nil {
		return nil, err
	}
	mws := map[string]*models.Middleware{}
	if len(rt.Middlewares) > 0 {
		list, err := s.middlewares.ListByWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		for i := range list {
			mws[list[i].Name] = &list[i]
		}
	}
	return buildTrace(f, app, mws, req), nil
}

type tracer struct {
	t *Trace
}

func (tr *tracer) step(stage, code, outcome, detail string, params map[string]string) *TraceStep {
	tr.t.Steps = append(tr.t.Steps, TraceStep{Stage: stage, Code: code, Outcome: outcome, Params: params, Detail: detail})
	return &tr.t.Steps[len(tr.t.Steps)-1]
}

func (tr *tracer) stop(kind string, status int, detail string) *Trace {
	tr.t.Result.Kind, tr.t.Result.Status, tr.t.Result.Detail = kind, status, detail
	return tr.t
}

// buildTrace follows Goma's order: route selection, maintenance, the chain, the canary, the backend.
func buildTrace(f *Flow, app *models.Application, mws map[string]*models.Middleware, req TraceRequest) *Trace {
	scheme := strings.ToLower(strings.TrimSpace(req.Scheme))
	if scheme != "http" {
		scheme = "https"
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if host == "" && len(f.Hosts) > 0 {
		host = f.Hosts[0].Host
	}
	rawPath := strings.TrimSpace(req.Path)
	query := map[string]string{}
	for k, v := range req.Query {
		query[k] = v
	}
	if i := strings.IndexByte(rawPath, '?'); i >= 0 {
		if q, err := url.ParseQuery(rawPath[i+1:]); err == nil {
			for k, v := range q {
				if _, set := query[k]; !set && len(v) > 0 {
					query[k] = v[0]
				}
			}
		}
		rawPath = rawPath[:i]
	}
	reqPath := cleanURLPath(rawPath)
	cookies := map[string]string{}
	if c := canaryroute.Header(req.Headers, "Cookie"); c != "" {
		for _, part := range strings.Split(c, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
				cookies[k] = v
			}
		}
	}
	for k, v := range req.Cookies {
		cookies[k] = v
	}

	tr := &tracer{t: &Trace{Method: method, URL: scheme + "://" + host + reqPath, Steps: []TraceStep{}}}
	tr.step(TraceStageVisitors, "request", TracePass, method+" "+tr.t.URL, map[string]string{"method": method, "url": tr.t.URL})

	if !f.Route.Serving {
		tr.step(TraceStageGateway, "not_served", TraceStop, "The gateway does not serve this route: "+f.Route.ServingReason+".", map[string]string{"reason": f.Route.ServingReason})
		return tr.stop("not_served", 404, "The gateway answers 404: no route it serves matches.")
	}
	if host != "" && !hostOnRoute(f, host) {
		tr.step(TraceStageGateway, "host_not_on_route", TraceStop, host+" is not one of this route's hosts.", map[string]string{"host": host})
		return tr.stop("no_route", 0, "Another route, or a 404 from the gateway.")
	}
	if other := winningSibling(f, host, reqPath); other != nil {
		tr.step(TraceStageGateway, "other_route", TraceStop,
			fmt.Sprintf("Route %s on %s has the longer matching path %s, so it handles this request.", other.Name, host, other.Path),
			map[string]string{"route": other.Name, "path": other.Path})
		tr.t.Result.RouteID, tr.t.Result.RouteName = other.RouteID, other.Name
		return tr.stop("other_route", 0, "Handled by route "+other.Name+".")
	}
	if !underPath(reqPath, f.Route.Path) {
		tr.step(TraceStageGateway, "path_no_match", TraceStop, reqPath+" is not under this route's path "+f.Route.Path+".", map[string]string{"path": reqPath, "route_path": f.Route.Path})
		return tr.stop("no_route", 0, "Another route on the host, or a 404 from the gateway.")
	}
	if len(f.Route.Methods) > 0 && !containsFold(f.Route.Methods, method) {
		tr.step(TraceStageGateway, "method_not_allowed", TraceStop, "This route only accepts "+strings.Join(f.Route.Methods, ", ")+".", map[string]string{"method": method, "methods": strings.Join(f.Route.Methods, ", ")})
		return tr.stop("no_route", 0, "This route does not accept "+method+".")
	}
	upstream := rewritePath(reqPath, f.Route.Path, f.Route.Rewrite)
	matched := tr.step(TraceStageGateway, "matched", TracePass, "Host and path "+f.Route.Path+" matched.", map[string]string{"route_path": f.Route.Path, "upstream_path": upstream})
	if upstream != reqPath {
		matched.Detail += " The app receives " + upstream + "."
	}
	if scheme == "https" && f.TLS.Mode == "none" {
		tr.step(TraceStageGateway, "tls_off", TraceNote, "This route has TLS off, so HTTPS gets no certificate of its own.", nil)
	}
	if f.Route.Suspended {
		tr.step(TraceStageGateway, "suspended", TraceStop, "The workspace is suspended: the gateway answers with a notice.", nil)
		return tr.stop("maintenance", 503, "503 with the suspension notice.")
	}
	if f.Route.Maintenance.Enabled {
		code := f.Route.Maintenance.StatusCode
		page := "message"
		if f.Route.Maintenance.HasHTML && strings.Contains(strings.ToLower(canaryroute.Header(req.Headers, "Accept")), "text/html") {
			page = "page"
		}
		tr.step(TraceStageGateway, "maintenance", TraceStop, "Maintenance is on: the gateway answers itself.", map[string]string{"status": strconv.Itoa(code), "body": page})
		return tr.stop("maintenance", code, fmt.Sprintf("%d with the maintenance %s.", code, page))
	}

	for i, m := range f.Chain {
		idx := i
		if res := traceMiddleware(tr, idx, m, mws[m.Name], scheme, host, reqPath, f.Route.Path, req.Headers, cookies, req.IP); res != nil {
			return res
		}
	}

	stable, canary := backendByRole(f, "stable"), backendByRole(f, "canary")
	if stable == nil {
		tr.step(TraceStageBackend, "not_deployed", TraceStop, "The app has no deployed release.", nil)
		return tr.stop("no_replica", 502, "502 from the gateway.")
	}
	target := stable
	tr.t.Result.Backend = "stable"
	if canary != nil {
		p := canaryroute.Evaluate(app.CanaryMatch, app.CanaryExclusive, canary.Weight, canaryroute.Request{Headers: req.Headers, Query: query, Cookies: cookies, IP: req.IP})
		tr.t.Canary = &p
		outcome := TracePass
		if p.Backend == canaryroute.Split {
			outcome = TraceFork
		}
		tr.step(TraceStageSplit, "canary_"+p.Backend, outcome, p.Reason, map[string]string{"backend": p.Backend, "chance": strconv.Itoa(p.CanaryChance)})
		tr.t.Result.Backend, tr.t.Result.CanaryChance = p.Backend, p.CanaryChance
		if p.Backend == canaryroute.Canary {
			target = canary
		}
	}
	for _, b := range []*FlowBackend{target, canaryIfSplit(tr.t.Result.Backend, canary)} {
		if b == nil {
			continue
		}
		if b.Health != nil && b.Health.Desired > 0 && b.Health.Running == 0 {
			tr.step(TraceStageBackend, "no_replica", TraceStop, "No "+b.Role+" replica is running.", map[string]string{"role": b.Role})
			return tr.stop("no_replica", 502, "502 from the gateway: nothing is running to answer.")
		}
	}
	tr.step(TraceStageBackend, "forwarded", TracePass, method+" "+upstream+" to "+target.Endpoint+".", map[string]string{"role": target.Role, "endpoint": target.Endpoint, "path": upstream})
	tr.t.Result.Kind, tr.t.Result.UpstreamPath = "app", upstream
	tr.t.Result.Detail = "Reaches the app as " + method + " " + upstream + "."
	return tr.t
}

func canaryIfSplit(backend string, canary *FlowBackend) *FlowBackend {
	if backend == canaryroute.Split {
		return canary
	}
	return nil
}

// Returns non-nil when the middleware ends the request.
func traceMiddleware(tr *tracer, idx int, m FlowMiddleware, mw *models.Middleware, scheme, host, reqPath, routePath string, headers, cookies map[string]string, ip string) *Trace {
	at := func(code, outcome, detail string, params map[string]string) *TraceStep {
		st := tr.step(TraceStageMiddleware, code, outcome, detail, params)
		st.Index = &idx
		if params == nil {
			st.Params = map[string]string{}
		}
		st.Params["name"] = m.Name
		return st
	}
	if m.Missing || mw == nil {
		at("mw_missing", TraceSkip, m.Name+" no longer exists; the gateway skips it.", nil)
		return nil
	}
	if len(mw.Paths) > 0 && !middlewarePathMatch(reqPath, routePath, mw.Paths) {
		at("mw_out_of_scope", TraceSkip, m.Name+" applies only to "+strings.Join(mw.Paths, ", ")+".", map[string]string{"paths": strings.Join(mw.Paths, ", ")})
		return nil
	}
	rule := mw.Rule
	str := func(k string) string { v, _ := rule[k].(string); return strings.TrimSpace(v) }
	redirect := func(location string) *Trace {
		code := 302
		if p, _ := rule["permanent"].(bool); p {
			code = 301
		}
		st := at("mw_redirect", TraceStop, fmt.Sprintf("%s redirects to %s.", m.Name, location), map[string]string{"location": location, "status": strconv.Itoa(code)})
		st.Status = code
		tr.t.Result.Location = location
		return tr.stop("redirect", code, fmt.Sprintf("%d redirect to %s.", code, location))
	}
	answer := func(code int, codeName, detail string) *Trace {
		st := at(codeName, TraceStop, detail, map[string]string{"status": strconv.Itoa(code)})
		st.Status = code
		return tr.stop("early_answer", code, fmt.Sprintf("%d from %s.", code, m.Name))
	}
	auth := canaryroute.Header(headers, "Authorization")
	authScheme := strings.ToLower(strings.SplitN(auth, " ", 2)[0])

	switch mw.Type {
	case "redirectScheme":
		target := str("scheme")
		if target == "" {
			target = "https"
		}
		if scheme != target {
			return redirect(target + "://" + host + reqPath)
		}
	case "redirectHost":
		canonical := ""
		switch {
		case str("host") != "":
			canonical = strings.ToLower(str("host"))
		case str("www") == "add" && !strings.HasPrefix(host, "www."):
			canonical = "www." + host
		case str("www") == "remove" && strings.HasPrefix(host, "www."):
			canonical = strings.TrimPrefix(host, "www.")
		}
		if canonical != "" && canonical != host {
			return redirect(scheme + "://" + canonical + reqPath)
		}
	case "redirect":
		if u := str("url"); u != "" {
			return redirect(u)
		}
	case "basicAuth", "ldapAuth":
		if authScheme != "basic" {
			return answer(401, "mw_auth_missing", m.Name+" asks for a username and password: no Basic credentials were sent.")
		}
		at("mw_auth_present", TracePass, "Basic credentials sent; the trace does not check them.", nil)
		return nil
	case "jwtAuth":
		if authScheme != "bearer" {
			return answer(401, "mw_auth_missing", m.Name+" needs a Bearer token: none was sent.")
		}
		at("mw_auth_present", TracePass, "Bearer token sent; the trace does not verify it.", nil)
		return nil
	case "oidc":
		if authScheme == "bearer" || len(cookies) > 0 {
			at("mw_auth_present", TracePass, "A token or session cookie was sent; the trace does not verify it.", nil)
			return nil
		}
		st := at("mw_oidc_login", TraceStop, m.Name+" sends a browser without a session to sign in.", map[string]string{"status": "302"})
		st.Status = 302
		return tr.stop("redirect", 302, "302 to the identity provider's sign-in.")
	case "forwardAuth":
		at("mw_forward_auth", TraceNote, m.Name+" asks an auth service, which may answer 401 or 403.", nil)
		return nil
	case "access":
		code := 403
		if c, ok := intRule(rule["statusCode"]); ok {
			code = c
		}
		return answer(code, "mw_blocked", m.Name+" blocks this path.")
	case "accessPolicy":
		if ip == "" {
			at("mw_needs_ip", TraceNote, m.Name+" allows or denies by client IP; give one to trace it.", nil)
			return nil
		}
		in := ipInRanges(ip, stringList(rule["sourceRanges"]))
		allow := strings.EqualFold(str("action"), "ALLOW")
		if in != allow {
			return answer(403, "mw_ip_denied", m.Name+" denies "+ip+".")
		}
	case "geoBlock":
		at("mw_geo", TraceNote, m.Name+" allows or denies by the client's country, which the trace cannot tell.", nil)
		return nil
	case "userAgentBlock":
		ua := strings.ToLower(canaryroute.Header(headers, "User-Agent"))
		for _, p := range stringList(rule["userAgents"]) {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" && strings.Contains(ua, p) {
				return answer(403, "mw_ua_blocked", m.Name+" blocks this User-Agent.")
			}
		}
	case "rateLimit":
		at("mw_rate_limit", TraceNote, m.Name+" passes this request; above its limit it answers 429.", nil)
		return nil
	case "bodyLimit":
		at("mw_body_limit", TraceNote, m.Name+" answers 413 to a body over its limit.", nil)
		return nil
	case "rewriteRegex", "addPrefix":
		at("mw_changes_path", TraceNote, m.Name+" changes the path the app receives.", nil)
		return nil
	}
	at("mw_pass", TracePass, m.Name+" lets the request through.", nil)
	return nil
}

func backendByRole(f *Flow, role string) *FlowBackend {
	for i := range f.Backends {
		if f.Backends[i].Role == role {
			return &f.Backends[i]
		}
	}
	return nil
}

func hostOnRoute(f *Flow, host string) bool {
	if len(f.Hosts) == 0 {
		return true
	}
	for _, h := range f.Hosts {
		if h.Host == host {
			return true
		}
	}
	return false
}

// The gateway picks the longest path among the routes on the host; only a longer one can take a request from this route.
func winningSibling(f *Flow, host, reqPath string) *FlowSibling {
	var best *FlowSibling
	for i := range f.Siblings {
		s := &f.Siblings[i]
		if !containsFold(s.SharedHosts, host) || len(s.Path) <= len(f.Route.Path) || !underPath(reqPath, s.Path) {
			continue
		}
		if best == nil || len(s.Path) > len(best.Path) {
			best = s
		}
	}
	return best
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func cleanURLPath(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// Goma swaps the matched route prefix for the rewrite and keeps the rest of the path.
func rewritePath(reqPath, routePath, rewrite string) string {
	if rewrite == "" {
		return reqPath
	}
	rest := strings.TrimPrefix(reqPath, strings.TrimRight(routePath, "/"))
	return cleanURLPath(rewrite + "/" + strings.TrimPrefix(rest, "/"))
}

// Mirrors Goma's IsPathMatching: patterns anchored at the start, tried as written and joined to the route path, case-insensitive.
func middlewarePathMatch(reqPath, routePath string, patterns []string) bool {
	for _, p := range patterns {
		for _, candidate := range []string{p, cleanURLPath(routePath + p)} {
			anchored := candidate
			if !strings.HasPrefix(anchored, "^") {
				anchored = "^" + anchored
			}
			if re, err := regexp.Compile("(?i)" + anchored); err == nil && re.MatchString(reqPath) {
				return true
			}
			if strings.EqualFold(reqPath, candidate) {
				return true
			}
			if base, ok := strings.CutSuffix(candidate, "/*"); ok && len(reqPath) >= len(base) && strings.EqualFold(reqPath[:len(base)], base) {
				return true
			}
		}
	}
	return false
}

func ipInRanges(ip string, ranges []string) bool {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if _, n, err := net.ParseCIDR(r); err == nil {
			if n.Contains(addr) {
				return true
			}
			continue
		}
		if other := net.ParseIP(r); other != nil && other.Equal(addr) {
			return true
		}
	}
	return false
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func intRule(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}
