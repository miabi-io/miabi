// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"errors"
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func jobResource(t *testing.T, doc string) d.Resource {
	t.Helper()
	set, err := d.Parse([]byte("apiVersion: miabi.io/v1\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	return set.All()[0]
}

func TestJobRunDecision(t *testing.T) {
	onChange := jobResource(t, "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x]}")
	once := jobResource(t, "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], runPolicy: once}")
	release := jobResource(t, "kind: Job\nmetadata: {name: j}\nspec: {app: api, command: [x], runPolicy: onRelease}")
	adopt := jobResource(t, "kind: Job\nmetadata: {name: j, annotations: {miabi.io/skip-initial-run: \"true\"}}\nspec: {app: api, command: [x]}")

	create := d.Change{Action: d.ActionCreate, Kind: d.KindJob, Name: "j"}
	specUpdate := d.Change{Action: d.ActionUpdate, Kind: d.KindJob, Name: "j", Fields: []d.FieldDiff{{Field: d.JobSpecField}}}
	otherUpdate := d.Change{Action: d.ActionUpdate, Kind: d.KindJob, Name: "j", Fields: []d.FieldDiff{{Field: "historyLimit"}}}

	cases := []struct {
		name string
		ch   d.Change
		res  d.Resource
		run  bool
	}{
		{"create runs", create, onChange, true},
		{"create adopting skips the run", create, adopt, false},
		{"onChange spec change runs", specUpdate, onChange, true},
		{"onChange other change doesn't run", otherUpdate, onChange, false},
		{"onRelease spec change waits for a release", specUpdate, release, false},
		{"once other change is allowed", otherUpdate, once, false},
	}
	for _, c := range cases {
		run, err := jobRun(c.ch, c.res)
		if err != nil || run != c.run {
			t.Errorf("%s: run = %v, err = %v", c.name, run, err)
		}
	}
	if _, err := jobRun(specUpdate, once); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("once spec change: err = %v", err)
	}
}

func TestRefuseRunMove(t *testing.T) {
	for _, kind := range []d.Kind{d.KindCronJob, d.KindJob} {
		ch := d.Change{Action: d.ActionUpdate, Kind: kind, Name: "j", Fields: []d.FieldDiff{{Field: "app", From: "api", To: "web"}}}
		if err := refuseImmutable(ch); !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "web") {
			t.Errorf("%s app move: err = %v", kind, err)
		}
	}
}

// An export carries the app's own CronJobs and Jobs, without identity or the
// fields an apply elsewhere would default anyway.
func TestExportJobs(t *testing.T) {
	set, err := d.Parse([]byte(`
apiVersion: miabi.io/v1
kind: CronJob
metadata: { name: report, uid: abc }
spec: { app: api, schedule: "@daily", command: [x], suspend: false }
---
apiVersion: miabi.io/v1
kind: Job
metadata: { name: migrate }
spec: { app: api, command: [x], waitForDeploy: true }
---
apiVersion: miabi.io/v1
kind: Job
metadata: { name: other }
spec: { app: web, command: [x] }
`))
	if err != nil {
		t.Fatal(err)
	}
	out := d.NewResourceSet()
	exportJobs(set, out, "api")
	if len(out.All()) != 2 {
		t.Fatalf("exported %d resources, want the two of app api", len(out.All()))
	}
	cj, _ := out.Get("CronJob/report")
	if cj.Metadata.UID != "" || cj.CronJob.Suspend != nil {
		t.Errorf("cronjob export = %+v / %+v", cj.Metadata, cj.CronJob)
	}
	j, _ := out.Get("Job/migrate")
	if j.Job.WaitForDeploy != nil {
		t.Error("the default waitForDeploy should be left out")
	}
}
