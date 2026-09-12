// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"slices"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type externalClusterFixture struct {
	ID                   uint `gorm:"primaryKey"`
	Name                 string
	IsDefault            bool
	Mode                 string
	Visibility           string
	ExternalBaseDomain   string
	ExternalCertProvider string
}

func (externalClusterFixture) TableName() string { return "clusters" }

func newExternalAccessDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&clusterRow{}, &externalClusterFixture{}, &serverFixture{}, &clusterSettingFixture{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestExternalAccessMovesOntoTheDefaultCluster(t *testing.T) {
	db := newExternalAccessDB(t)
	for _, row := range []any{
		&externalClusterFixture{ID: 1, Name: "default", IsDefault: true, Mode: "standalone", Visibility: "all"},
		&externalClusterFixture{ID: 2, Name: "paris", Mode: "standalone", Visibility: "all"},
		&clusterSettingFixture{Key: "external_base_domain", Value: " *.Apps.Example.com "},
		&clusterSettingFixture{Key: "external_base_provider", Value: "letsencrypt"},
		&clusterSettingFixture{Key: "maintenance_mode", Value: "false"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := externalAccessStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}

	var clusters []externalClusterFixture
	if err := db.Order("id").Find(&clusters).Error; err != nil {
		t.Fatal(err)
	}
	if def := clusters[0]; def.ExternalBaseDomain != "apps.example.com" || def.ExternalCertProvider != "letsencrypt" {
		t.Errorf("default cluster = %+v, want apps.example.com with letsencrypt", def)
	}
	if other := clusters[1]; other.ExternalBaseDomain != "" || other.ExternalCertProvider != "" {
		t.Errorf("another cluster took the platform domain: %+v", other)
	}
	var keys []string
	if err := db.Model(&clusterSettingFixture{}).Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"maintenance_mode"}) {
		t.Errorf("settings left = %v, want only maintenance_mode", keys)
	}

	if err := db.Create(&clusterSettingFixture{Key: "external_base_domain", Value: "other.example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := externalAccessStep(context.Background(), db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var def externalClusterFixture
	if err := db.First(&def, 1).Error; err != nil {
		t.Fatal(err)
	}
	if def.ExternalBaseDomain != "apps.example.com" {
		t.Errorf("re-run replaced the cluster's domain with %q", def.ExternalBaseDomain)
	}
}

func TestExternalAccessStepWithNothingConfigured(t *testing.T) {
	db := newExternalAccessDB(t)
	if err := db.Create(&clusterSettingFixture{Key: "external_base_domain", Value: ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := externalAccessStep(context.Background(), db); err != nil {
		t.Fatalf("step: %v", err)
	}
	var clusters, settings int64
	db.Model(&externalClusterFixture{}).Count(&clusters)
	db.Model(&clusterSettingFixture{}).Count(&settings)
	if clusters != 0 || settings != 0 {
		t.Errorf("clusters = %d, settings = %d; want no cluster created and the empty setting removed", clusters, settings)
	}
}
