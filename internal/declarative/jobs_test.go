// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

const jobsYAML = `
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec:
  image: ghcr.io/org/api
---
apiVersion: miabi.io/v1
kind: CronJob
metadata: { name: nightly-report }
spec:
  app: api
  schedule: "0 2 * * *"
  command: ["./api", "report", "send"]
  concurrencyPolicy: forbid
---
apiVersion: miabi.io/v1
kind: Job
metadata: { name: migrate }
spec:
  app: api
  command: ["./api", "migrate", "up"]
  runPolicy: onRelease
`

func TestParseCronJobAndJob(t *testing.T) {
	set, err := d.Parse([]byte(jobsYAML))
	if err != nil {
		t.Fatal(err)
	}
	cj, ok := set.Get("CronJob/nightly-report")
	if !ok || cj.CronJob.Schedule != "0 2 * * *" || cj.CronJob.ConcurrencyPolicy != "forbid" {
		t.Fatalf("cronjob = %+v", cj.CronJob)
	}
	j, ok := set.Get("Job/migrate")
	if !ok || j.Job.Policy() != d.RunOnRelease || j.Job.Waits() {
		t.Fatalf("job = %+v", j.Job)
	}
	edges := d.Edges(set)
	want := map[string]bool{"CronJob/nightly-report->Application/api": false, "Job/migrate->Application/api": false}
	for _, e := range edges {
		if _, ok := want[e.From+"->"+e.To]; ok {
			want[e.From+"->"+e.To] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing edge %s", k)
		}
	}
}

func TestJobsValidation(t *testing.T) {
	cases := map[string]string{
		"bad schedule":    "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"61 * * * *\", command: [x]}",
		"no schedule":     "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, command: [x]}",
		"no command":      "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\"}",
		"bad policy":      "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\", command: [x], concurrencyPolicy: never}",
		"registry no img": "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], registry: ghcr}",
		"no app":          "kind: Job\nmetadata: {name: j}\nspec: {command: [x]}",
		"bad run policy":  "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], runPolicy: always}",
		"wait with once":  "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], runPolicy: once, waitForDeploy: true}",
		"bad runAsUser":   "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], security: {runAsUser: \"a:b:c\"}}",
	}
	for name, doc := range cases {
		if _, err := d.Parse([]byte("apiVersion: miabi.io/v1\n" + doc)); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func parseOne(t *testing.T, doc string) *d.ResourceSet {
	t.Helper()
	set, err := d.Parse([]byte("apiVersion: miabi.io/v1\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// An omitted suspend leaves a console pause alone; a stated one converges.
func TestCronJobSuspendOptionalWhenUnset(t *testing.T) {
	live := parseOne(t, "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\", command: [x], suspend: true}")
	silent := parseOne(t, "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\", command: [x]}")
	if p := d.BuildPlan(silent, live, d.PlanOptions{}); len(p.Changes) != 0 {
		t.Errorf("silent suspend should not drift: %+v", p.Changes)
	}
	stated := parseOne(t, "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\", command: [x], suspend: false}")
	p := d.BuildPlan(stated, live, d.PlanOptions{})
	if len(p.Changes) != 1 || p.Changes[0].Fields[0].Field != "suspend" {
		t.Errorf("stated suspend should converge: %+v", p.Changes)
	}
	defaults := parseOne(t, "kind: CronJob\nmetadata: {name: c}\nspec: {app: api, schedule: \"@daily\", command: [x], concurrencyPolicy: allow, historyLimit: 20}")
	if p := d.BuildPlan(defaults, silent, d.PlanOptions{}); len(p.Changes) != 0 {
		t.Errorf("explicit defaults should equal omitted ones: %+v", p.Changes)
	}
}

// A Job's run template is compared only through its fingerprint, so the same
// spec never plans a run twice, and the plan marks the change as one "spec" field.
func TestJobDiffByFingerprint(t *testing.T) {
	doc := "kind: Job\nmetadata: {name: migrate}\nspec: {app: api, command: [./api, migrate]}"
	desired := parseOne(t, doc)
	live := parseOne(t, doc)
	r, _ := live.Get("Job/migrate")
	r.Job.SpecFP = r.Job.Fingerprint()
	live.Add(r)
	if p := d.BuildPlan(desired, live, d.PlanOptions{}); len(p.Changes) != 0 {
		t.Errorf("same spec should be a noop: %+v", p.Changes)
	}
	changed := parseOne(t, "kind: Job\nmetadata: {name: migrate}\nspec: {app: api, command: [./api, migrate, --force]}")
	p := d.BuildPlan(changed, live, d.PlanOptions{})
	if len(p.Changes) != 1 || len(p.Changes[0].Fields) != 1 || p.Changes[0].Fields[0].Field != d.JobSpecField {
		t.Fatalf("changed command = %+v", p.Changes)
	}
	policy := parseOne(t, "kind: Job\nmetadata: {name: migrate}\nspec: {app: api, command: [./api, migrate], historyLimit: 5}")
	p = d.BuildPlan(policy, live, d.PlanOptions{})
	if len(p.Changes) != 1 || p.Changes[0].Fields[0].Field != "historyLimit" {
		t.Errorf("history change should not touch the spec: %+v", p.Changes)
	}
}

func TestJobsRankAfterApplication(t *testing.T) {
	set, err := d.Parse([]byte(jobsYAML))
	if err != nil {
		t.Fatal(err)
	}
	p := d.BuildPlan(set, d.NewResourceSet(), d.PlanOptions{})
	var order []string
	for _, c := range p.Changes {
		order = append(order, string(c.Kind))
	}
	if got := strings.Join(order, ","); got != "Application,CronJob,Job" {
		t.Errorf("order = %s", got)
	}
}

func TestJobAppIsAReference(t *testing.T) {
	set := parseOne(t, "kind: Job\nmetadata: {name: j}\nspec: {app: ghost, command: [x]}")
	err := d.CheckReferences(set.References(), d.KnownNames{"applications": {"api": true}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown app should be reported, got %v", err)
	}
}
