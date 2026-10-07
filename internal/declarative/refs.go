// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Reference is one {{ .collection.name.field }} reference a bundle's templates make.
type Reference struct {
	// Where locates it for an error message, e.g. `application "web" env DATABASE_URL`.
	Where      string
	Collection string // databases | secrets | applications | inputs
	Name       string
	Field      string // empty when the reference names no field
}

func (r Reference) String() string {
	ref := "." + r.Collection + "." + r.Name
	if r.Field != "" {
		ref += "." + r.Field
	}
	return ref + " in " + r.Where
}

// Fields a database and an application reference may name, as Renderer.ref resolves them.
var (
	databaseRefFields    = []string{"host", "port", "user", "password", "name", "uri", "url"}
	applicationRefFields = []string{"host", "port", "scheme", "url", "alias"}
)

// References lists the references in every value apply renders: application env, registry passwords,
// middleware rules and config files (under their own delimiters).
func (s *ResourceSet) References() []Reference {
	var out []Reference
	for _, r := range s.All() {
		switch {
		case r.Application != nil:
			for k, v := range r.Application.Env {
				out = append(out, refsIn(fmt.Sprintf("application %q env %s", r.Metadata.Name, k), v, actionPattern)...)
			}
		case r.Registry != nil:
			out = append(out, refsIn(fmt.Sprintf("registry %q password", r.Metadata.Name), r.Registry.Password, actionPattern)...)
		case r.Middleware != nil:
			out = append(out, ruleRefs(fmt.Sprintf("middleware %q rule", r.Metadata.Name), r.Middleware.Rule)...)
		case r.CronJob != nil:
			out = append(out, Reference{Where: fmt.Sprintf("cronjob %q app", r.Metadata.Name), Collection: "applications", Name: r.CronJob.App})
		case r.Job != nil:
			out = append(out, Reference{Where: fmt.Sprintf("job %q app", r.Metadata.Name), Collection: "applications", Name: r.Job.App})
		case r.Config != nil:
			action := actionPattern
			if d := r.Config.Delimiters; len(d) == 2 && (d[0] != "{{" || d[1] != "}}") {
				action = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(d[0]) + `.*?` + regexp.QuoteMeta(d[1]))
			}
			for k, v := range r.Config.Data {
				out = append(out, refsIn(fmt.Sprintf("config %q file %s", r.Metadata.Name, k), v, action)...)
			}
		}
	}
	return out
}

func ruleRefs(where string, v any) []Reference {
	switch t := v.(type) {
	case string:
		return refsIn(where, t, actionPattern)
	case map[string]any:
		var out []Reference
		for k, x := range t {
			out = append(out, ruleRefs(where+"."+k, x)...)
		}
		return out
	case []any:
		var out []Reference
		for i, x := range t {
			out = append(out, ruleRefs(fmt.Sprintf("%s[%d]", where, i), x)...)
		}
		return out
	}
	return nil
}

func refsIn(where, s string, action *regexp.Regexp) []Reference {
	var out []Reference
	for _, a := range action.FindAllString(s, -1) {
		for _, m := range refPattern.FindAllStringSubmatch(a, -1) {
			keys := strings.Split(strings.Trim(m[2], "."), ".")
			ref := Reference{Where: where, Collection: m[1], Name: keys[0]}
			if len(keys) > 1 {
				ref.Field = keys[1]
			}
			out = append(out, ref)
		}
	}
	return out
}

// KnownNames holds, per collection, the names a reference may resolve to: those declared in the bundle
// and those already in the workspace.
type KnownNames map[string]map[string]bool

// CheckReferences reports every reference to a name that exists nowhere, or to a field its kind lacks.
// Those can only fail at apply, so a dry run should say so. A name that does exist is never flagged,
// even where apply might still resolve it late: this check never blocks what apply would accept.
func CheckReferences(refs []Reference, known KnownNames) error {
	var problems []string
	seen := map[string]bool{}
	for _, r := range refs {
		var msg string
		switch {
		case !known[r.Collection][r.Name]:
			msg = fmt.Sprintf("unknown %s %q (%s)", singular(r.Collection), r.Name, r)
		case r.Collection == "databases" && r.Field != "" && !slices.Contains(databaseRefFields, r.Field):
			msg = fmt.Sprintf("database %q has no field %q (%s); use one of %s", r.Name, r.Field, r, strings.Join(databaseRefFields, ", "))
		case r.Collection == "applications" && r.Field != "" && !slices.Contains(applicationRefFields, r.Field):
			msg = fmt.Sprintf("application %q has no field %q (%s); use one of %s", r.Name, r.Field, r, strings.Join(applicationRefFields, ", "))
		default:
			continue
		}
		if !seen[msg] {
			seen[msg] = true
			problems = append(problems, msg)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("unresolvable references:\n  %s", strings.Join(problems, "\n  "))
}

func singular(collection string) string {
	switch collection {
	case "databases":
		return "database"
	case "secrets":
		return "secret"
	case "applications":
		return "application"
	case "inputs":
		return "input"
	}
	return collection
}
