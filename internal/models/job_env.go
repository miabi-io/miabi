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

package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// JobEnvMask stands in for a secret value in API responses; sending it back keeps the stored value.
const JobEnvMask = "••••••••"

type JobEnvVar struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"is_secret,omitempty"`
}

// JobEnv is a job's own variables. A secret value is stored encrypted and never leaves the API unmasked.
type JobEnv []JobEnvVar

type jobEnvRow JobEnvVar

func (e JobEnv) Value() (driver.Value, error) {
	if len(e) == 0 {
		return "[]", nil
	}
	rows := make([]jobEnvRow, len(e))
	for i, v := range e {
		rows[i] = jobEnvRow(v)
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

func (e *JobEnv) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case nil:
		*e = nil
		return nil
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return errors.New("job env: unsupported column type")
	}
	if len(b) == 0 {
		*e = nil
		return nil
	}
	var rows []jobEnvRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	out := make(JobEnv, len(rows))
	for i, r := range rows {
		out[i] = JobEnvVar(r)
	}
	*e = out
	return nil
}

func (e JobEnv) MarshalJSON() ([]byte, error) {
	out := make([]JobEnvVar, len(e))
	for i, v := range e {
		if v.IsSecret {
			v.Value = JobEnvMask
		}
		out[i] = v
	}
	return json.Marshal(out)
}

// Lookup returns the variable named key.
func (e JobEnv) Lookup(key string) (JobEnvVar, bool) {
	for _, v := range e {
		if v.Key == key {
			return v, true
		}
	}
	return JobEnvVar{}, false
}

// InheritsEnv reads a nullable inherit_env column, where a missing value means the default, true.
func InheritsEnv(v *bool) bool { return v == nil || *v }
