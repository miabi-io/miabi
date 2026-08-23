// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbundle

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

const InfoExt = ".xml"

// InfoSchema is the info file's version.
const InfoSchema = 1

// InfoNotice is stamped into every info file so whoever finds one in a bucket can tell what it is.
// The file is deliberately readable: it is the index a restore consults before it has opened
// anything. It carries no secret — those live in the sealed state file beside it.
const InfoNotice = "Miabi portable workspace bundle index. The state file beside it is encrypted with the backup passphrase."

// Artifact subjects.
const (
	// SubjectState is the sealed state file: the workspace's configuration and
	// its secrets. Exactly one per bundle, and the one artifact without which a
	// bundle restores nothing.
	SubjectState = "state"
	// SubjectDatabase is one logical database's dump.
	SubjectDatabase = "database"
	// SubjectVolume is one workspace volume's archive.
	SubjectVolume = "volume"
)

// Info is a bundle's self-description, written to the bucket in cleartext beside the sealed state
// file. It exists so a restore is possible with nothing but the bucket and the passphrase: the
// same facts live in the platform's database, which is what a migration leaves behind.
type Info struct {
	XMLName xml.Name `json:"-" xml:"workspaceBundle"`

	Notice string `json:"_notice" xml:"notice"`

	Schema int    `json:"schema" xml:"schema,attr"`
	Ref    string `json:"ref" xml:"ref,attr"`

	// Workspace is the source workspace's handle — the natural key everything in
	// the state file hangs off, and the default name a restore recreates it under.
	Workspace   string `json:"workspace" xml:"workspace"`
	DisplayName string `json:"display_name,omitempty" xml:"displayName,omitempty"`
	// SourceInstall identifies the install that produced the bundle. Informational:
	// a bundle is portable by design, so a restore never requires a match.
	SourceInstall string `json:"source_install,omitempty" xml:"sourceInstall,omitempty"`
	MiabiVersion  string `json:"miabi_version,omitempty" xml:"miabiVersion,omitempty"`

	// Encrypted records that the state file (and, when the backup helpers support
	// it, the data artifacts) are sealed with a passphrase.
	Encrypted bool `json:"encrypted" xml:"encrypted"`

	Bucket string `json:"bucket,omitempty" xml:"bucket,omitempty"`
	// Prefix is the object prefix the bundle's own branch lives under.
	Prefix string `json:"prefix,omitempty" xml:"prefix,omitempty"`

	// Counts summarize the configuration carried in the sealed state file, which
	// cannot be inspected without the passphrase. They are what makes a bucket
	// listing informative rather than a list of opaque refs.
	Apps      int `json:"apps" xml:"apps,omitempty"`
	Databases int `json:"databases" xml:"databases,omitempty"`
	Volumes   int `json:"volumes" xml:"volumes,omitempty"`
	Secrets   int `json:"secrets" xml:"secrets,omitempty"`
	Configs   int `json:"configs" xml:"configs,omitempty"`
	Routes    int `json:"routes" xml:"routes,omitempty"`
	// Certificates, Pipelines and GitOpsSources are counted separately because
	// they are the classes an operator most often wants to confirm travelled
	// before trusting a bundle as a migration rather than a data backup.
	Certificates  int `json:"certificates" xml:"certificates,omitempty"`
	Pipelines     int `json:"pipelines" xml:"pipelines,omitempty"`
	GitOpsSources int `json:"gitops_sources" xml:"gitopsSources,omitempty"`

	Artifacts []Artifact `json:"artifacts" xml:"artifacts>artifact"`
	CreatedAt time.Time  `json:"created_at" xml:"createdAt"`
}

// Artifact is one file in a bundle.
type Artifact struct {
	// Subject is "state", "database" or "volume".
	Subject string `json:"subject" xml:"subject,attr"`
	// Database is the logical database name a dump restores into
	// (subject=database), and Instance the managed instance that hosts it.
	Database string `json:"database,omitempty" xml:"database,omitempty"`
	Instance string `json:"instance,omitempty" xml:"instance,omitempty"`
	// Engine decides which restore tool can read a dump back.
	Engine string `json:"engine,omitempty" xml:"engine,omitempty"`
	// Volume is the workspace volume an archive restores into (subject=volume),
	// by its Miabi name — never the Docker name, which the target mints itself.
	Volume string `json:"volume,omitempty" xml:"volume,omitempty"`
	// File is the artifact's name as the *-bkup tools record it, relative to Path.
	File string `json:"file" xml:"file"`
	// Path is the object prefix this artifact was written under, recorded per artifact because a
	// restore must find it again without re-deriving a layout. Two implementations of one layout is
	// how a restore ends up looking in a path nothing was ever written to.
	Path      string `json:"path,omitempty" xml:"path,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty" xml:"sizeBytes,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty" xml:"encrypted,omitempty"`
	// Error records why an artifact is missing from an otherwise complete bundle.
	// A bundle that lists what it failed to capture is honest; one that silently
	// omits it looks complete until the day it is needed.
	Error string `json:"error,omitempty" xml:"error,omitempty"`
}

// Key is an artifact's full object key.
func (a Artifact) Key() string {
	if p := strings.Trim(a.Path, "/"); p != "" {
		return p + "/" + a.File
	}
	return a.File
}

// OK reports whether the artifact was actually captured.
func (a Artifact) OK() bool { return a.Error == "" && a.File != "" }

// BySubject returns the captured artifacts of one subject.
func (i *Info) BySubject(subject string) []Artifact {
	out := make([]Artifact, 0, len(i.Artifacts))
	for _, a := range i.Artifacts {
		if a.Subject == subject && a.OK() {
			out = append(out, a)
		}
	}
	return out
}

// StateArtifact returns the sealed state file's artifact, or nil.
func (i *Info) StateArtifact() *Artifact {
	for idx := range i.Artifacts {
		if i.Artifacts[idx].Subject == SubjectState && i.Artifacts[idx].OK() {
			return &i.Artifacts[idx]
		}
	}
	return nil
}

// Validate checks an info file describes something restorable.
func (i *Info) Validate() error {
	if i.Schema != InfoSchema {
		return fmt.Errorf("bundle info schema %d is not supported by this build (expected %d)", i.Schema, InfoSchema)
	}
	if !IsRef(i.Ref) {
		return fmt.Errorf("bundle info carries no valid ref")
	}
	if i.StateArtifact() == nil {
		return fmt.Errorf("bundle %s contains no state file — there is nothing to restore from it", i.Ref)
	}
	return nil
}

// EncodeInfo renders an info file for upload. It stamps the schema defensively, but callers should
// set it at construction: anything that validates before encoding sees a zero. Validate does not
// assign it — a validator that repairs what it checks cannot report a genuine mismatch.
func EncodeInfo(i *Info) ([]byte, error) {
	i.Schema = InfoSchema
	i.Notice = InfoNotice
	body, err := xml.MarshalIndent(i, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// DecodeInfo parses and validates an info file read from the bucket.
func DecodeInfo(b []byte) (*Info, error) {
	var i Info
	if err := xml.Unmarshal(b, &i); err != nil {
		return nil, fmt.Errorf("decode bundle info: %w", err)
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return &i, nil
}
