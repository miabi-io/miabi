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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

func TestSealEnv(t *testing.T) {
	crypto.Init("test-master-key-for-job-env")
	t.Cleanup(func() { crypto.Init("") })
	s := &Service{}

	sealed, err := s.SealEnv(1, models.JobEnv{{Key: "MODE", Value: "nightly"}, {Key: "TOKEN", Value: "s3cret", IsSecret: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := sealed.Lookup("TOKEN")
	if tok.Value == "s3cret" || !crypto.IsEncrypted(tok.Value) {
		t.Fatalf("a secret must be stored encrypted: %+v", tok)
	}

	again, err := s.SealEnv(1, models.JobEnv{{Key: "MODE", Value: "weekly"}, {Key: "TOKEN", Value: models.JobEnvMask, IsSecret: true}}, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := again.Lookup("TOKEN"); kept.Value != tok.Value {
		t.Fatal("a masked secret must keep its stored value")
	}
	if !SameEnv(sealed, models.JobEnv{{Key: "MODE", Value: "nightly"}, {Key: "TOKEN", Value: models.JobEnvMask, IsSecret: true}}) {
		t.Fatal("an unchanged env sent back masked must compare equal")
	}
	if SameEnv(sealed, models.JobEnv{{Key: "MODE", Value: "weekly"}, {Key: "TOKEN", Value: models.JobEnvMask, IsSecret: true}}) {
		t.Fatal("a changed value must not compare equal")
	}

	for name, in := range map[string]models.JobEnv{
		"empty key":   {{Key: " ", Value: "x"}},
		"equals sign": {{Key: "A=B", Value: "x"}},
		"duplicate":   {{Key: "A", Value: "1"}, {Key: "A", Value: "2"}},
		"bad field":   {{Key: "URL", Value: "${{ apps.web.pubic_url }}"}},
		"mask, none":  {{Key: "NEW", Value: models.JobEnvMask, IsSecret: true}},
	} {
		if _, err := s.SealEnv(1, in, nil); !errors.Is(err, ErrInvalidEnv) {
			t.Errorf("%s: err = %v, want ErrInvalidEnv", name, err)
		}
	}
}
