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
package restriction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
)

type fakeRepo struct {
	ws    map[uint]*models.Workspace
	reads int
}

func (f *fakeRepo) FindByID(id uint) (*models.Workspace, error) {
	f.reads++
	w, ok := f.ws[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *w
	return &cp, nil
}

func (f *fakeRepo) SetRestriction(id uint, fields map[string]any) error {
	w := f.ws[id]
	for k, v := range fields {
		switch k {
		case "restriction":
			w.Restriction = v.(models.WorkspaceRestriction)
		case "restriction_reason":
			w.RestrictionReason = v.(string)
		case "restriction_until":
			w.RestrictionUntil, _ = v.(*time.Time)
		case "frozen_resources":
			if v == nil {
				w.FrozenResources = nil
			} else {
				w.FrozenResources = &models.FrozenResources{Apps: []uint{7}, Databases: []uint{9}}
			}
		}
	}
	return nil
}

func (f *fakeRepo) ListRestrictionsDue(now time.Time) ([]models.Workspace, error) {
	var out []models.Workspace
	for _, w := range f.ws {
		if w.Restriction != "" && w.RestrictionUntil != nil && !w.RestrictionUntil.After(now) {
			out = append(out, *w)
		}
	}
	return out, nil
}

type fakeWorkloads struct {
	stopped int
	started []*models.FrozenResources
}

func (f *fakeWorkloads) StopWorkspace(context.Context, uint) (account.StopResult, models.FrozenResources) {
	f.stopped++
	return account.StopResult{Apps: 1, Databases: 1}, models.FrozenResources{Apps: []uint{7}, Databases: []uint{9}}
}

func (f *fakeWorkloads) StartWorkspace(_ context.Context, _ uint, only *models.FrozenResources) (account.StartResult, error) {
	f.started = append(f.started, only)
	return account.StartResult{}, nil
}

func newTest() (*Service, *fakeRepo, *fakeWorkloads) {
	repo := &fakeRepo{ws: map[uint]*models.Workspace{1: {ID: 1, Name: "acme"}, 2: {ID: 2, Name: "system", System: true}}}
	wl := &fakeWorkloads{}
	return NewService(repo, wl), repo, wl
}

func TestAllowedMatrix(t *testing.T) {
	s, _, _ := newTest()
	ctx := context.Background()
	if err := s.Allowed(1, ActionWrite); err != nil {
		t.Fatalf("unrestricted write: %v", err)
	}
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionReadOnly, Reason: "unpaid"}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		a    Action
		want string
	}{{ActionWrite, "WORKSPACE_READ_ONLY"}, {ActionDeploy, "WORKSPACE_READ_ONLY"}, {ActionRun, ""}} {
		err := s.Allowed(1, tt.a)
		var re *Error
		if tt.want == "" && err != nil || tt.want != "" && (!errors.As(err, &re) || re.Code() != tt.want) {
			t.Errorf("read_only action %d: %v", tt.a, err)
		}
	}
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionSuspended}); err != nil {
		t.Fatal(err)
	}
	if err := s.Allowed(1, ActionRun); err == nil {
		t.Fatal("a suspended workspace must not run workloads")
	}
	var nilSvc *Service
	if err := nilSvc.Allowed(1, ActionWrite); err != nil {
		t.Fatal("a nil service allows everything")
	}
}

func TestSuspendStopsAndLiftRestartsExactly(t *testing.T) {
	s, repo, wl := newTest()
	ctx := context.Background()
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionSuspended, Reason: "fraud"}); err != nil {
		t.Fatal(err)
	}
	if wl.stopped != 1 || repo.ws[1].FrozenResources == nil {
		t.Fatalf("suspend: stopped %d frozen %v", wl.stopped, repo.ws[1].FrozenResources)
	}
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionSuspended}); err != nil {
		t.Fatal(err)
	}
	if wl.stopped != 1 {
		t.Fatal("suspending again must not stop again and overwrite what to restart")
	}
	if _, err := s.Lift(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(wl.started) != 1 || len(wl.started[0].Apps) != 1 || wl.started[0].Apps[0] != 7 {
		t.Fatalf("lift restarted %v", wl.started)
	}
	if repo.ws[1].Restriction != "" || repo.ws[1].FrozenResources != nil {
		t.Fatalf("after lift: %+v", repo.ws[1])
	}
}

func TestSuspendedToReadOnlyRestarts(t *testing.T) {
	s, _, wl := newTest()
	ctx := context.Background()
	_, _ = s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionSuspended})
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionReadOnly}); err != nil {
		t.Fatal(err)
	}
	if len(wl.started) != 1 {
		t.Fatal("moving to read-only must start what the suspension stopped")
	}
}

func TestSetRefusals(t *testing.T) {
	s, _, _ := newTest()
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	for name, tt := range map[string]struct {
		id   uint
		in   SetInput
		want error
	}{
		"bad state":     {1, SetInput{State: "frozen"}, ErrInvalidState},
		"until in past": {1, SetInput{State: models.WorkspaceRestrictionReadOnly, Until: &past}, ErrUntilPast},
		"system":        {2, SetInput{State: models.WorkspaceRestrictionReadOnly}, ErrSystem},
	} {
		if _, err := s.Set(ctx, tt.id, 5, tt.in); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", name, err, tt.want)
		}
	}
}

func TestExpireDueAndCache(t *testing.T) {
	s, repo, _ := newTest()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	until := clock.Add(time.Hour)
	if _, err := s.Set(context.Background(), 1, 5, SetInput{State: models.WorkspaceRestrictionReadOnly, Until: &until}); err != nil {
		t.Fatal(err)
	}
	_ = s.Allowed(1, ActionWrite)
	reads := repo.reads
	_ = s.Allowed(1, ActionWrite)
	if repo.reads != reads {
		t.Fatal("a second check within the cache TTL must not read the database")
	}
	clock = until.Add(time.Minute)
	if err := s.ExpireDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Allowed(1, ActionWrite); err != nil {
		t.Fatalf("restriction past its end date still applies: %v", err)
	}
}

func TestChangeNotifies(t *testing.T) {
	s, _, _ := newTest()
	var got []uint
	s.SetOnChange(func(_ context.Context, ws uint) { got = append(got, ws) })
	ctx := context.Background()
	if _, err := s.Set(ctx, 1, 5, SetInput{State: models.WorkspaceRestrictionSuspended}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lift(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Fatalf("notified %v, want the workspace twice (set, lift)", got)
	}
}
