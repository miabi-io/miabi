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

package backup

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	// DBArtifactRe matches the artifacts of the database *-bkup tools:
	// <name>_YYYYMMDD_HHMMSS.<sql|archive|dump>[.gz|.zst][.gpg|.age].
	DBArtifactRe = regexp.MustCompile(`[\w.\-]+\.(?:sql|archive|dump)(?:\.gz|\.zst)?(?:\.gpg|\.age)?`)
	// VolumeArtifactRe matches volume-bkup archives: <name>_YYYYMMDD_HHMMSS.tar[.gz|.zst][.gpg|.age].
	VolumeArtifactRe = regexp.MustCompile(`[\w.\-]+\.tar(?:\.gz|\.zst)?(?:\.gpg|\.age)?`)

	completedBytesRe = regexp.MustCompile(`msg="?Backup completed"?[^\n]*\bbytes=(\d+)`)
)

// IsEncryptedArtifact reports whether the tools encrypted the artifact (GPG or age).
func IsEncryptedArtifact(name string) bool {
	return strings.HasSuffix(name, ".gpg") || strings.HasSuffix(name, ".age")
}

// IndexEnv names the tool's backup index for a scope (an instance, a volume,
// the platform). Each scope gets its own index so scopes sharing a bucket path
// never see each other's history; "{engine}" is expanded by the tool.
func IndexEnv(scope string) string {
	return "BACKUP_INDEX_NAME={engine}-" + indexScope(scope) + "-metadata.json"
}

func indexScope(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 100 {
		out = out[:100]
	}
	if out == "" {
		out = "default"
	}
	return out
}

// ArtifactSize returns the stored size the tool reports on its final
// "Backup completed" line (0 when the tool predates it).
func ArtifactSize(out string) int64 {
	m := completedBytesRe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(m[len(m)-1][1], 10, 64)
	return n
}

var completedSHARe = regexp.MustCompile(`msg="?Backup completed"?[^\n]*\bsha256=([0-9a-f]{64})`)

// ArtifactSHA256 returns the stored artifact's sha256 from the tool's final
// "Backup completed" line ("" when the tool predates it).
func ArtifactSHA256(out string) string {
	m := completedSHARe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// ErrAgeIdentityRequired is returned when restoring an age-encrypted artifact
// without the matching identity.
var ErrAgeIdentityRequired = errors.New("this backup is encrypted with age: an age identity (AGE-SECRET-KEY-…) is required to restore it")

// Encrypting reports whether the destination encrypts what is written to it.
func (d Destination) Encrypting() bool { return d.AgeRecipients != "" || d.GPGPassphrase != "" }

// EncryptionEnv is the encryption environment of a backup run: age recipients
// when set, else the GPG passphrase.
func (d Destination) EncryptionEnv() []string {
	switch {
	case d.AgeRecipients != "":
		return []string{"ENCRYPTION=age", "AGE_RECIPIENTS=" + d.AgeRecipients}
	case d.GPGPassphrase != "":
		return []string{"GPG_PASSPHRASE=" + d.GPGPassphrase}
	}
	return nil
}

// RestoreKeyEnv is what the tool needs to decrypt file: the passphrase for
// ".gpg", the identity for ".age", nothing for a cleartext artifact.
func RestoreKeyEnv(file, passphrase, ageIdentity string) ([]string, error) {
	switch {
	case strings.HasSuffix(file, ".age"):
		if ageIdentity == "" {
			return nil, ErrAgeIdentityRequired
		}
		return []string{"AGE_IDENTITY=" + ageIdentity}, nil
	case strings.HasSuffix(file, ".gpg") && passphrase != "":
		return []string{"GPG_PASSPHRASE=" + passphrase}, nil
	}
	return nil, nil
}

// VerifyOutcome interprets a `verify` run: ok, failed with a reason, or skipped
// when the image predates the command.
func VerifyOutcome(exit int, out string, err error) (status, reason string) {
	switch {
	case err != nil:
		return VerifyFailed, err.Error()
	case exit == 0:
		return VerifyOK, ""
	case strings.Contains(out, "unknown command"):
		return "", ""
	}
	detail := strings.TrimSpace(out)
	if i := strings.LastIndex(detail, "Error: "); i >= 0 {
		detail = strings.TrimSpace(detail[i+len("Error: "):])
	}
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	return VerifyFailed, detail
}
