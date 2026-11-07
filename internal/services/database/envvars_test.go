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

package database

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type nopSecrets struct{}

func (nopSecrets) UpsertOwned(uint, string, uint, string, string, string) (*models.Secret, error) {
	return nil, nil
}
func (nopSecrets) DeleteOwned(uint, string, uint) ([]models.Application, error) { return nil, nil }

func TestEnvKeysDefaultsPrefixAndMap(t *testing.T) {
	cases := []struct {
		name   string
		engine models.DBEngine
		prefix string
		m      map[string]string
		want   []string
	}{
		{"sql defaults", models.DBEnginePostgres, "", nil,
			[]string{"DB_URL", "DATABASE_URL", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD"}},
		{"prefix", models.DBEngineMySQL, "ANALYTICS", nil,
			[]string{"ANALYTICS_DB_URL", "ANALYTICS_DATABASE_URL", "ANALYTICS_DB_HOST", "ANALYTICS_DB_PORT", "ANALYTICS_DB_NAME", "ANALYTICS_DB_USER", "ANALYTICS_DB_PASSWORD"}},
		{"mapped and skipped", models.DBEnginePostgres, "X",
			map[string]string{EnvFieldURL: "SPRING_DATASOURCE_URL", EnvFieldLegacy: "", EnvFieldHost: ""},
			[]string{"SPRING_DATASOURCE_URL", "X_DB_PORT", "X_DB_NAME", "X_DB_USER", "X_DB_PASSWORD"}},
		{"redis defaults", models.DBEngineRedis, "", nil,
			[]string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD"}},
	}
	for _, c := range cases {
		if got := EnvKeys(c.engine, c.prefix, c.m); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeEnvMapRejects(t *testing.T) {
	cases := map[string]struct {
		engine models.DBEngine
		m      map[string]string
	}{
		"field not on engine": {models.DBEngineRedis, map[string]string{EnvFieldUser: "REDIS_USER"}},
		"unknown field":       {models.DBEnginePostgres, map[string]string{"dsn": "DSN"}},
		"invalid name":        {models.DBEnginePostgres, map[string]string{EnvFieldURL: "1-BAD"}},
		"duplicate name":      {models.DBEnginePostgres, map[string]string{EnvFieldURL: "DB_HOST"}},
	}
	for name, c := range cases {
		if _, err := NormalizeEnvMap(c.engine, "", c.m); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := NormalizeEnvMap(models.DBEnginePostgres, "", map[string]string{EnvFieldURL: "JDBC_URL"}); err != nil {
		t.Errorf("valid map refused: %v", err)
	}
}

func TestAppEnvUsesSecretRefs(t *testing.T) {
	s := &Service{secrets: nopSecrets{}}
	inst := &models.DatabaseInstance{Name: "main", Engine: models.DBEnginePostgres, Host: "mb-db-1", Port: 5432}
	d := &models.Database{Name: "shop", Username: "shop_u", PasswordEnc: mustEncrypt(t, "pw")}
	vars, err := s.AppEnv(inst, d, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vars {
		got[v.Key] = v.Value
	}
	ref := "${{ secrets.db_main_shop_url }}"
	if got["DB_URL"] != ref || got["DATABASE_URL"] != ref {
		t.Errorf("url vars = %q / %q, want %q", got["DB_URL"], got["DATABASE_URL"], ref)
	}
	if got["DB_PASSWORD"] != "${{ secrets.db_main_shop_password }}" || got["DB_HOST"] != "mb-db-1" || got["DB_PORT"] != "5432" {
		t.Errorf("vars = %v", got)
	}
}

func TestAppEnvRedisInstance(t *testing.T) {
	s := &Service{}
	inst := &models.DatabaseInstance{Name: "cache", Engine: models.DBEngineRedis, Host: "mb-redis", Port: 6379, AdminPasswordEnc: mustEncrypt(t, "pw")}
	vars, err := s.AppEnv(inst, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, v := range vars {
		keys = append(keys, v.Key)
		if v.Key == "REDIS_URL" && (v.Value != "redis://:pw@mb-redis:6379" || !v.Secret) {
			t.Errorf("REDIS_URL = %+v", v)
		}
	}
	if !slices.Equal(keys, []string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD"}) {
		t.Errorf("keys = %v", keys)
	}
}

type linkInstRow struct {
	ID               uint `gorm:"primaryKey"`
	WorkspaceID      uint
	Name             string
	Engine           string
	Status           string
	Host             string
	Port             int
	AdminUser        string
	AdminPasswordEnc string
	CreatedAt        time.Time
}

func (linkInstRow) TableName() string { return "database_instances" }

type linkDBRow struct {
	ID            uint `gorm:"primaryKey"`
	WorkspaceID   uint
	InstanceID    uint
	Name          string
	Username      string
	PasswordEnc   string
	ApplicationID *uint
	EnvPrefix     string
	EnvMap        map[string]string `gorm:"serializer:json"`
	EnvVars       []string          `gorm:"serializer:json"`
	CreatedAt     time.Time
}

func (linkDBRow) TableName() string { return "databases" }

type linkRow struct {
	ID            uint `gorm:"primaryKey"`
	WorkspaceID   uint
	InstanceID    uint
	ApplicationID uint
	EnvPrefix     string
	EnvMap        map[string]string `gorm:"serializer:json"`
	EnvVars       []string          `gorm:"serializer:json"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (linkRow) TableName() string { return "database_instance_links" }

func newLinkSvc(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&linkInstRow{}, &linkDBRow{}, &linkRow{}, &instNetRow{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repositories.NewDatabaseRepository(db)}, db
}

// A SQL instance can't be linked directly, and a second link may not reuse an unprefixed SQL database's var names.
func TestLinkInstanceAndEnvConflicts(t *testing.T) {
	s, db := newLinkSvc(t)
	app := uint(7)
	db.Create(&[]linkInstRow{
		{ID: 1, WorkspaceID: 1, Name: "cache", Engine: "redis", Host: "r", Port: 6379},
		{ID: 2, WorkspaceID: 1, Name: "main", Engine: "postgres", Host: "p", Port: 5432},
	})
	db.Create(&linkDBRow{ID: 10, WorkspaceID: 1, InstanceID: 2, Name: "shop", ApplicationID: &app})

	if _, err := s.LinkInstance(1, 2, app, EnvLink{}); !errors.Is(err, ErrLinkLogicalDatabase) {
		t.Fatalf("postgres instance link: err = %v", err)
	}

	env, err := s.ResolveEnvLink(1, app, models.DBEngineRedis, "", nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []uint{app, 8} {
		if _, err := s.LinkInstance(1, 1, other, env); err != nil {
			t.Fatalf("link app %d: %v", other, err)
		}
	}
	linked, _ := s.ListByApp(1, app)
	if len(linked) != 2 || linked[1].Kind != LinkKindInstance || !slices.Contains(linked[1].EnvVars, "REDIS_URL") {
		t.Fatalf("ListByApp = %+v", linked)
	}
	if !slices.Contains(linked[0].EnvVars, "DB_URL") {
		t.Errorf("legacy row should report default keys, got %v", linked[0].EnvVars)
	}

	_, err = s.ResolveEnvLink(1, app, models.DBEnginePostgres, "", map[string]string{EnvFieldURL: "REDIS_URL"}, 0, 0)
	if !errors.Is(err, ErrEnvConflict) {
		t.Errorf("conflict: err = %v", err)
	}
	if _, err := s.ResolveEnvLink(1, app, models.DBEnginePostgres, "", nil, 10, 0); err != nil {
		t.Errorf("re-attaching the same database must not conflict with itself: %v", err)
	}

	if err := s.Delete(context.Background(), &models.DatabaseInstance{ID: 1, Engine: models.DBEngineRedis, Status: models.DBStatusStopped}); !errors.Is(err, ErrInstanceLinked) {
		t.Errorf("delete linked instance: err = %v", err)
	}
	if _, err := s.UnlinkInstance(1, 1, app); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstanceConnectionForApp(1, app, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("connection after unlink: err = %v", err)
	}
}
