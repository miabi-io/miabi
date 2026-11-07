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

// The info file is deliberately readable and carries no secret; secrets live in the sealed state file.
const (
	InfoNotice      = "Miabi portable workspace bundle index. The state file beside it is encrypted with the backup passphrase."
	PlainInfoNotice = "Miabi portable workspace bundle index. This bundle is NOT encrypted: the state file beside it holds the workspace's secrets in cleartext."
)

// Artifact subjects.
const (
	// Exactly one per bundle; without it a bundle restores nothing.
	SubjectState = "state"
	// SubjectDatabase is one logical database's dump.
	SubjectDatabase = "database"
	// SubjectVolume is one workspace volume's archive.
	SubjectVolume = "volume"
)

// Info lets a restore work with only the bucket and the passphrase, even after the source DB is gone.
type Info struct {
	XMLName xml.Name `json:"-" xml:"workspaceBundle"`

	Notice string `json:"_notice" xml:"notice"`

	Schema int    `json:"schema" xml:"schema,attr"`
	Ref    string `json:"ref" xml:"ref,attr"`

	Workspace   string `json:"workspace" xml:"workspace"`
	DisplayName string `json:"display_name,omitempty" xml:"displayName,omitempty"`
	// Informational: bundles are portable, so a restore never requires a match.
	SourceInstall string `json:"source_install,omitempty" xml:"sourceInstall,omitempty"`
	MiabiVersion  string `json:"miabi_version,omitempty" xml:"miabiVersion,omitempty"`
	Encrypted     bool   `json:"encrypted" xml:"encrypted"`

	Bucket string `json:"bucket,omitempty" xml:"bucket,omitempty"`
	// Prefix is the object prefix the bundle's own branch lives under.
	Prefix string `json:"prefix,omitempty" xml:"prefix,omitempty"`

	// The sealed state file can't be inspected without the passphrase, so these counts make a listing informative.
	Apps          int `json:"apps" xml:"apps,omitempty"`
	Databases     int `json:"databases" xml:"databases,omitempty"`
	Volumes       int `json:"volumes" xml:"volumes,omitempty"`
	Secrets       int `json:"secrets" xml:"secrets,omitempty"`
	Configs       int `json:"configs" xml:"configs,omitempty"`
	Routes        int `json:"routes" xml:"routes,omitempty"`
	Certificates  int `json:"certificates" xml:"certificates,omitempty"`
	Pipelines     int `json:"pipelines" xml:"pipelines,omitempty"`
	GitOpsSources int `json:"gitops_sources" xml:"gitopsSources,omitempty"`

	Artifacts []Artifact `json:"artifacts" xml:"artifacts>artifact"`
	CreatedAt time.Time  `json:"created_at" xml:"createdAt"`
}

// Artifact is one file in a bundle.
type Artifact struct {
	// Subject is "state", "database" or "volume".
	Subject  string `json:"subject" xml:"subject,attr"`
	Database string `json:"database,omitempty" xml:"database,omitempty"`
	Instance string `json:"instance,omitempty" xml:"instance,omitempty"`
	// Engine decides which restore tool can read a dump back.
	Engine string `json:"engine,omitempty" xml:"engine,omitempty"`
	// By Miabi name, never the Docker name, which the target mints itself.
	Volume string `json:"volume,omitempty" xml:"volume,omitempty"`
	// File is the artifact's name as the *-bkup tools record it, relative to Path.
	File string `json:"file" xml:"file"`
	// Recorded per artifact so a restore never re-derives the layout and looks where nothing was written.
	Path      string `json:"path,omitempty" xml:"path,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty" xml:"sizeBytes,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty" xml:"encrypted,omitempty"`
	// A bundle that lists what it failed to capture is honest; silently omitting it looks complete.
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

// Validate does not assign the schema: a validator that repairs what it checks cannot report a mismatch.
func EncodeInfo(i *Info) ([]byte, error) {
	i.Schema = InfoSchema
	i.Notice = InfoNotice
	if !i.Encrypted {
		i.Notice = PlainInfoNotice
	}
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
