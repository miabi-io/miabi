// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"github.com/jkaninda/encryptor"
	goutils "github.com/jkaninda/go-utils"
	"gopkg.in/yaml.v3"
)

// ConfigEncryptionKeyEnv is the passphrase the CENTRAL gateway — the one shipped beside the control
// plane — shares with Miabi. When set, Miabi encrypts sensitive rendered config (middleware rules
// and inline TLS material) so it is never written to the provider directory as plaintext. It MUST
// match that gateway's GOMA_CONFIG_ENCRYPTION_KEY.
//
// It is NOT the key for the gateways Miabi deploys on nodes. Those each get their own, derived per
// node, so one compromised edge host cannot read another's config — see edgegateway.ConfigKey. The
// env var stays the operator's setting because the compose gateway's environment is theirs, not
// Miabi's, to write.
const ConfigEncryptionKeyEnv = "GOMA_CONFIG_ENCRYPTION_KEY"

// CentralConfigKey returns the central gateway's passphrase, or "" when config encryption is off.
func CentralConfigKey() string {
	return goutils.Env(ConfigEncryptionKeyEnv, "")
}

// encryptField encrypts s into an ASCII-armored PGP message under key. An empty key means
// encryption is off and callers must not reach here.
func encryptField(s, key string) (string, error) {
	return encryptor.EncryptString([]byte(s), key)
}

// renderRule returns the value to emit for a middleware's `rule`. With an empty key it is the rule
// mapping unchanged; otherwise the mapping is serialized to YAML and replaced by a single encrypted
// scalar that Goma decrypts and decodes at load.
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
