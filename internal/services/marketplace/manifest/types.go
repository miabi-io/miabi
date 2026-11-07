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

// Package manifest defines the miabi.io/v1 Template format and its parsing, validation and interpolation.
package manifest

// APIVersion / Kind are the only accepted document identifiers.
const (
	APIVersion = "miabi.io/v1"
	KindValue  = "Template"
)

// Placement decides which database server hosts a template's logical database.
type Placement string

const (
	// PlacementAuto, the default, reuses a compatible running instance or provisions a dedicated one.
	PlacementAuto Placement = "auto"
	// Forced for Redis, which has no logical databases.
	PlacementDedicated Placement = "dedicated"
	PlacementShared    Placement = "shared"
)

// Valid reports whether p is one of the recognized placement modes.
func (p Placement) Valid() bool {
	switch p {
	case PlacementAuto, PlacementDedicated, PlacementShared:
		return true
	default:
		return false
	}
}

// InputType is the kind of value an install-time input collects.
type InputType string

const (
	InputString   InputType = "string"
	InputPassword InputType = "password"
	InputBool     InputType = "bool"
	InputSelect   InputType = "select"
	InputNumber   InputType = "number"
)

// Manifest is one version of a template.
type Manifest struct {
	APIVersion   string      `yaml:"apiVersion" json:"apiVersion"`
	Kind         string      `yaml:"kind" json:"kind"`
	Metadata     Metadata    `yaml:"metadata" json:"metadata"`
	Inputs       []Input     `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Databases    []Database  `yaml:"databases,omitempty" json:"databases,omitempty"`
	Volumes      []Volume    `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Configs      []Config    `yaml:"configs,omitempty" json:"configs,omitempty"`
	Stack        *StackSpec  `yaml:"stack,omitempty" json:"stack,omitempty"`
	Applications []AppSpec   `yaml:"applications,omitempty" json:"applications,omitempty"`
	Routes       []RouteSpec `yaml:"routes,omitempty" json:"routes,omitempty"`
}

// Declaring this block forces a stack even for a single app; an app-level var with the same key wins.
type StackSpec struct {
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	SecretEnv   []string          `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// Metadata describes and identifies a template.
type Metadata struct {
	// Name is the stable template handle (e.g. "ghost").
	Name string `yaml:"name" json:"name"`
	// DisplayName is the free-text catalog label (e.g. "Ghost").
	DisplayName string   `yaml:"displayName" json:"displayName"`
	Version     string   `yaml:"version" json:"version"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Category    string   `yaml:"category,omitempty" json:"category,omitempty"`
	Icon        string   `yaml:"icon,omitempty" json:"icon,omitempty"`
	Homepage    string   `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Author      *Author  `yaml:"author,omitempty" json:"author,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	MinMiabi    string   `yaml:"minMiabi,omitempty" json:"minMiabi,omitempty"`
}

// Distinct from Metadata.Homepage, which points at the upstream project.
type Author struct {
	Name    string `yaml:"name" json:"name"`
	Email   string `yaml:"email,omitempty" json:"email,omitempty"`
	Website string `yaml:"website,omitempty" json:"website,omitempty"`
}

type Input struct {
	Key         string    `yaml:"key" json:"key"`
	Label       string    `yaml:"label,omitempty" json:"label,omitempty"`
	Help        string    `yaml:"help,omitempty" json:"help,omitempty"`
	Type        InputType `yaml:"type,omitempty" json:"type,omitempty"`
	Default     string    `yaml:"default,omitempty" json:"default,omitempty"`
	Placeholder string    `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	Pattern     string    `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Options     []string  `yaml:"options,omitempty" json:"options,omitempty"`
	Required    bool      `yaml:"required,omitempty" json:"required,omitempty"`
	// Generate fills the value with a strong random secret when left blank.
	Generate bool `yaml:"generate,omitempty" json:"generate,omitempty"`
	// 0 uses the default (24); e.g. 32 for an AES-256 key.
	Length int `yaml:"length,omitempty" json:"length,omitempty"`
}

