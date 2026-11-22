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

package registryserver

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// retentionSvc has web at ages 1d (latest, v4), 10d (v3), 40d (v2), 90d (v1), and one image with no age.
func retentionSvc(t *testing.T) (*Service, map[string]string) {
	t.Helper()
	tags := map[string]string{"latest": "a", "v4": "a", "v3": "b", "v2": "c", "v1": "d", "mystery": "e"}
	svc := agedRegistry(t, tags, map[string]time.Time{"a": days(1), "b": days(10), "c": days(40), "d": days(90), "e": {}})
	svc.SetCatalog(stubCatalog{digests: map[string]bool{}})
	return svc, tags
}

func remaining(tags map[string]string) []string {
	out := make([]string, 0, len(tags))
	for t := range tags {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

func run(t *testing.T, svc *Service, p RetentionPolicy, dryRun bool) RetentionWorkspaceReport {
	t.Helper()
	reps, err := svc.Retention(context.Background(), p, dryRun, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 {
		t.Fatalf("reports = %+v", reps)
	}
	return reps[0]
}

func TestRetentionKeepsTheNewestImages(t *testing.T) {
	svc, tags := retentionSvc(t)
	rep := run(t, svc, RetentionPolicy{KeepImages: 2}, false)
	if rep.Count != 2 || !slices.Equal(remaining(tags), []string{"latest", "mystery", "v3", "v4"}) {
		t.Fatalf("count %d, left %v; the newest two images stay, and an image with no age is never deleted", rep.Count, remaining(tags))
	}
}

func TestRetentionDryRunDeletesNothing(t *testing.T) {
	svc, tags := retentionSvc(t)
	rep := run(t, svc, RetentionPolicy{KeepImages: 2}, true)
	if rep.Count != 2 || len(tags) != 6 {
		t.Fatalf("count %d, left %d tags", rep.Count, len(tags))
	}
	if len(rep.Repositories) != 1 || !slices.Equal(rep.Repositories[0].Tags, []string{"v2", "v1"}) {
		t.Fatalf("report = %+v", rep.Repositories)
	}
}

func TestRetentionKeepsTheRollbackWindowAndProtectedTags(t *testing.T) {
	svc, tags := retentionSvc(t)
	svc.SetCatalog(stubCatalog{
		digests:    map[string]bool{},
		recentRefs: map[string]bool{"registry.example.com/ws_7/web:v1": true},
	})
	run(t, svc, RetentionPolicy{KeepImages: 1, ProtectedTags: []string{"v3"}}, false)
	if !slices.Equal(remaining(tags), []string{"latest", "mystery", "v1", "v3", "v4"}) {
		t.Fatalf("left %v; v1 is a recent release's image and v3 matches a protected pattern", remaining(tags))
	}
}

func TestRetentionHonoursTheMinimumAge(t *testing.T) {
	svc, tags := retentionSvc(t)
	run(t, svc, RetentionPolicy{KeepImages: 1, MinAgeDays: 60}, false)
	if _, ok := tags["v1"]; ok {
		t.Fatal("v1 is 90 days old and past the newest image, so it goes")
	}
	if _, ok := tags["v2"]; !ok {
		t.Fatal("v2 is 40 days old, younger than the 60-day minimum")
	}
}

func TestRetentionOverrides(t *testing.T) {
	cases := []struct {
		name      string
		override  models.RegistryRetentionOverride
		wantCount int
		skipped   bool
	}{
		{"opted out", models.RegistryRetentionOverride{WorkspaceID: 7, Disabled: true}, 0, true},
		{"keeps more", models.RegistryRetentionOverride{WorkspaceID: 7, KeepImages: 10}, 0, false},
		{"can't keep fewer", models.RegistryRetentionOverride{WorkspaceID: 7, KeepImages: 1}, 2, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := retentionSvc(t)
			rep := run(t, svc, RetentionPolicy{KeepImages: 2, Overrides: map[uint]models.RegistryRetentionOverride{7: c.override}}, true)
			if rep.Count != c.wantCount || rep.Skipped != c.skipped {
				t.Fatalf("count %d skipped %v, want %d %v", rep.Count, rep.Skipped, c.wantCount, c.skipped)
			}
		})
	}
}

type memRetentionStore struct {
	policy  models.RegistryRetention
	running bool
	runs    []models.RegistryRetentionRun
}

func (m *memRetentionStore) Policy() (*models.RegistryRetention, error) { return &m.policy, nil }
func (m *memRetentionStore) Overrides() (map[uint]models.RegistryRetentionOverride, error) {
	return nil, nil
}
func (m *memRetentionStore) StartRun(run *models.RegistryRetentionRun, _ time.Duration) (bool, error) {
	if m.running {
		return false, nil
	}
	m.running = true
	return true, nil
}
func (m *memRetentionStore) FinishRun(run *models.RegistryRetentionRun) error {
	m.running = false
	m.runs = append(m.runs, *run)
	return nil
}

func TestRetentionRunnerRecordsTheRun(t *testing.T) {
	svc, tags := retentionSvc(t)
	store := &memRetentionStore{policy: models.RegistryRetention{Enabled: true, KeepImages: 2}}
	var told []RetentionWorkspaceReport
	r := NewRetentionRunner(svc, store, func(rep RetentionWorkspaceReport) { told = append(told, rep) })
	r.now = func() time.Time { return ruleNow }

	if err := r.Scheduled(); err != nil {
		t.Fatal(err)
	}
	if len(store.runs) != 1 || store.runs[0].Status != models.RetentionRunSucceeded || store.runs[0].Tags != 2 {
		t.Fatalf("runs = %+v", store.runs)
	}
	if len(told) != 1 || told[0].WorkspaceID != 7 || len(tags) != 4 {
		t.Fatalf("told %+v, left %v", told, tags)
	}

	store.running = true
	if _, err := r.Start(true, "manual"); !errors.Is(err, ErrRetentionRunning) {
		t.Fatalf("err = %v; one run at a time", err)
	}
}

func TestScheduledDoesNothingWhileOff(t *testing.T) {
	svc, tags := retentionSvc(t)
	store := &memRetentionStore{policy: models.RegistryRetention{KeepImages: 1}}
	if err := NewRetentionRunner(svc, store, nil).Scheduled(); err != nil {
		t.Fatal(err)
	}
	if len(store.runs) != 0 || len(tags) != 6 {
		t.Fatal("a disabled policy ran")
	}
}
