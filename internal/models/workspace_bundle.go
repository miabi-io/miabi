// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// WorkspaceBundleKind is what a run did: wrote a bundle, or read one back.
type WorkspaceBundleKind string

const (
	// BundleExport writes a workspace to the bucket as a portable bundle.
	BundleExport WorkspaceBundleKind = "export"
	// BundleRestore rebuilds a workspace from one.
	BundleRestore WorkspaceBundleKind = "restore"
)

// Bundle phases, in the order a run passes through them. They are display state,
// not a state machine: a phase is recorded as work starts so a run that stalls
// says where, which is the question asked of a long backup.
const (
	BundlePhaseState     = "state"     // collecting configuration + secrets
	BundlePhaseDatabases = "databases" // dumping / restoring managed databases
	BundlePhaseVolumes   = "volumes"   // archiving / restoring volumes
	BundlePhaseUpload    = "upload"    // sealing and writing the state + info files
	BundlePhaseDone      = "done"
)

// WorkspaceBundle is one export or restore run of a portable workspace bundle. Separate from
// Backup/VolumeBackup, which record one artifact each against a live resource: a bundle run
// records a whole workspace crossing a boundary, listed in its own info file in the bucket.
type WorkspaceBundle struct {
	ID          uint                `json:"id" gorm:"primaryKey"`
	WorkspaceID uint                `json:"workspace_id" gorm:"index;not null"`
	Kind        WorkspaceBundleKind `json:"kind" gorm:"not null;default:export"`
	// Ref is the bundle's name in the bucket ("mbwb_<workspace>_<timestamp>"). On
	// a restore it is the bundle that was read.
	Ref    string       `json:"ref" gorm:"index"`
	Status BackupStatus `json:"status" gorm:"not null;default:pending"`
	Phase  string       `json:"phase,omitempty"`
	// Trigger is manual for now; scheduled bundles reuse the same row.
	Trigger string `json:"trigger,omitempty"`

	// TargetWorkspaceID is where a restore puts what it reads — usually the run's own workspace,
	// differing when a bundle lands in a fresh one (a clone or migration). Zero on an export. The
	// run stays owned by the workspace whose bucket it read.
	TargetWorkspaceID uint `json:"target_workspace_id,omitempty" gorm:"index"`
	// RestoreData pulls the dumps and archives as well as the configuration. Off
	// restores the shape of a workspace without its contents — useful to stage a
	// clone before committing to moving gigabytes.
	RestoreData bool `json:"restore_data"`
	// DeployApps rolls out the restored applications at the end of a restore.
	DeployApps bool `json:"deploy_apps"`

	S3Bucket string `json:"s3_bucket,omitempty"`
	S3Prefix string `json:"s3_prefix,omitempty"`
	// SourceWorkspace is the workspace the bundle was taken from, by name. On a
	// restore into a differently-named workspace it is the only record of where
	// the data came from.
	SourceWorkspace string `json:"source_workspace,omitempty"`

	// Artifacts / SizeBytes summarize what the run moved.
	Artifacts int   `json:"artifacts"`
	SizeBytes int64 `json:"size_bytes"`

	// Report is the human-readable outcome: what was created, skipped or could not be carried. A
	// restore's report is the deliverable — it names the manual follow-ups (DNS, certificates,
	// unverified domains) — so it is persisted rather than logged and lost.
	Report BundleReport `json:"report,omitempty" gorm:"serializer:json"`

	Logs       string     `json:"logs,omitempty" gorm:"type:text"`
	Error      string     `json:"error,omitempty" gorm:"type:text"`
	CreatedBy  *uint      `json:"created_by,omitempty"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// BundleReport is the outcome of a run, item by item.
type BundleReport struct {
	Items []BundleReportItem `json:"items,omitempty"`
	// Notes are the manual follow-ups a restore cannot perform itself.
	Notes []string `json:"notes,omitempty"`
}

// BundleReportItem is one resource's outcome.
type BundleReportItem struct {
	Kind   string `json:"kind"`             // app | database | volume | secret | route | …
	Name   string `json:"name"`             //
	Action string `json:"action"`           // captured | created | skipped | failed
	Detail string `json:"detail,omitempty"` // why it was skipped, or what failed
}

// Add appends an item to the report.
func (r *BundleReport) Add(kind, name, action, detail string) {
	r.Items = append(r.Items, BundleReportItem{Kind: kind, Name: name, Action: action, Detail: detail})
}

// Note appends a manual follow-up.
func (r *BundleReport) Note(note string) { r.Notes = append(r.Notes, note) }

// Failures returns the items that did not succeed.
func (r *BundleReport) Failures() []BundleReportItem {
	var out []BundleReportItem
	for _, it := range r.Items {
		if it.Action == "failed" {
			out = append(out, it)
		}
	}
	return out
}