// Database is a logical database dependency. See Placement.
type Database struct {
	Name      string    `yaml:"name" json:"name"`
	Engine    string    `yaml:"engine" json:"engine"`
	Version   string    `yaml:"version,omitempty" json:"version,omitempty"`
	Placement Placement `yaml:"placement,omitempty" json:"placement,omitempty"`
	// A sized database never lands on an existing instance, which has its own limits.
	Resources *Resources `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// Volume is a managed volume created before the applications start.
type Volume struct {
	Name string `yaml:"name" json:"name"`
}

type AppSpec struct {
	Name          string            `yaml:"name" json:"name"`
	Primary       bool              `yaml:"primary,omitempty" json:"primary,omitempty"`
	Image         string            `yaml:"image" json:"image"`
	Tag           string            `yaml:"tag,omitempty" json:"tag,omitempty"`
	Command       []string          `yaml:"command,omitempty" json:"command,omitempty"`
	RestartPolicy string            `yaml:"restartPolicy,omitempty" json:"restartPolicy,omitempty"`
	Ports         []Port            `yaml:"ports,omitempty" json:"ports,omitempty"`
	Env           map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	SecretEnv     []string          `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"`
	Mounts        []Mount           `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Resources     *Resources        `yaml:"resources,omitempty" json:"resources,omitempty"`
	Healthcheck   *Healthcheck      `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
}

// Port is a container port the application listens on.
type Port struct {
	Container int    `yaml:"container" json:"container"`
	Scheme    string `yaml:"scheme,omitempty" json:"scheme,omitempty"` // http | https
}

// Host binds are deliberately not permitted in templates.
type Mount struct {
	Volume string `yaml:"volume,omitempty" json:"volume,omitempty"`
	Config string `yaml:"config,omitempty" json:"config,omitempty"`
	// Empty projects every file under Path as a directory prefix.
	Key      string `yaml:"key,omitempty" json:"key,omitempty"`
	Path     string `yaml:"path" json:"path"`
	Mode     string `yaml:"mode,omitempty" json:"mode,omitempty"`
	ReadOnly bool   `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`
}

type Config struct {
	Name  string            `yaml:"name" json:"name"`
	Files map[string]string `yaml:"files" json:"files"`
	Mode  string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Sensitive redacts content in API responses and diffs it by digest only.
	Sensitive bool `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	// Exactly two distinct entries.
	Delimiters []string `yaml:"delimiters,omitempty" json:"delimiters,omitempty"`
}

// Memory accepts Ki/Mi/Gi; CPU is a core fraction; empty uses the platform default.
type Resources struct {
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
}

// Healthcheck mirrors the application healthcheck options.
type Healthcheck struct {
	Type               string `yaml:"type,omitempty" json:"type,omitempty"` // image (default) | none | http | command
	Path               string `yaml:"path,omitempty" json:"path,omitempty"`
	Command            string `yaml:"command,omitempty" json:"command,omitempty"`
	Port               int    `yaml:"port,omitempty" json:"port,omitempty"`
	IntervalSeconds    int    `yaml:"intervalSeconds,omitempty" json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	Retries            int    `yaml:"retries,omitempty" json:"retries,omitempty"`
	StartPeriodSeconds int    `yaml:"startPeriodSeconds,omitempty" json:"startPeriodSeconds,omitempty"`
}

// False when the template has no applications (database-only).
func (m *Manifest) PrimaryApp() (AppSpec, bool) {
	if len(m.Applications) == 0 {
		return AppSpec{}, false
	}
	for _, a := range m.Applications {
		if a.Primary {
			return a, true
		}
	}
	return m.Applications[0], true
}

func (m *Manifest) IsDatabaseOnly() bool {
	return len(m.Applications) == 0 && len(m.Databases) > 0
}

func (m *Manifest) WantsStack() bool {
	return len(m.Applications) > 1 || m.Stack != nil
}
