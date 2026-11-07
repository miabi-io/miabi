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

package worker

import (
	"context"
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// orderLog records the side effects whose order decides whether a rolling deploy drops traffic.
type orderLog struct{ steps []string }

type stopFake struct {
	docker.Client
	log *orderLog
}

func (f stopFake) StopContainer(_ context.Context, id string, _ int) error {
	f.log.steps = append(f.log.steps, "stop "+id)
	return nil
}

func (f stopFake) RemoveContainer(context.Context, string, bool) error { return nil }

type stopClients struct{ c docker.Client }

func (s stopClients) For(uint) (docker.Client, error) { return s.c, nil }
func (stopClients) LocalID() uint                     { return 0 }

type routeFake struct {
	log      *orderLog
	releases *repositories.ReleaseRepository
}

func (r routeFake) SyncRoute(_ context.Context, appID uint) error {
	active, err := r.releases.FindActive(appID)
	if err != nil {
		return err
	}
	r.log.steps = append(r.log.steps, "sync "+active.ContainerID)
	return nil
}

func newSwapHarness(t *testing.T) (*DeployHandler, *orderLog, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tables := []any{&models.Application{}, &models.AppEnvVar{}, &models.Network{}, &models.AppPort{},
		&models.Stack{}, &models.Deployment{}, &models.Release{}}
	seen := map[*schema.Schema]bool{}
	for _, m := range tables {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(m); err != nil {
			t.Fatal(err)
		}
		dropUIDDefault(stmt.Schema, seen)
	}
	if err := db.AutoMigrate(tables...); err != nil {
		t.Fatal(err)
	}
	prevDelay := routeDrainDelay
	routeDrainDelay = 0
	t.Cleanup(func() { routeDrainDelay = prevDelay })

	log := &orderLog{}
	releases := repositories.NewReleaseRepository(db)
	h := &DeployHandler{
		apps:        repositories.NewApplicationRepository(db),
		deployments: repositories.NewDeploymentRepository(db),
		releases:    releases,
		clients:     stopClients{stopFake{log: log}},
		routes:      routeFake{log: log, releases: releases},
		bus:         eventbus.New(),
	}
	return h, log, db
}

// SQLite can't parse the Postgres-only gen_random_uuid() default; BeforeCreate sets uid anyway.
func dropUIDDefault(s *schema.Schema, seen map[*schema.Schema]bool) {
	if s == nil || seen[s] {
		return
	}
	seen[s] = true
	if f := s.LookUpField("uid"); f != nil {
		f.HasDefaultValue, f.DefaultValue, f.DefaultValueInterface = false, "", nil
	}
	for _, rel := range s.Relationships.Relations {
		dropUIDDefault(rel.FieldSchema, seen)
		if rel.JoinTable != nil {
			dropUIDDefault(rel.JoinTable, seen)
		}
	}
}

// Otherwise the gateway dials a stopped upstream and answers 503 until the route sync lands.
func TestSwapSyncsRouteBeforeRetiringPrevious(t *testing.T) {
	h, log, db := newSwapHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1"}
	db.Create(app)
	db.Create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "old", Active: true})
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	db.Create(dep)

	h.swapAndRelease(app, dep, "nginx:2", "new")

	want := []string{"sync new", "stop old"}
	if !reflect.DeepEqual(log.steps, want) {
		t.Fatalf("steps = %v, want %v", log.steps, want)
	}
	if active, _ := h.releases.FindActive(app.ID); active == nil || active.ContainerID != "new" {
		t.Fatalf("active release = %+v, want the new container", active)
	}
}

// A superseded canary also keeps serving its share until the route drops the split.
func TestSwapRetiresCanaryAfterRouteSync(t *testing.T) {
	h, log, db := newSwapHarness(t)
	app := &models.Application{Name: "web", Image: "nginx", Tag: "1"}
	db.Create(app)
	db.Create(&models.Release{ApplicationID: app.ID, Version: 1, ContainerID: "stable", Active: true})
	canaryDep := &models.Deployment{ApplicationID: app.ID, Status: models.DeploymentCanary}
	db.Create(canaryDep)
	canary := &models.Release{ApplicationID: app.ID, DeploymentID: canaryDep.ID, Version: 2, ContainerID: "canary"}
	db.Create(canary)
	if err := h.apps.SetCanary(app.ID, &canary.ID, 10); err != nil {
		t.Fatal(err)
	}
	dep := &models.Deployment{ApplicationID: app.ID, Strategy: models.DeployRolling}
	db.Create(dep)

	h.swapAndRelease(app, dep, "nginx:3", "new")

	want := []string{"sync new", "stop canary", "stop stable"}
	if !reflect.DeepEqual(log.steps, want) {
		t.Fatalf("steps = %v, want %v", log.steps, want)
	}
	if cur, _ := h.apps.FindByID(app.ID); cur.CanaryReleaseID != nil {
		t.Fatal("canary split should be cleared")
	}
}
