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

package declarative

import (
	"regexp"
	"sort"
	"strings"
)

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
	// Only drawn for a middleware in the same bundle; naming an existing workspace one is not a dangling reference.
	EdgeMiddleware EdgeType = "middleware"
	// EdgeSchedule is CronJob -> Application (spec.app).
	EdgeSchedule EdgeType = "schedule"
	// EdgeRun is Job -> Application (spec.app).
	EdgeRun EdgeType = "run"
	// EdgeRepository is Application or GitSource -> GitRepository (the credential it clones with).
	EdgeRepository EdgeType = "repository"
)

type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Type EdgeType `json:"type"`
}

// Mirrors the keys the renderer exposes (see render.go data()).
var tmplRef = regexp.MustCompile(`\{\{[-\s]*\.(databases|secrets|applications)\.([A-Za-z0-9_-]+)`)

// Edges must run on the un-rendered set, where env templates still reveal soft references.
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
			// add() skips targets outside the set, so a credential created elsewhere leaves no dangling edge.
			add(KindApplication, name, KindRegistry, a.Registry, EdgeRegistry)
			if a.Source != nil {
				add(KindApplication, name, KindGitRepository, a.Source.Repository, EdgeRepository)
			}
			for _, v := range a.Env {
				for _, m := range tmplRef.FindAllStringSubmatch(v, -1) {
					switch m[1] {
					case "databases":
						add(KindApplication, name, KindDatabase, m[2], EdgeDatabase)
					case "secrets":
						add(KindApplication, name, KindSecret, m[2], EdgeSecret)
						add(KindApplication, name, KindSealedSecret, m[2], EdgeSecret)
					case "applications":
						add(KindApplication, name, KindApplication, m[2], EdgeAppRef)
					}
				}
			}
		case r.GitSource != nil:
			add(KindGitSource, name, KindGitRepository, r.GitSource.Repository, EdgeRepository)
		case r.GitRepository != nil:
			add(KindGitRepository, name, KindSecret, r.GitRepository.SecretRef, EdgeSecret)
			add(KindGitRepository, name, KindSealedSecret, r.GitRepository.SecretRef, EdgeSecret)
		case r.CronJob != nil:
			add(KindCronJob, name, KindApplication, r.CronJob.App, EdgeSchedule)
			add(KindCronJob, name, KindRegistry, r.CronJob.Registry, EdgeRegistry)
		case r.Job != nil:
			add(KindJob, name, KindApplication, r.Job.App, EdgeRun)
			add(KindJob, name, KindRegistry, r.Job.Registry, EdgeRegistry)
		case r.Route != nil:
			add(KindRoute, name, KindApplication, r.Route.App, EdgeRoute)
			for _, mw := range r.Route.Middlewares {
				add(KindRoute, name, KindMiddleware, mw, EdgeMiddleware)
			}
			// A route may answer on several hosts spanning more than one domain.
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
