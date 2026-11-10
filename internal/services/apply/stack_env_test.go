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

package apply

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func stackEnvService(t *testing.T) *Service {
	t.Helper()
	crypto.Init("test-master-key-for-stack-env")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Created by hand: the uid column's Postgres-only gen_random_uuid() default is not valid SQLite.
	if err := db.Exec(`CREATE TABLE stacks (id INTEGER PRIMARY KEY, uid TEXT, workspace_id INTEGER, name TEXT,
		display_name TEXT, docker_name TEXT, cluster_id INTEGER, docker_network TEXT, description TEXT,
		metadata TEXT, annotations TEXT, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	// Only what preloading a stack's members reads; the stack has none.
	if err := db.Exec(`CREATE TABLE applications (id INTEGER PRIMARY KEY, stack_id INTEGER, created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.StackEnvVar{}); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO stacks (id, workspace_id, name, docker_name) VALUES (7, 1, 'shop', 'mb-shop')`)
	return &Service{stacks: stack.NewService(repositories.NewStackRepository(db), nil,
		repositories.NewStackEnvVarRepository(db), nil, nil, nil, nil, nil)}
}

func TestReconcileStackEnv(t *testing.T) {
	s := stackEnvService(t)
	set := func(env map[string]string, secret ...string) bool {
		t.Helper()
		changed, err := s.reconcileStackEnv(1, 7, &d.StackSpec{Env: env, SecretEnv: secret})
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}

	if !set(map[string]string{"DB_HOST": "db", "API_KEY": "k1", "OLD": "x"}, "API_KEY") {
		t.Error("a first env reported no change")
	}
	// Unchanged values must not redeploy the members on every sync.
	if set(map[string]string{"DB_HOST": "db", "API_KEY": "k1", "OLD": "x"}, "API_KEY") {
		t.Error("an unchanged env reported a change")
	}
	env, secret := s.stackEnvOf(1, 7)
	if env["DB_HOST"] != "db" || env["OLD"] != "x" || len(secret) != 1 || secret[0] != "API_KEY" {
		t.Fatalf("after create: env %v, secret %v", env, secret)
	}
	if v, err := s.stacks.RevealEnvVar(1, 7, "API_KEY"); err != nil || v != "k1" {
		t.Fatalf("API_KEY = %q, %v; want it stored encrypted as k1", v, err)
	}

	if !set(map[string]string{"DB_HOST": "db2", "API_KEY": "k2"}, "API_KEY") {
		t.Error("changed and removed variables reported no change")
	}
	env, _ = s.stackEnvOf(1, 7)
	if env["DB_HOST"] != "db2" {
		t.Errorf("DB_HOST = %q, want db2", env["DB_HOST"])
	}
	if _, ok := env["OLD"]; ok {
		t.Error("OLD survived a manifest that no longer declares it")
	}
	if v, _ := s.stacks.RevealEnvVar(1, 7, "API_KEY"); v != "k2" {
		t.Errorf("API_KEY = %q, want k2", v)
	}

	// A manifest silent about env leaves the stack's variables alone.
	if changed, err := s.reconcileStackEnv(1, 7, &d.StackSpec{}); err != nil || changed {
		t.Fatalf("omitted env: changed=%v, err=%v", changed, err)
	}
	if env, _ = s.stackEnvOf(1, 7); len(env) != 2 {
		t.Errorf("omitted env changed the stack: %v", env)
	}

	set(map[string]string{})
	if env, _ = s.stackEnvOf(1, 7); len(env) != 0 {
		t.Errorf("empty env left %v", env)
	}
}

// An app this apply already deployed has the new values, and one reached by both a stack and a secret rolls once.
func TestToRedeployDeploysEachAppOnce(t *testing.T) {
	deployed, seen := map[uint]bool{2: true}, map[uint]bool{}
	members := []models.Application{{ID: 1, Name: "web"}, {ID: 2, Name: "worker"}, {ID: 3, Name: "cron"}}
	consumers := []models.Application{{ID: 3, Name: "cron"}, {ID: 4, Name: "api"}}
	var got []string
	for _, a := range toRedeploy(members, deployed, seen) {
		got = append(got, a.Name)
	}
	for _, a := range toRedeploy(consumers, deployed, seen) {
		got = append(got, a.Name)
	}
	if strings.Join(got, ",") != "web,cron,api" {
		t.Errorf("redeploy %v, want [web cron api]", got)
	}
}

const stackEnvBundle = `
apiVersion: miabi.io/v1
kind: Database
metadata: { name: shop-db }
spec: { engine: postgres }
---
apiVersion: miabi.io/v1
kind: Stack
metadata: { name: shop }
spec:
  env:
    DB_HOST: "{{ .databases.shop-db.host }}"
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: web }
spec: { image: web, stack: shop }
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: worker }
spec: { image: web, stack: shop }
`

// With the connection declared on the stack, no app's env names the database: its first member owns the link.
func TestStackEnvDatabaseConsumer(t *testing.T) {
	set, err := d.Parse([]byte(stackEnvBundle))
	if err != nil {
		t.Fatal(err)
	}
	if got := rawConsumerApp(set.All(), "shop-db"); got != "web" {
		t.Errorf("consumer = %q, want web", got)
	}
}

// A member receives its stack's env, so what that env addresses counts when placing the member.
func TestStackEnvRefsReachMembers(t *testing.T) {
	set, err := d.Parse([]byte(stackEnvBundle))
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := set.Get(string(d.KindApplication) + "/worker")
	if refs := refsOf(worker.Application).withStackRefs(worker.Application, set); !refs.dbs["shop-db"] {
		t.Errorf("refs = %+v, want shop-db through the stack", refs)
	}
}
