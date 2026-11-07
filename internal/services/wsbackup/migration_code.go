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

package wsbackup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// migrationCodePrefix marks a migration code, so a pasted key or URL is told apart from one.
const migrationCodePrefix = "miabi-migrate:"

// ErrBadMigrationCode is a code that was mistyped or truncated.
var ErrBadMigrationCode = errors.New("that is not a migration code: copy it again from the source's Migration page")

type migrationCode struct {
	URL       string `json:"u"`
	Key       string `json:"k"`
	Workspace string `json:"w,omitempty"`
}

// Workspace is empty for an instance migration, where the importer picks.
func EncodeMigrationCode(url, key, workspace string) string {
	raw, _ := json.Marshal(migrationCode{URL: strings.TrimRight(url, "/"), Key: key, Workspace: workspace})
	return migrationCodePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeMigrationCode unpacks a migration code.
func DecodeMigrationCode(code string) (url, key, workspace string, err error) {
	body, ok := strings.CutPrefix(strings.TrimSpace(code), migrationCodePrefix)
	if !ok {
		return "", "", "", ErrBadMigrationCode
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return "", "", "", ErrBadMigrationCode
	}
	var c migrationCode
	if json.Unmarshal(raw, &c) != nil || c.URL == "" || c.Key == "" {
		return "", "", "", ErrBadMigrationCode
	}
	return c.URL, c.Key, c.Workspace, nil
}

func (src *MigrationSource) resolve() (string, error) {
	if strings.TrimSpace(src.Code) == "" {
		return "", nil
	}
	url, key, ws, err := DecodeMigrationCode(src.Code)
	if err != nil {
		return "", err
	}
	src.URL, src.Key = url, key
	return ws, nil
}
