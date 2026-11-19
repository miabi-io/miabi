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

// Package managedenv builds the MIABI_* variables Miabi sets in every app and job container.
package managedenv

import (
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/appref"
	"github.com/miabi-io/miabi/internal/models"
)

// The names are a public contract: add to them, never rename or remove one.
const (
	AppName          = "MIABI_APP_NAME"
	AppID            = "MIABI_APP_ID"
	WorkspaceName    = "MIABI_WORKSPACE_NAME"
	WorkspaceID      = "MIABI_WORKSPACE_ID"
	Stack            = "MIABI_STACK"
	KindVar          = "MIABI_KIND"
	JobName          = "MIABI_JOB_NAME"
	JobRunID         = "MIABI_JOB_RUN_ID"
	DeploymentID     = "MIABI_DEPLOYMENT_ID"
	Release          = "MIABI_RELEASE"
	Version          = "MIABI_VERSION"
	ImageTag         = "MIABI_IMAGE_TAG"
	Commit           = "MIABI_COMMIT"
	Branch           = "MIABI_BRANCH"
	Image            = "MIABI_IMAGE"
	ImageDigest      = "MIABI_IMAGE_DIGEST"
	Port             = "MIABI_PORT"
	InternalURL      = "MIABI_INTERNAL_URL"
	PublicURL        = "MIABI_PUBLIC_URL"
	PublicHost       = "MIABI_PUBLIC_HOST"
	Location         = "MIABI_LOCATION"
	Node             = "MIABI_NODE"
	NodeID           = "MIABI_NODE_ID"
	Replica          = "MIABI_REPLICA"
	CPULimit         = "MIABI_CPU_LIMIT"
	MemoryLimitMB    = "MIABI_MEMORY_LIMIT_MB"
	taskSlotTemplate = "{{.Task.Slot}}"
	nodeIDTemplate   = "{{.Node.ID}}"
)

// Names lists every variable Miabi may set, in the order it sets them.
var Names = []string{
	AppName, AppID, WorkspaceName, WorkspaceID, Stack, KindVar, JobName, JobRunID,
	DeploymentID, Release, Version, ImageTag, Commit, Branch, Image, ImageDigest,
	Port, InternalURL, PublicURL, PublicHost, Location, Node, NodeID, Replica,
	CPULimit, MemoryLimitMB,
}

var known = func() map[string]bool {
	m := make(map[string]bool, len(Names))
	for _, n := range Names {
		m[n] = true
	}
	return m
}()

// IsManaged reports whether name is one Miabi sets.
func IsManaged(name string) bool { return known[name] }

type Kind string

const (
	KindApp     Kind = "app"
	KindJob     Kind = "job"
	KindCronJob Kind = "cronjob"
)

// Input is everything one container's managed env depends on; zero values leave their variable unset.
type Input struct {
	AppName       string
	AppID         uint
	WorkspaceName string
	WorkspaceID   uint
	Stack         string
	Kind          Kind
	JobName       string
	JobRunID      uint
	DeploymentID  uint
	Release       int
	ImageTag      string
	Commit        string
	Branch        string
	Image         string
	Digest        string
	Port          int
	PublicURL     string
	PublicHost    string
	Location      string
	Node          string
	// Service marks a Swarm service, whose env is shared by every task, so per-task values become templates.
	Service     bool
	NanoCPUs    int64
	MemoryBytes int64
}

type Var struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// PerReplica values are Swarm templates the engine expands in each task.
	PerReplica bool `json:"per_replica,omitempty"`
}

