// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dr

import (
	"encoding/xml"
	"fmt"
	"path"
	"strings"
	"time"
)

// ManifestExt is the info file's extension. XML, and only XML: it is written for
// people and tooling outside Miabi to read, and one format means one thing to
// find in a bucket rather than two to look for.
const ManifestExt = ".xml"

// ManifestSchema is the manifest's version.
const ManifestSchema = 1

// Manifest is a recovery point's self-description, written to the bucket in cleartext beside the
// sealed identity envelope, so restore preflight works with nothing but the bucket and the
// passphrase. Nothing in it is secret: the KEK fingerprint identifies a key without revealing it.
type Manifest struct {
	XMLName xml.Name `json:"-" xml:"recoveryPoint"`

	// Notice is stamped on every file so whoever finds one in a bucket — months
	// later, mid-incident, without this repository open — can tell what it is.
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
	// VolumePrefix is the object prefix volume archives live under; the two are
	// configured separately, and a restore that guessed would find nothing.
	VolumePrefix string `json:"volume_prefix,omitempty" xml:"volumePrefix,omitempty"`

	Artifacts []Artifact `json:"artifacts" xml:"artifacts>artifact"`
	CreatedAt time.Time  `json:"created_at" xml:"createdAt"`
}

// Artifact is one file in a recovery point.
type Artifact struct {
	// Subject is "database", "volume", "identity", "tenant-database" or
	// "tenant-volume".
	Subject string `json:"subject" xml:"subject,attr"`
	// Volume is the Docker volume an archive restores into (subject=volume).
	Volume string `json:"volume,omitempty" xml:"volume,omitempty"`
	// Workspace is the owning workspace slug for tenant artifacts.
	Workspace string `json:"workspace,omitempty" xml:"workspace,omitempty"`
	// Database is the logical database name for tenant database artifacts.
	Database string `json:"database,omitempty" xml:"database,omitempty"`
	// Engine is the tenant database engine (postgres, mysql, …), which decides
	// which restore tool can read the dump back.
	Engine string `json:"engine,omitempty" xml:"engine,omitempty"`
	// File is the artifact's name as the *-bkup tools record it, relative to
	// Path. The identity envelope records its full object key instead.
	File string `json:"file" xml:"file"`
	// Path is the object prefix this artifact was written under. Recorded per artifact because it
	// cannot be derived from the subject alone — tenant artifacts live under a per-workspace branch.
	// Empty on manifests written before this field existed; see ArtifactPath.
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

// TenantArtifacts returns the tenant database dumps and volume archives — the
// workload data, restored after the control plane is back and its containers
// have been recreated.
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

// ManifestNotice is stamped into every info file so whoever finds one in a bucket knows what it
// is. The file is deliberately readable: it is the index a restore consults before it has a
// database. It carries no key, credential or passphrase.
const ManifestNotice = "Miabi recovery point index."

// EncodeManifest renders a manifest for upload. It stamps the schema defensively, but callers
// should set it at construction: anything that validates before encoding sees a zero. Validate
// deliberately does not assign it — a validator that repairs what it checks cannot report a mismatch.
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

// TenantSegment is the branch tenant artifacts live under, below the database or
// volume prefix: "<prefix>/tenants/<workspace>".
const TenantSegment = "tenants"

// TenantPath is the object prefix for one workspace's artifacts of a kind. It lives next to the
// manifest because the backup writes it and the restore has to find it again: two implementations
// of the same layout is how a restore ends up searching a path nothing was written to.
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

// ArtifactPath is the prefix an artifact lives under. It prefers the value recorded on the
// artifact; manifests written before that field existed carry none, so the layout is
// reconstructed from the subject — in one place, and aware of tenant artifacts.
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

// ManifestObject is the info file's object name within the backup ROOT prefix, not under the
// database or volume paths. It is the one file an operator is expected to find by looking, so it
// sits at the top of the tree rather than inside one of its branches.
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
