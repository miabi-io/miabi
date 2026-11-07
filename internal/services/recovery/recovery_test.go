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

package recovery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// An empty store is all Reconcile needs for an empty report, the case that broke.
type fakeSettings struct{}

func (fakeSettings) Get(string) (*models.Setting, error) { return nil, errors.New("not found") }
func (fakeSettings) BulkUpsert([]models.Setting) error   { return nil }
func (fakeSettings) Delete(string) error                 { return nil }

// fakeServers has no nodes.
type fakeServers struct{}

func (fakeServers) List() ([]models.Server, error) { return nil, nil }
func (fakeServers) Update(*models.Server) error    { return nil }

// A clean reconcile rendered a blank page because nil lists encoded as null; they must encode as [].
func TestReconcileReportNeverEncodesNullLists(t *testing.T) {
	s := New(fakeSettings{}, fakeServers{}, nil, nil)

	rep, err := s.Reconcile(t.Context())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rep.Unrecoverable == nil || rep.Failures == nil || rep.Manual == nil {
		t.Fatalf("Reconcile returned nil lists: %+v", rep)
	}

	body, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"unrecoverable", "manual", "failures"} {
		if strings.Contains(string(body), `"`+field+`":null`) {
			t.Errorf("%q encoded as null; a client reading .length on it crashes:\n%s", field, body)
		}
	}
}

// Status is served on every page load, so it must be safe with no marker set.
func TestStatusWithNoRecoveryInProgress(t *testing.T) {
	s := New(fakeSettings{}, fakeServers{}, nil, nil)
	if s.Pending() {
		t.Error("Pending() is true with no marker stored")
	}
	if st := s.Status(); st.Pending {
		t.Errorf("Status() reports pending: %+v", st)
	}
}