// Build never emits an empty value: a variable without one is left unset.
func Build(in Input) []Var {
	var out []Var
	set := func(name, value string) {
		if value != "" {
			out = append(out, Var{Name: name, Value: value})
		}
	}
	id := func(v uint) string {
		if v == 0 {
			return ""
		}
		return strconv.FormatUint(uint64(v), 10)
	}
	set(AppName, in.AppName)
	set(AppID, id(in.AppID))
	set(WorkspaceName, in.WorkspaceName)
	set(WorkspaceID, id(in.WorkspaceID))
	set(Stack, in.Stack)
	kind := in.Kind
	if kind == "" {
		kind = KindApp
	}
	set(KindVar, string(kind))
	if kind != KindApp {
		set(JobName, in.JobName)
		set(JobRunID, id(in.JobRunID))
	}
	set(DeploymentID, id(in.DeploymentID))
	if in.Release > 0 {
		set(Release, strconv.Itoa(in.Release))
	}
	set(Version, versionOf(in.ImageTag))
	set(ImageTag, in.ImageTag)
	set(Commit, in.Commit)
	set(Branch, in.Branch)
	set(Image, in.Image)
	set(ImageDigest, in.Digest)
	if in.Port > 0 {
		set(Port, strconv.Itoa(in.Port))
	}
	set(InternalURL, internalURL(in.AppName, in.Port))
	set(PublicURL, in.PublicURL)
	set(PublicHost, in.PublicHost)
	set(Location, in.Location)
	set(Node, in.Node)
	if in.Service {
		out = append(out, Var{Name: NodeID, Value: nodeIDTemplate, PerReplica: true})
		out = append(out, Var{Name: Replica, Value: taskSlotTemplate, PerReplica: true})
	} else {
		set(Replica, "1")
	}
	set(CPULimit, cpuLimit(in.NanoCPUs))
	if in.MemoryBytes > 0 {
		set(MemoryLimitMB, strconv.FormatInt(in.MemoryBytes/(1024*1024), 10))
	}
	return out
}

// Env renders vars as KEY=value entries.
func Env(vars []Var) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Name+"="+v.Value)
	}
	return out
}

// Matches the pipeline set, where MIABI_IMAGE_TAG is the version with a leading v.
func versionOf(tag string) string {
	v := strings.TrimPrefix(tag, "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return ""
	}
	return v
}

func internalURL(name string, port int) string {
	if name == "" {
		return ""
	}
	if port <= 0 {
		return "http://" + name
	}
	return "http://" + name + ":" + strconv.Itoa(port)
}

func cpuLimit(nano int64) string {
	if nano <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(nano)/1e9, 'f', -1, 64)
}

type WorkspaceFinder interface {
	FindByID(id uint) (*models.Workspace, error)
}

type ClusterFinder interface {
	FindByID(id uint) (*models.Cluster, error)
}

type ServerFinder interface {
	FindByID(id uint) (*models.Server, error)
	FindLocal() (*models.Server, error)
}

type RouteLister interface {
	ListByApp(appID uint) ([]models.Route, error)
}

// Source fills the values that come from the app and where it runs; any nil finder leaves its values unset.
type Source struct {
	Workspaces WorkspaceFinder
	Clusters   ClusterFinder
	Servers    ServerFinder
	Routes     RouteLister
}

// ForApp leaves deployment, release and job values to the caller, which knows what it is running.
func (s *Source) ForApp(app *models.Application) Input {
	in := Input{
		AppName:     app.Name,
		AppID:       app.ID,
		WorkspaceID: app.WorkspaceID,
		Kind:        KindApp,
		Port:        app.Port,
		Service:     app.RuntimeKind == models.RuntimeService,
		NanoCPUs:    app.NanoCPUs,
		MemoryBytes: app.MemoryBytes,
	}
	if app.SourceType == models.AppSourceGit {
		in.Branch = app.GitRef
	}
	if app.Stack != nil {
		in.Stack = app.Stack.Name
	}
	if s == nil {
		return in
	}
	if s.Workspaces != nil {
		if ws, err := s.Workspaces.FindByID(app.WorkspaceID); err == nil {
			in.WorkspaceName = ws.Name
		}
	}
	if s.Clusters != nil {
		if c, err := s.Clusters.FindByID(app.ClusterID); err == nil {
			in.Location = c.Name
		}
	}
	// A service's tasks land wherever Swarm puts them, so only a pinned one has a known node.
	if s.Servers != nil {
		switch {
		case in.Service && app.PinnedServerID != 0:
			in.Node = s.serverName(app.PinnedServerID)
		case !in.Service:
			in.Node = s.serverName(app.ServerID)
		}
	}
	if s.Routes != nil {
		if routes, err := s.Routes.ListByApp(app.ID); err == nil {
			addr := appref.AddressOf(app, routes)
			in.PublicURL, in.PublicHost = addr.URL, addr.Host
		}
	}
	return in
}

func (s *Source) serverName(id uint) string {
	var srv *models.Server
	var err error
	if id == 0 {
		srv, err = s.Servers.FindLocal()
	} else {
		srv, err = s.Servers.FindByID(id)
	}
	if err != nil || srv == nil {
		return ""
	}
	return srv.Name
}
