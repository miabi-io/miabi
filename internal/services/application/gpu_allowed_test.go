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

	"github.com/miabi-io/miabi/internal/services/quota"
)

func TestGPUAllowedRefusesTheRestrictedProfile(t *testing.T) {
	free := &Service{quota: &quota.Service{}}
	if err := free.gpuAllowed(1, 1); err != nil {
		t.Errorf("enforcement off should allow a GPU request, got %v", err)
	}

	restrictedQuota := &quota.Service{}
	restrictedQuota.SetForceNonRoot(true)
	restricted := &Service{quota: restrictedQuota}
	if err := restricted.gpuAllowed(1, 1); !errors.Is(err, ErrGPURestricted) {
		t.Errorf("a restricted workspace must not save a GPU app, got %v", err)
	}
	if err := restricted.gpuAllowed(1, 0); err != nil {
		t.Errorf("no GPU request is never gated, got %v", err)
	}
}
