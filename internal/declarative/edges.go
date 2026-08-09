// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"regexp"
	"sort"
	"strings"
)

// EdgeType classifies a dependency between two resources, so a topology view can
// label and style the link.
type EdgeType string

const (
	EdgeMount    EdgeType = "mount"    // Application -> Volume (mounts[].volume)
	EdgeStack    EdgeType = "stack"    // Application -> Stack  (stack)
	EdgeRoute    EdgeType = "route"    // Route       -> Application (app)
	EdgeDomain   EdgeType = "domain"   // Route       -> Domain (host under the domain)
	EdgeDatabase EdgeType = "database" // Application -> Database (env {{ .databases.X }})
	EdgeSecret   EdgeType = "secret"   // Application -> Secret   (env {{ .secrets.X }})
	EdgeAppRef   EdgeType = "app-ref"  // Application -> Application (env {{ .applications.X }})
	EdgeRegistry EdgeType = "registry" // Application -> Registry (spec.registry)
	EdgeConfig   EdgeType = "config"   // Application -> Config   (mounts[].config)
)

// Edge is a directed dependency from one resource to another, keyed by Resource
// Key() ("<Kind>/<name>"). From depends on / targets To.
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Type EdgeType `json:"type"`
}

// tmplRef matches a template reference into a named resource collection,
// capturing the collection and the resource name. Mirrors the keys the renderer
// exposes (see render.go data()).
var tmplRef = regexp.MustCompile(`\{\{[-\s]*\.(databases|secrets|applications)\.([A-Za-z0-9_-]+)`)

// Edges derives the cross-resource dependency edges within a set. It must run on the parsed
// (un-rendered) set, where env still carries the "{{ .databases.X }}" templates that reveal soft
// references. Only edges whose target is in the set are emitted; the result is deduplicated.
func Edges(set *ResourceSet) []Edge {
	var out []Edge
	seen := map[Edge]bool{}
	add := func(fromKind Kind, fromName string, toKind Kind, toName string, t EdgeType) {
		if toName == "" || !set.Has(toKind, toName) {
			return
		}
		// A resource never links to itself (e.g. an app referencing its own alias).
		if fromKind == toKind && fromName == toName {
			return
		}
		e := Edge{
			From: string(fromKind) + "/" + fromName,
			To:   string(toKind) + "/" + toName,
			Type: t,
		}
		if seen[e] {
			return
		}
		seen[e] = true
		out = append(out, e)
	}

	domains := set.ByKind(KindDomain)

	for _, r := range set.All() {
		name := r.Metadata.Name
		switch {
		case r.Application != nil:
			a := r.Application
			if a.Stack != "" {
				add(KindApplication, name, KindStack, a.Stack, EdgeStack)
			}
			for _, mt := range a.Mounts {
				if mt.Config != "" {
					add(KindApplication, name, KindConfig, mt.Config, EdgeConfig)
					continue
				}
				add(KindApplication, name, KindVolume, mt.Volume, EdgeMount)
			}
			// Only drawn when the credential is declared in the same bundle — add()
			// skips targets outside the set, so referencing a workspace credential
			// created elsewhere leaves no dangling edge.
			add(KindApplication, name, KindRegistry, a.Registry, EdgeRegistry)
			for _, v := range a.Env {
				for _, m := range tmplRef.FindAllStringSubmatch(v, -1) {
					switch m[1] {
					case "databases":
						add(KindApplication, name, KindDatabase, m[2], EdgeDatabase)
					case "secrets":
						add(KindApplication, name, KindSecret, m[2], EdgeSecret)
					case "applications":
						add(KindApplication, name, KindApplication, m[2], EdgeAppRef)
					}
				}
			}
		case r.Route != nil:
			add(KindRoute, name, KindApplication, r.Route.App, EdgeRoute)
			// Link the route to each owning Domain when a host is the domain or a
			// subdomain of it (the Domain's name is a real FQDN). A route may answer
			// on several hosts spanning more than one domain.
			for _, h := range r.Route.Hosts {
				if d := matchDomain(h, domains); d != "" {
					add(KindRoute, name, KindDomain, d, EdgeDomain)
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// matchDomain returns the name of the most specific domain that host falls
// under (exact match or a subdomain), or "" when none applies.
func matchDomain(host string, domains []Resource) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	best := ""
	for _, d := range domains {
		dn := strings.ToLower(d.Metadata.Name)
		if host == dn || strings.HasSuffix(host, "."+dn) {
			if len(dn) > len(best) {
				best = d.Metadata.Name
			}
		}
	}
	return best
}
