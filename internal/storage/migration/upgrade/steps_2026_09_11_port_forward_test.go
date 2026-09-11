// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type bindingFixture struct {
	ID      uint `gorm:"primaryKey"`
	Managed bool
	BindIP  string
}

func (bindingFixture) TableName() string { return "port_bindings" }

func TestPortForwardStep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&clusterRow{}, &serverFixture{}, &bindingFixture{}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Create(&[]clusterRow{
		{ID: 1, Name: "default", IsDefault: true, Mode: "swarm", Visibility: "all"},
		{ID: 2, Name: "lyon", Mode: "standalone", ManagerServerID: 12, Visibility: "all"},
	}).Error)
	must(db.Create(&[]serverFixture{
		{ID: 10, Name: "manager", IsLocal: true, Connectivity: "port-forward", ClusterID: 1},
		{ID: 11, Name: "worker", Connectivity: "port-forward", ClusterID: 1},
		{ID: 12, Name: "lyon", Connectivity: "port-forward", ClusterID: 2},
		{ID: 13, Name: "paris", Connectivity: "edge-gateway", ClusterID: 1},
	}).Error)
	must(db.Create(&[]bindingFixture{{ID: 1, Managed: true, BindIP: "10.0.0.12"}, {ID: 2}}).Error)

	for run := 0; run < 2; run++ {
		must(portForwardStep(context.Background(), db))
	}

	want := map[uint]string{10: "cluster", 11: "cluster", 12: "edge-gateway", 13: "edge-gateway"}
	var servers []serverFixture
	must(db.Find(&servers).Error)
	for _, s := range servers {
		if s.Connectivity != want[s.ID] {
			t.Errorf("node %s connectivity = %q, want %q", s.Name, s.Connectivity, want[s.ID])
		}
	}

	var lyon clusterRow
	must(db.First(&lyon, 2).Error)
	if !lyon.LegacyIngress || lyon.IngressServerID != 12 {
		t.Errorf("converted cluster = legacy %v, ingress node %d; want flagged and served by node 12", lyon.LegacyIngress, lyon.IngressServerID)
	}
	var def clusterRow
	must(db.First(&def, 1).Error)
	if def.LegacyIngress {
		t.Error("the default cluster was flagged for conversion")
	}

	var n int64
	must(db.Table("port_bindings").Count(&n).Error)
	if n != 1 {
		t.Errorf("port bindings = %d, want only the user-requested one left", n)
	}
	for _, col := range []string{"managed", "bind_ip"} {
		if db.Migrator().HasColumn(&portBindingRow{}, col) {
			t.Errorf("port_bindings.%s was not dropped", col)
		}
	}
}
