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

package database

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/miabi-io/miabi/internal/models"
)

// An env map keys on these to rename a field's variable, or blanks one to skip the field.
const (
	EnvFieldURL      = "url"
	EnvFieldLegacy   = "database_url" // deprecated alias of url
	EnvFieldHost     = "host"
	EnvFieldPort     = "port"
	EnvFieldName     = "name"
	EnvFieldUser     = "user"
	EnvFieldPassword = "password"
)

// envFieldOrder fixes the order variables are written in.
var envFieldOrder = []string{EnvFieldURL, EnvFieldLegacy, EnvFieldHost, EnvFieldPort, EnvFieldName, EnvFieldUser, EnvFieldPassword}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Redis uses REDIS_* so it can sit next to an unprefixed SQL database.
func DefaultEnvNames(engine models.DBEngine) map[string]string {
	if engine == models.DBEngineRedis {
		return map[string]string{
			EnvFieldURL:      "REDIS_URL",
			EnvFieldHost:     "REDIS_HOST",
			EnvFieldPort:     "REDIS_PORT",
			EnvFieldPassword: "REDIS_PASSWORD",
		}
	}
	return map[string]string{
		EnvFieldURL:      "DB_URL",
		EnvFieldLegacy:   "DATABASE_URL",
		EnvFieldHost:     "DB_HOST",
		EnvFieldPort:     "DB_PORT",
		EnvFieldName:     "DB_NAME",
		EnvFieldUser:     "DB_USER",
		EnvFieldPassword: "DB_PASSWORD",
	}
}

func NormalizeEnvMap(engine models.DBEngine, prefix string, m map[string]string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	defaults := DefaultEnvNames(engine)
	out := make(map[string]string, len(m))
	for field, name := range m {
		if _, ok := defaults[field]; !ok {
			return nil, fmt.Errorf("env field %q does not apply to %s", field, engine)
		}
		if name != "" && !envNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid env var name %q for %s", name, field)
		}
		out[field] = name
	}
	seen := map[string]string{}
	for field, name := range resolveEnvNames(engine, prefix, out) {
		if other, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s and %s both map to %s", other, field, name)
		}
		seen[name] = field
	}
	return out, nil
}

// EnvKeys returns the variable names a link injects, in write order.
func EnvKeys(engine models.DBEngine, prefix string, m map[string]string) []string {
	names := resolveEnvNames(engine, prefix, m)
	out := make([]string, 0, len(names))
	for _, f := range envFieldOrder {
		if n, ok := names[f]; ok {
			out = append(out, n)
		}
	}
	return out
}

func resolveEnvNames(engine models.DBEngine, prefix string, m map[string]string) map[string]string {
	out := map[string]string{}
	for field, def := range DefaultEnvNames(engine) {
		name, mapped := m[field]
		if !mapped {
			name = def
			if prefix != "" {
				name = prefix + "_" + def
			}
		}
		if name != "" {
			out[field] = name
		}
	}
	return out
}

// EnvVar is one variable a database link writes onto an app.
type EnvVar struct {
	Key    string
	Value  string
	Secret bool
}

// Password and URL are Vault references when secrets are wired, so the app env never holds plaintext.
func (s *Service) AppEnv(inst *models.DatabaseInstance, d *models.Database, prefix string, m map[string]string) ([]EnvVar, error) {
	var conn ConnectionInfo
	var err error
	var urlSecret, passSecret string
	if d != nil {
		conn, err = s.DatabaseConnection(inst, d)
		urlSecret, passSecret = URLSecretName(inst, d), PasswordSecretName(inst, d)
	} else {
		conn, err = s.InstanceConnection(inst)
		urlSecret, passSecret = InstanceURLSecretName(inst), InstancePasswordSecretName(inst)
	}
	if err != nil {
		return nil, err
	}
	url := EnvVar{Value: conn.URI, Secret: true}
	pass := EnvVar{Value: conn.Password, Secret: true}
	if s.secrets != nil {
		if conn.URI != "" {
			url = EnvVar{Value: "${{ secrets." + urlSecret + " }}"}
		}
		if conn.Password != "" {
			pass = EnvVar{Value: "${{ secrets." + passSecret + " }}"}
		}
	}
	values := map[string]EnvVar{
		EnvFieldURL:      url,
		EnvFieldLegacy:   url,
		EnvFieldHost:     {Value: conn.Host},
		EnvFieldPort:     {Value: strconv.Itoa(conn.Port)},
		EnvFieldName:     {Value: conn.Database},
		EnvFieldUser:     {Value: conn.Username},
		EnvFieldPassword: pass,
	}
	names := resolveEnvNames(inst.Engine, prefix, m)
	var out []EnvVar
	for _, f := range envFieldOrder {
		name, ok := names[f]
		v := values[f]
		if !ok || v.Value == "" || (f == EnvFieldPort && conn.Port == 0) {
			continue
		}
		v.Key = name
		out = append(out, v)
	}
	return out, nil
}

// InjectedKeys falls back to the prefix-implied set for links made before keys were recorded.
func InjectedKeys(engine models.DBEngine, prefix string, m map[string]string, recorded []string) []string {
	if len(recorded) > 0 {
		return recorded
	}
	return EnvKeys(engine, prefix, m)
}

// envConflicts returns the keys that are already injected by another link.
func envConflicts(keys, taken []string) []string {
	var out []string
	for _, k := range keys {
		if slices.Contains(taken, k) {
			out = append(out, k)
		}
	}
	return out
}
