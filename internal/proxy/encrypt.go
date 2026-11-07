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

package proxy

import (
	"github.com/jkaninda/encryptor"
	goutils "github.com/jkaninda/go-utils"
	"gopkg.in/yaml.v3"
)

// Central gateway's key only (must match its GOMA_CONFIG_ENCRYPTION_KEY); node gateways get per-node keys, see edgegateway.ConfigKey.
const ConfigEncryptionKeyEnv = "GOMA_CONFIG_ENCRYPTION_KEY"

// CentralConfigKey returns the central gateway's passphrase, or "" when config encryption is off.
func CentralConfigKey() string {
	return goutils.Env(ConfigEncryptionKeyEnv, "")
}

// An empty key means encryption is off and callers must not reach here.
func encryptField(s, key string) (string, error) {
	return encryptor.EncryptString([]byte(s), key)
}

// With a key, the rule becomes one encrypted YAML scalar that Goma decrypts and decodes at load.
func renderRule(rule map[string]interface{}, key string) (interface{}, error) {
	if len(rule) == 0 {
		return nil, nil
	}
	if key == "" {
		return rule, nil
	}
	data, err := yaml.Marshal(rule)
	if err != nil {
		return nil, err
	}
	return encryptField(string(data), key)
}
