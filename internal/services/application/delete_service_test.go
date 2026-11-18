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
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

type removeFake struct {
	docker.Client
	removed *[]string
	err     error
}

func (f removeFake) ServiceRemove(_ context.Context, name string) error {
	*f.removed = append(*f.removed, name)
	return f.err
}

type removeClients struct {
	NodeDocker
	c docker.Client
}

func (r removeClients) For(uint) (docker.Client, error) { return r.c, nil }

func TestRemoveServiceUsesTheAliasEvenWithoutARelease(t *testing.T) {
	var removed []string
	s := &Service{clients: removeClients{c: removeFake{removed: &removed}}}
	app := &models.Application{ID: 72, Name: "crash-svc", RuntimeKind: models.RuntimeService}

	s.removeService(context.Background(), app)

	if len(removed) != 1 || removed[0] != node.AppAlias(app) {
		t.Fatalf("removed %v, want [%s]", removed, node.AppAlias(app))
	}
}

func TestRemoveServiceToleratesAnAlreadyRemovedService(t *testing.T) {
	var removed []string
	s := &Service{clients: removeClients{c: removeFake{removed: &removed, err: docker.ErrNotFound}}}
	s.removeService(context.Background(), &models.Application{ID: 1, RuntimeKind: models.RuntimeService})
	if len(removed) != 1 {
		t.Fatalf("removed %v", removed)
	}
}
