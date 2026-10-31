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

package models

import "time"

// VersionScheme is how a version policy numbers the images a pipeline builds.
type VersionScheme string

const (
	// VersionSchemeSemver numbers builds MAJOR.MINOR.PATCH within a series such as 1.x.x.
	VersionSchemeSemver VersionScheme = "semver"
	// VersionSchemeCalver numbers builds by date, e.g. 2026.10.3, restarting the counter each period.
	VersionSchemeCalver VersionScheme = "calver"
	// VersionSchemeRun keeps the run-<number> tag pipelines used before versioning existed.
	VersionSchemeRun VersionScheme = "run"
)

// VersionBump is the part of a semantic version a build increases.
type VersionBump string

const (
	BumpPatch VersionBump = "patch"
	BumpMinor VersionBump = "minor"
	BumpMajor VersionBump = "major"
)

// Non-release branch handling for a version policy.
const (
	// OtherBranchesPrerelease versions a non-release branch as <next>-<branch>.<n>.
	OtherBranchesPrerelease = "prerelease"
	// OtherBranchesRunTag gives a non-release branch build the plain run-<number> tag.
	OtherBranchesRunTag = "run-tag"
)

// VersionPolicy decides the version, and so the image tag, each pipeline run builds. It belongs to one
// image repository: the application for an app-bound pipeline, the pipeline itself otherwise, so two
// pipelines pushing to the same repository share one sequence and can never mint the same tag.
type VersionPolicy struct {
	UIDModel
	ID          uint `json:"id" gorm:"primaryKey"`
	WorkspaceID uint `json:"workspace_id" gorm:"index;not null"`
	// ApplicationID and PipelineID are exclusive owners; a partial unique index keeps one policy each.
	ApplicationID *uint `json:"application_id,omitempty"`
	PipelineID    *uint `json:"pipeline_id,omitempty"`

	Scheme VersionScheme `json:"scheme" gorm:"not null;default:semver"`
	// Template renders the image tag from the version, e.g. "v{version}".
	Template string `json:"template" gorm:"not null;default:'v{version}'"`
	// Series is the active version line for semver, e.g. "1.x.x"; fixed parts never move.
	Series string      `json:"series,omitempty"`
	Bump   VersionBump `json:"bump" gorm:"not null;default:patch"`
	// Start is the first version of a series that has none yet.
	Start string `json:"start,omitempty"`
	// CalverFormat is the date part of a calver version, e.g. "YYYY.MM".
	CalverFormat string `json:"calver_format,omitempty"`
	// ReleaseBranches produce real versions; empty treats every branch as a release branch.
	ReleaseBranches []string `json:"release_branches,omitempty" gorm:"serializer:json"`
	// OtherBranches is OtherBranchesPrerelease or OtherBranchesRunTag.
	OtherBranches string `json:"other_branches" gorm:"not null;default:prerelease"`
	// BranchSeries maps a branch pattern (path.Match) to the series it releases on, for maintenance lines.
	BranchSeries map[string]string `json:"branch_series,omitempty" gorm:"serializer:json"`
	// FloatingTags are templates such as "latest" or "v{major}", moved to the newest version they name.
	FloatingTags    []string `json:"floating_tags,omitempty" gorm:"serializer:json"`
	BumpFromCommits bool     `json:"bump_from_commits"`
	// ManagedBy names the pipeline whose spec `versioning:` block owns the settings; empty = the API owns them.
	ManagedBy string `json:"managed_by,omitempty"`
	// Current is the highest published version, kept for display.
	Current string `json:"current,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Filled on read: the owning application or pipeline name, and the tag the next release build gets.
	OwnerKind  string `json:"owner_kind,omitempty" gorm:"-"`
	Repository string `json:"repository,omitempty" gorm:"-"`
	Next       string `json:"next,omitempty" gorm:"-"`
}

// AllocationStatus is the lifecycle of a reserved version.
type AllocationStatus string

const (
	// AllocationReserved is held by a run that has not finished.
	AllocationReserved AllocationStatus = "reserved"
	// AllocationPublished was built and tagged; it is never handed out again.
	AllocationPublished AllocationStatus = "published"
	// AllocationAbandoned failed after a newer version was reserved, or its tag already existed, so it is skipped.
	AllocationAbandoned AllocationStatus = "abandoned"
)

// VersionAllocation is one version handed to a run. A failed run that still holds the head of its series
// deletes its row so the next run reuses the number; the unique key stops two runs sharing one.
type VersionAllocation struct {
	ID         uint        `json:"id" gorm:"primaryKey"`
	PolicyID   uint        `json:"policy_id" gorm:"uniqueIndex:idx_version_alloc_policy_version;not null"`
	Version    string      `json:"version" gorm:"uniqueIndex:idx_version_alloc_policy_version;not null"`
	Tag        string      `json:"tag" gorm:"not null"`
	Series     string      `json:"series,omitempty" gorm:"index"`
	Branch     string      `json:"branch,omitempty"`
	Prerelease bool        `json:"prerelease"`
	Bump       VersionBump `json:"bump,omitempty"`
	// Exact marks a version set by hand, which is never silently moved to another.
	Exact         bool             `json:"exact,omitempty"`
	Status        AllocationStatus `json:"status" gorm:"not null;default:reserved"`
	PipelineRunID uint             `json:"pipeline_run_id" gorm:"index;not null"`
	Digest        string           `json:"digest,omitempty"`
	Note          string           `json:"note,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`

	// Filled on read, to link the history to its runs.
	RunNumber  int  `json:"run_number,omitempty" gorm:"-"`
	PipelineID uint `json:"pipeline_id,omitempty" gorm:"-"`
}
