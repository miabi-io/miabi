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

package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type fakeVolumes []*models.Volume

func (f fakeVolumes) NodeLocalOf(*models.Application) []*models.Volume { return f }

func TestJobNode(t *testing.T) {
	service := &models.Application{RuntimeKind: models.RuntimeService, ServerID: 1}
	container := &models.Application{RuntimeKind: models.RuntimeContainer, ServerID: 1}
	onNode3 := &models.Volume{Name: "pgdata", ServerID: 3}
	cases := []struct {
		name string
		app  *models.Application
		vols fakeVolumes
		want uint
	}{
		{"service follows its volume", service, fakeVolumes{onNode3, {Name: "uploads", ServerID: 3}}, 3},
		{"service without node-local volumes stays on its node", service, nil, 1},
		{"container keeps its node", container, fakeVolumes{onNode3}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{}
			s.SetVolumes(tc.vols, nil)
			got, err := s.jobNode(tc.app)
			if err != nil || got != tc.want {
				t.Fatalf("jobNode = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestJobNodeRefusesVolumesOnDifferentNodes(t *testing.T) {
	s := &Service{}
	s.SetVolumes(fakeVolumes{{Name: "pgdata", ServerID: 3}, {Name: "uploads", ServerID: 4}}, func(id uint) string {
		return map[uint]string{3: "safari-01", 4: "safari-02"}[id]
	})
	_, err := s.jobNode(&models.Application{RuntimeKind: models.RuntimeService, ServerID: 1})
	if !errors.Is(err, ErrVolumesSplit) {
		t.Fatalf("err = %v, want ErrVolumesSplit", err)
	}
	if !strings.Contains(err.Error(), `"pgdata" on safari-01, "uploads" on safari-02`) {
		t.Fatalf("message does not name the volumes and nodes: %v", err)
	}
}

func TestJobNodeWithoutVolumeLookupKeepsTheAppNode(t *testing.T) {
	got, err := (&Service{}).jobNode(&models.Application{RuntimeKind: models.RuntimeService, ServerID: 1})
	if err != nil || got != 1 {
		t.Fatalf("jobNode = %d, %v", got, err)
	}
}
