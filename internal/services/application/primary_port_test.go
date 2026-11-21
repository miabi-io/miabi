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

package application

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

func TestPrimaryPort(t *testing.T) {
	ports := []models.AppPort{{ContainerPort: 8080}, {ContainerPort: 9090}, {ContainerPort: 9090, Protocol: "udp"}}
	cases := []struct {
		name            string
		marked, current int
		want            int
	}{
		{"marked wins", 9090, 8080, 9090},
		{"current kept", 0, 9090, 9090},
		{"current removed", 0, 7000, 8080},
		{"new app", 0, 0, 8080},
	}
	for _, c := range cases {
		if got := PrimaryPort(ports, c.marked, c.current); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got := PrimaryPort(nil, 0, 8080); got != 0 {
		t.Errorf("no ports: got %d", got)
	}
}

func TestPrimaryPortErrors(t *testing.T) {
	s, db := managedEnvService(t)
	db.Exec(`DROP TABLE app_ports`)
	if err := db.AutoMigrate(&models.AppPort{}); err != nil {
		t.Fatal(err)
	}
	s.ports = repositories.NewAppPortRepository(db)
	db.Exec(`INSERT INTO app_ports (application_id, container_port, protocol, scheme) VALUES (1, 8080, 'tcp', 'http'), (1, 9090, 'tcp', 'http')`)
	app := &models.Application{ID: 1, Port: 8080}

	if err := s.SetPorts(app, []PortSpec{{ContainerPort: 1, Primary: true}, {ContainerPort: 2, Primary: true}}); !errors.Is(err, ErrPrimaryPorts) {
		t.Errorf("two primaries: %v", err)
	}
	if err := s.SetPrimaryPort(app, 5000); !errors.Is(err, ErrPrimaryUndeclared) {
		t.Errorf("undeclared primary: %v", err)
	}
	if err := s.SetPrimaryPort(app, 8080); err != nil {
		t.Errorf("unchanged primary: %v", err)
	}
}
