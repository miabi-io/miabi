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

package errorhandlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
)

func invoke(t *testing.T, code int, message string, err error) dto.ErrorInfo {
	t.Helper()
	o := okapi.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c := okapi.NewContext(o, rec, req)
	if herr := CustomErrorHandler()(c, code, message, err); herr != nil {
		t.Fatalf("handler returned error: %v", herr)
	}
	var body dto.ErrorResponseBody
	if derr := json.Unmarshal(rec.Body.Bytes(), &body); derr != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), derr)
	}
	if body.Success {
		t.Fatalf("expected success=false, got true")
	}
	if body.Error == nil {
		t.Fatalf("expected error info, got nil")
	}
	return *body.Error
}

func TestErrorHandler_PromotesErrWhenMessageGeneric(t *testing.T) {
	const reason = "this is the Miabi control-plane/agent container and cannot be modified from the containers list"
	got := invoke(t, http.StatusConflict, http.StatusText(http.StatusConflict), errors.New(reason))
	if got.StatusCode != http.StatusConflict {
		t.Errorf("status_code = %d, want %d", got.StatusCode, http.StatusConflict)
	}
	if got.Code != "CONFLICT" {
		t.Errorf("code = %q, want CONFLICT", got.Code)
	}
	if got.Message != reason {
		t.Errorf("message = %q, want the reason", got.Message)
	}
	if got.Error != reason {
		t.Errorf("error = %q, want the reason", got.Error)
	}
}

func TestErrorHandler_KeepsSpecificMessage(t *testing.T) {
	got := invoke(t, http.StatusInternalServerError, "failed to list containers", errors.New("dial unix /var/run/docker.sock: connect: no such file"))
	if got.Message != "failed to list containers" {
		t.Errorf("message = %q, want the specific message", got.Message)
	}
	if got.Error == "" || got.Error == got.Message {
		t.Errorf("error = %q, want the underlying detail", got.Error)
	}
}

type codedMsgErr struct{}

func (codedMsgErr) Error() string {
	return "pq: duplicate key value violates unique constraint \"uniq_slug\""
}
func (codedMsgErr) Code() string    { return "CONFLICT" }
func (codedMsgErr) Message() string { return "a workspace with that slug already exists" }

func TestErrorHandler_PrefersDomainMessage(t *testing.T) {
	got := invoke(t, http.StatusConflict, http.StatusText(http.StatusConflict), codedMsgErr{})
	if got.Code != "CONFLICT" {
		t.Errorf("code = %q, want CONFLICT", got.Code)
	}
	if got.Message != "a workspace with that slug already exists" {
		t.Errorf("message = %q, want the curated message", got.Message)
	}
	if got.Error == "" || got.Error == got.Message {
		t.Errorf("error = %q, want the raw Error() detail", got.Error)
	}
}

func TestErrorHandler_NoErrFallsBackToStatusText(t *testing.T) {
	got := invoke(t, http.StatusNotFound, "", nil)
	if got.Message != http.StatusText(http.StatusNotFound) {
		t.Errorf("message = %q, want %q", got.Message, http.StatusText(http.StatusNotFound))
	}
	if got.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", got.Code)
	}
}
