// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"github.com/jkaninda/encryptor"
	goutils "github.com/jkaninda/go-utils"
	"gopkg.in/yaml.v3"
)

// ConfigEncryptionKeyEnv is the passphrase shared with Goma Gateway. When set, Miabi encrypts
// sensitive rendered config (middleware rules and inline TLS material) so it is never written to
// the provider directory as plaintext. It MUST match Goma's GOMA_CONFIG_ENCRYPTION_KEY.
const ConfigEncryptionKeyEnv = "GOMA_CONFIG_ENCRYPTION_KEY"

// configEncryptionKey returns the shared passphrase, or an empty string when
// config encryption is disabled.
func configEncryptionKey() string {
	return goutils.Env(ConfigEncryptionKeyEnv, "")
}

func encryptionEnabled() bool {
	return configEncryptionKey() != ""
}

// encryptField encrypts s into an ASCII-armored PGP message using the shared key.
// Callers should only invoke it when encryptionEnabled returns true.
func encryptField(s string) (string, error) {
	return encryptor.EncryptString([]byte(s), configEncryptionKey())
}

// renderRule returns the value to emit for a middleware's `rule`. With encryption disabled it is
// the rule mapping unchanged; with encryption enabled the mapping is serialized to YAML and
// replaced by a single encrypted scalar that Goma decrypts and decodes at load.
func renderRule(rule map[string]interface{}) (interface{}, error) {
	if len(rule) == 0 {
		return nil, nil
	}
	if !encryptionEnabled() {
		return rule, nil
	}
	data, err := yaml.Marshal(rule)
	if err != nil {
		return nil, err
	}
	return encryptField(string(data))
}
