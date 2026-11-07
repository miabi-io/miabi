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

package dr

import (
	"encoding/xml"
	"fmt"
	"path"
	"strings"
	"time"
)

// ManifestExt is XML only: one format means one thing to find in a bucket.
const ManifestExt = ".xml"

// ManifestSchema is the manifest's version.
const ManifestSchema = 1

// Manifest is cleartext so restore preflight needs only the bucket and passphrase; the KEK fingerprint reveals no key.
type Manifest struct {
	XMLName xml.Name `json:"-" xml:"recoveryPoint"`

	// Notice lets whoever finds the file mid-incident, without this repo, tell what it is.
	Notice string `json:"_notice" xml:"notice"`

	Schema int    `json:"schema" xml:"schema,attr"`
	Ref    string `json:"ref" xml:"ref,attr"`

	InstallID     string `json:"install_id" xml:"installId"`
	MiabiVersion  string `json:"miabi_version" xml:"miabiVersion"`
	SchemaVersion string `json:"schema_version,omitempty" xml:"schemaVersion,omitempty"`
	// KEKFingerprint is HMAC-SHA256(master key, KEKFingerprintLabel).
	KEKFingerprint string `json:"kek_fingerprint,omitempty" xml:"kekFingerprint,omitempty"`

	Encrypted      bool `json:"encrypted" xml:"encrypted"`
	IdentitySealed bool `json:"identity_sealed" xml:"identitySealed"`

	Bucket string `json:"bucket,omitempty" xml:"bucket,omitempty"`
	// Prefix is the object prefix the database dump lives under.
	Prefix string `json:"prefix,omitempty" xml:"prefix,omitempty"`
	// VolumePrefix is configured separately from the database prefix, so a restore cannot guess it.
	VolumePrefix string `json:"volume_prefix,omitempty" xml:"volumePrefix,omitempty"`

	Artifacts []Artifact `json:"artifacts" xml:"artifacts>artifact"`
	CreatedAt time.Time  `json:"created_at" xml:"createdAt"`
}

// Artifact is one file in a recovery point.
type Artifact struct {
	Subject string `json:"subject" xml:"subject,attr"`
	// Volume is the Docker volume an archive restores into (subject=volume).
	Volume string `json:"volume,omitempty" xml:"volume,omitempty"`
	// Workspace is the owning workspace slug for tenant artifacts.
	Workspace string `json:"workspace,omitempty" xml:"workspace,omitempty"`
	// Database is the logical database name for tenant database artifacts.
	Database string `json:"database,omitempty" xml:"database,omitempty"`
	// Engine decides which restore tool can read the dump back.
	Engine string `json:"engine,omitempty" xml:"engine,omitempty"`
	// File is relative to Path; the identity envelope records its full object key instead.
	File string `json:"file" xml:"file"`
	// Path is recorded per artifact because tenant artifacts live under a per-workspace branch; empty on old manifests.
	Path      string `json:"path,omitempty" xml:"path,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty" xml:"sizeBytes,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty" xml:"encrypted,omitempty"`
}

// DatabaseArtifact returns the control-plane dump, or nil.
func (m *Manifest) DatabaseArtifact() *Artifact {
	for i := range m.Artifacts {
		if m.Artifacts[i].Subject == SubjectDatabase {
			return &m.Artifacts[i]
		}
	}
	return nil
}

// TenantArtifacts are restored after the control plane is back and its containers recreated.
func (m *Manifest) TenantArtifacts() []Artifact {
	out := make([]Artifact, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if a.Subject == SubjectTenantDatabase || a.Subject == SubjectTenantVolume {
			out = append(out, a)
		}
	}
	return out
}

// Artifact subjects, mirroring models.PlatformBackupSubject.
const (
	SubjectDatabase       = "database"
	SubjectVolume         = "volume"
	SubjectIdentity       = "identity"
	SubjectTenantDatabase = "tenant-database"
	SubjectTenantVolume   = "tenant-volume"
)

// VolumeArtifacts returns the platform volume archives.
func (m *Manifest) VolumeArtifacts() []Artifact {
	out := make([]Artifact, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if a.Subject == SubjectVolume && a.Volume != "" {
			out = append(out, a)
		}
	}
	return out
}

// Validate checks a manifest describes something restorable.
func (m *Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("recovery point manifest schema %d is not supported by this build (expected %d)", m.Schema, ManifestSchema)
	}
	if !IsRef(m.Ref) {
		return fmt.Errorf("recovery point manifest carries no valid ref")
	}
	if m.DatabaseArtifact() == nil {
		return fmt.Errorf("recovery point %s contains no control-plane database dump", m.Ref)
	}
	return nil
}

// ManifestNotice: the file is deliberately readable and carries no key, credential or passphrase.
const ManifestNotice = "Miabi recovery point index."

// EncodeManifest stamps the schema, but Validate deliberately does not, so set it at construction.
func EncodeManifest(m *Manifest) ([]byte, error) {
	m.Schema = ManifestSchema
	m.Notice = ManifestNotice
	body, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// DecodeManifest parses and validates a manifest read from the bucket.
func DecodeManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decode recovery point manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

const TenantSegment = "tenants"

// TenantPath is shared by backup and restore so both use one layout.
func TenantPath(base, workspace string) string {
	ws := slugSegment(workspace)
	if ws == "" {
		ws = "unknown"
	}
	if b := strings.Trim(base, "/"); b != "" {
		return b + "/" + TenantSegment + "/" + ws
	}
	return TenantSegment + "/" + ws
}

func slugSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ArtifactPath reconstructs the layout from the subject for manifests written before Path existed.
func (m *Manifest) ArtifactPath(a Artifact) string {
	if p := strings.Trim(a.Path, "/"); p != "" {
		return p
	}
	switch a.Subject {
	case SubjectVolume:
		return strings.Trim(m.VolumePrefix, "/")
	case SubjectTenantVolume:
		return TenantPath(m.VolumePrefix, a.Workspace)
	case SubjectTenantDatabase:
		return TenantPath(m.Prefix, a.Workspace)
	default:
		return strings.Trim(m.Prefix, "/")
	}
}

// ArtifactKey is an artifact's full object key.
func (m *Manifest) ArtifactKey(a Artifact) string {
	if p := m.ArtifactPath(a); p != "" {
		return p + "/" + a.File
	}
	return a.File
}

// ManifestObject sits at the root prefix because it is the one file an operator finds by looking.
func ManifestObject(rootPrefix, ref string) string {
	name := "recovery-" + ref + ManifestExt
	if p := strings.Trim(strings.TrimSpace(rootPrefix), "/"); p != "" {
		return path.Join(p, name)
	}
	return name
}

// RefFromManifestObject recovers the ref from an info file's object key, or "".
func RefFromManifestObject(key string) string {
	base := path.Base(strings.TrimSpace(key))
	if !strings.HasPrefix(base, "recovery-") || !strings.HasSuffix(base, ManifestExt) {
		return ""
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(base, "recovery-"), ManifestExt)
	if !IsRef(ref) {
		return ""
	}
	return ref
}
