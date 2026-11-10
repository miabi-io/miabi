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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// A manifest that names no ports must not erase names set in the console.
func TestKeepPortNames(t *testing.T) {
	live := []models.AppPort{
		{ContainerPort: 8080, Protocol: "tcp", Name: "http"},
		{ContainerPort: 53, Protocol: "udp", Name: "dns"},
	}
	got := KeepPortNames([]PortSpec{
		{ContainerPort: 8080},
		{ContainerPort: 53, Protocol: "tcp"},
		{ContainerPort: 53, Protocol: "udp", Name: "resolver"},
		{ContainerPort: 9090},
	}, live)
	want := []string{"http", "", "resolver", ""}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("port %d/%s name = %q, want %q", got[i].ContainerPort, got[i].Protocol, got[i].Name, w)
		}
	}
}
