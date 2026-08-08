// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// fakeSettings is an empty settings store — all Reconcile needs to produce a
// report with nothing in it, which is the case that broke.
type fakeSettings struct{}

func (fakeSettings) Get(string) (*models.Setting, error) { return nil, errors.New("not found") }
func (fakeSettings) BulkUpsert([]models.Setting) error   { return nil }
func (fakeSettings) Delete(string) error                 { return nil }

// fakeServers has no nodes.
type fakeServers struct{}

func (fakeServers) List() ([]models.Server, error) { return nil, nil }
func (fakeServers) Update(*models.Server) error    { return nil }

// A nil Go slice marshals to JSON null, and the admin page reads report.failures.length — so a CLEAN
// reconcile, the case that should be unremarkable, rendered a blank page. "Nothing went wrong" encodes as [],
// not null, and saying so is the API's job rather than every consumer's.
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
