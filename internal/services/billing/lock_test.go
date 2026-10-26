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

package billing

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestGuardServiceUser(t *testing.T) {
	svcUser := &models.User{Email: ServiceUserEmail}
	person := &models.User{Email: "admin@example.com"}

	s := &Service{}
	if err := s.GuardServiceUser(svcUser); err != nil {
		t.Errorf("unlocked: %v, want the service user unguarded", err)
	}
	s.SetLocked(true)
	if err := s.GuardServiceUser(svcUser); !errors.Is(err, ErrConnectionLocked) {
		t.Errorf("locked: %v, want ErrConnectionLocked", err)
	}
	if err := s.GuardServiceUser(person); err != nil {
		t.Errorf("locked, other user: %v, want nil", err)
	}
}

func TestGuardWorkspaceDeletion(t *testing.T) {
	acct := uint(3)
	billed := &models.Workspace{BillingAccountID: &acct}
	unbilled := &models.Workspace{}

	s := &Service{}
	if err := s.GuardWorkspaceDeletion(billed); err != nil {
		t.Errorf("not console mode: %v, want nil", err)
	}
	s.SetConsoleOnly(func() bool { return true })
	if err := s.GuardWorkspaceDeletion(billed); !errors.Is(err, ErrWorkspaceManagedByConsole) {
		t.Errorf("billed in console mode: %v, want ErrWorkspaceManagedByConsole", err)
	}
	if err := s.GuardWorkspaceDeletion(unbilled); err != nil {
		t.Errorf("unbilled in console mode: %v, want nil", err)
	}
}
