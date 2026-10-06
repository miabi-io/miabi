// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/robfig/cron/v3"
)

// Annotation that creates a Job definition without its first run, to adopt a job
// that has already run elsewhere.
const AnnotationSkipInitialRun = "miabi.io/skip-initial-run"

// defaultJobHistory mirrors the job service's history default, so an omitted
// historyLimit and a stored 0 compare equal.
const defaultJobHistory = 20

// maxBackoffLimit caps retries, so a broken migration can't loop for hours.
const maxBackoffLimit = 10

var concurrencyPolicies = []string{models.ConcurrencyAllow, models.ConcurrencyForbid, models.ConcurrencyReplace}

func (r *Resource) validateCronJob() error {
	c := r.CronJob
	name := r.Metadata.Name
	if c == nil {
		return fmt.Errorf("cronjob %q: spec is required", name)
	}
	if strings.TrimSpace(c.App) == "" {
		return fmt.Errorf("cronjob %q: app is required", name)
	}
	if strings.TrimSpace(c.Schedule) == "" {
		return fmt.Errorf("cronjob %q: schedule is required", name)
	}
	if _, err := cron.ParseStandard(c.Schedule); err != nil {
		return fmt.Errorf("cronjob %q: invalid schedule %q: %v", name, c.Schedule, err)
	}
	if c.ConcurrencyPolicy != "" && !contains(concurrencyPolicies, c.ConcurrencyPolicy) {
		return fmt.Errorf("cronjob %q: concurrencyPolicy must be one of %s", name, strings.Join(concurrencyPolicies, ", "))
	}
	return validateRunTemplate("cronjob", name, c.Command, c.Image, c.Registry, c.TimeoutSeconds, c.HistoryLimit, c.Security)
}

func (r *Resource) validateJob() error {
	j := r.Job
	name := r.Metadata.Name
	if j == nil {
		return fmt.Errorf("job %q: spec is required", name)
	}
	if strings.TrimSpace(j.App) == "" {
		return fmt.Errorf("job %q: app is required", name)
	}
	switch j.Policy() {
	case RunOnChange, RunOnce, RunOnRelease:
	default:
		return fmt.Errorf("job %q: runPolicy must be one of %s, %s, %s", name, RunOnChange, RunOnce, RunOnRelease)
	}
	if j.WaitForDeploy != nil && j.Policy() != RunOnChange {
		return fmt.Errorf("job %q: waitForDeploy only applies to runPolicy %s", name, RunOnChange)
	}
	if j.BackoffLimit < 0 || j.BackoffLimit > maxBackoffLimit {
		return fmt.Errorf("job %q: backoffLimit must be between 0 and %d", name, maxBackoffLimit)
	}
	return validateRunTemplate("job", name, j.Command, j.Image, j.Registry, j.TimeoutSeconds, j.HistoryLimit, j.Security)
}

func validateRunTemplate(kind, name string, command []string, image, registry string, timeout, history int, sec *SecuritySpec) error {
	if !hasNonEmpty(command) {
		return fmt.Errorf("%s %q: command is required", kind, name)
	}
	if registry != "" && strings.TrimSpace(image) == "" {
		return fmt.Errorf("%s %q: registry is only valid with image", kind, name)
	}
	if timeout < 0 || history < 0 {
		return fmt.Errorf("%s %q: timeoutSeconds and historyLimit must not be negative", kind, name)
	}
	if sec != nil {
		if _, err := models.NormalizeRunAsUser(sec.RunAsUser); err != nil {
			return fmt.Errorf("%s %q: security.runAsUser: %v", kind, name, err)
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Policy returns the run policy, defaulting to onChange.
func (j *JobSpec) Policy() string {
	if j.RunPolicy == "" {
		return RunOnChange
	}
	return j.RunPolicy
}

// Waits reports whether a run is held while the app has a deploy in flight.
func (j *JobSpec) Waits() bool {
	return j.Policy() == RunOnChange && (j.WaitForDeploy == nil || *j.WaitForDeploy)
}

// RunAsUser returns the pinned account, or "".
func (j *JobSpec) RunAsUser() string {
	if j.Security == nil {
		return ""
	}
	return j.Security.RunAsUser
}

// RunAsUser returns the pinned account, or "".
func (c *CronJobSpec) RunAsUser() string {
	if c.Security == nil {
		return ""
	}
	return c.Security.RunAsUser
}

// Fingerprint hashes what a run executes: a change to any of these is a new run
// under onChange. Policy, history and app are not part of it.
func (j *JobSpec) Fingerprint() string {
	user, _ := models.NormalizeRunAsUser(j.RunAsUser())
	h := sha256.New()
	for _, part := range []string{
		strings.Join(j.Command, "\x1f"), strings.Join(j.Entrypoint, "\x1f"),
		strings.TrimSpace(j.Image), j.Registry, user, strconv.Itoa(j.TimeoutSeconds),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func cronJobFields(c *CronJobSpec, f map[string]string) {
	f["app"] = c.App
	f["schedule"] = strings.TrimSpace(c.Schedule)
	f["command"] = argList(c.Command)
	f["entrypoint"] = argList(c.Entrypoint)
	f["image"] = strings.TrimSpace(c.Image)
	f["registry"] = c.Registry
	f["timeoutSeconds"] = strconv.Itoa(c.TimeoutSeconds)
	policy := c.ConcurrencyPolicy
	if policy == "" {
		policy = models.ConcurrencyAllow
	}
	f["concurrencyPolicy"] = policy
	f["historyLimit"] = strconv.Itoa(historyOrDefault(c.HistoryLimit))
	user, _ := models.NormalizeRunAsUser(c.RunAsUser())
	f["security.runAsUser"] = user
	if c.Suspend != nil {
		f["suspend"] = strconv.FormatBool(*c.Suspend)
	}
}

// diffJob compares a Job definition. The run template is compared only through
// its fingerprint, as one "spec" field, so the plan says whether it will run
// rather than listing every changed argument.
func diffJob(actual, desired Resource) []FieldDiff {
	a, d := actual.Job, desired.Job
	if d == nil {
		return nil
	}
	if a == nil {
		a = &JobSpec{}
	}
	var out []FieldDiff
	add := func(field, from, to string) {
		if from != to {
			out = append(out, FieldDiff{Field: field, From: from, To: to})
		}
	}
	add("app", a.App, d.App)
	add("historyLimit", strconv.Itoa(historyOrDefault(a.HistoryLimit)), strconv.Itoa(historyOrDefault(d.HistoryLimit)))
	add("runPolicy", a.Policy(), d.Policy())
	add("waitForDeploy", strconv.FormatBool(a.Waits()), strconv.FormatBool(d.Waits()))
	add("backoffLimit", strconv.Itoa(a.BackoffLimit), strconv.Itoa(d.BackoffLimit))
	from := a.SpecFP
	if from == "" {
		from = a.Fingerprint()
	}
	if to := d.Fingerprint(); from != to {
		out = append(out, FieldDiff{Field: JobSpecField, From: from, To: to})
	}
	return out
}

// JobSpecField is the diff field a changed Job run template shows up as.
const JobSpecField = "spec"

func historyOrDefault(n int) int {
	if n <= 0 {
		return defaultJobHistory
	}
	return n
}

func argList(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return strings.Join(q, " ")
}
