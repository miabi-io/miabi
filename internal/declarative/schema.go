// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package declarative is the shared declarative resource model for Miabi.
// It generalizes the marketplace manifest engine into a family of
// miabi.io/v1 resource kinds (Application, Stack, Database, Volume, Domain,
// Secret, Project) and exposes a single parse/validate/render path plus a
// plan/diff engine.
//
// Four consumers speak the same kinds: the `apply` API, GitOps reconciliation,
// the Terraform/OpenTofu provider, and marketplace templates. One schema, one
// validator, one plan engine — author once, consume four ways.
package declarative

// APIVersion is the only accepted apiVersion. It is shared with the marketplace
// manifest engine so the two families stay in lock-step.
const APIVersion = "miabi.io/v1"

// Kind enumerates the declarative resource kinds. They map 1:1 to the resources
// the platform manages and the Terraform provider exposes.
type Kind string

const (
	KindApplication Kind = "Application"
	KindStack       Kind = "Stack"
	KindDatabase    Kind = "Database"
	KindVolume      Kind = "Volume"
	// KindRoute is an HTTP routing rule (host/path → app:port + TLS). It is
	// distinct from KindDomain: a Route exposes an app on a hostname, while a
	// Domain is the owned hostname/zone that tracks DNS ownership + the default
	// TLS policy routes under it inherit.
	KindRoute  Kind = "Route"
	KindSecret Kind = "Secret"
	// KindDomain is an owned hostname (or zone) a workspace controls: its default
	// TLS policy and (optional) wildcard coverage. DNS-ownership verification is a
	// runtime action, not a declarable field.
	KindDomain Kind = "Domain"
	// KindProject bundles a set of resources living in one repo/namespace.
	KindProject Kind = "Project"
)

// knownKinds is the set of recognized kinds, used by the parser to route a
// document's spec into the right typed field.
var knownKinds = map[Kind]bool{
	KindApplication: true,
	KindStack:       true,
	KindDatabase:    true,
	KindVolume:      true,
	KindRoute:       true,
	KindSecret:      true,
	KindDomain:      true,
	KindProject:     true,
}

// Meta is the identity block shared by every kind. Name is the reserved
// identity; Labels and Annotations are the two free-form key/value maps for
// user metadata. Labels are short and identifying (intended for selection and
// grouping); Annotations hold descriptive, non-selectable data the engine
// stores but never queries. Custom user metadata belongs in one of these maps —
// never as ad-hoc top-level keys, which would collide with future reserved
// fields.
type Meta struct {
	Name string `yaml:"name" json:"name"`
	// UID is the resource's portable identifier (its Miabi uid). Written on
	// export so a restore into another install — or a rename in a manifest — keeps
	// identity. Optional in hand-authored manifests; matched ahead of name when set.
	UID         string            `yaml:"uid,omitempty" json:"uid,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// Resource is one parsed declarative object. Exactly one of the typed spec
// pointers is set, determined by Kind.
type Resource struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind   `yaml:"kind" json:"kind"`
	Metadata   Meta   `yaml:"metadata" json:"metadata"`

	Application *ApplicationSpec `yaml:"-" json:"application,omitempty"`
	Stack       *StackSpec       `yaml:"-" json:"stack,omitempty"`
	Database    *DatabaseSpec    `yaml:"-" json:"database,omitempty"`
	Volume      *VolumeSpec      `yaml:"-" json:"volume,omitempty"`
	Route       *RouteSpec       `yaml:"-" json:"route,omitempty"`
	Secret      *SecretSpec      `yaml:"-" json:"secret,omitempty"`
	Domain      *DomainSpec      `yaml:"-" json:"domain,omitempty"`
	Project     *ProjectSpec     `yaml:"-" json:"project,omitempty"`
}

// Key is the stable identity of a resource within a workspace: "<kind>/<name>".
// Names are unique per kind, so this keys the desired/actual comparison.
func (r Resource) Key() string { return string(r.Kind) + "/" + r.Metadata.Name }

// ApplicationSpec is a long-running container workload. CI writes the immutable
// Digest; GitOps converges runtime to it.
type ApplicationSpec struct {
	Image     string            `yaml:"image" json:"image"`
	Tag       string            `yaml:"tag,omitempty" json:"tag,omitempty"`
	Digest    string            `yaml:"digest,omitempty" json:"digest,omitempty"` // sha256:… (immutable pin)
	Command   []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Ports     []PortSpec        `yaml:"ports,omitempty" json:"ports,omitempty"`
	Env       map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	SecretEnv []string          `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"`
	Mounts    []MountSpec       `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Resources *ResourceSpec     `yaml:"resources,omitempty" json:"resources,omitempty"`
	// ContainerLabels are user-defined Docker labels stamped on the app's
	// container(s) — for label-driven tools like Traefik. Reserved keys
	// (io.miabi.*, com.docker.*) are stripped on apply (a manifest is
	// machine-authored, so import is fail-soft rather than erroring).
	ContainerLabels map[string]string `yaml:"containerLabels,omitempty" json:"containerLabels,omitempty"`
	// Stack optionally names the owning Stack resource.
	Stack string `yaml:"stack,omitempty" json:"stack,omitempty"`
	// ExternalLabel pins the subdomain used for external access
	// (`<label>.<base-domain>`). Optional — when unset Miabi generates a
	// stable label; pinning it keeps the public URL deterministic across applies.
	// The label is platform-wide unique: if it is already claimed by another app,
	// it is ignored and a generated label is used instead (the apply still
	// succeeds), so a copy-pasted manifest never fails on a label clash.
	ExternalLabel string `yaml:"externalLabel,omitempty" json:"externalLabel,omitempty"`
}

// PortSpec is a container port and how it is reached. The two exposure knobs are
// orthogonal: externalAccess gives it a public HTTPS URL through the reverse
// proxy, while publish/hostPort binds it to a raw port on the node (like
// `docker -p`).
type PortSpec struct {
	Container int    `yaml:"container" json:"container"`
	Protocol  string `yaml:"protocol,omitempty" json:"protocol,omitempty"` // tcp|udp (default tcp)
	Scheme    string `yaml:"scheme,omitempty" json:"scheme,omitempty"`     // http|https (default http)
	// ExternalAccess publishes this port on `<externalLabel>.<base-domain>` over
	// HTTPS via the reverse proxy (the base domain must be configured platform-wide).
	ExternalAccess bool `yaml:"externalAccess,omitempty" json:"externalAccess,omitempty"`
	// Publish binds this container port to a host port on the node (raw TCP/UDP).
	// HostPort requests a specific host port; 0 auto-allocates from the pool.
	Publish  bool `yaml:"publish,omitempty" json:"publish,omitempty"`
	HostPort int  `yaml:"hostPort,omitempty" json:"hostPort,omitempty"`
}

// MountSpec attaches a managed Volume into the container filesystem.
type MountSpec struct {
	Volume   string `yaml:"volume" json:"volume"`
	Path     string `yaml:"path" json:"path"`
	ReadOnly bool   `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`
}

// ResourceSpec caps memory/CPU and requests GPUs. Empty/zero means unlimited/none.
type ResourceSpec struct {
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"` // e.g. "512Mi"
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`       // e.g. "0.5"
	// GPU is the number of whole GPU units the app requests (0 = none). GPUKind
	// narrows the request to a vendor/model ("nvidia", "NVIDIA A100-…").
	GPU     int    `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	GPUKind string `yaml:"gpuKind,omitempty" json:"gpuKind,omitempty"`
}

// StackSpec groups applications into one logical unit / network.
type StackSpec struct {
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// DatabaseSpec requests a logical database on a DatabaseInstance.
type DatabaseSpec struct {
	Engine    string `yaml:"engine" json:"engine"`                       // postgres|mysql|mariadb|redis
	Version   string `yaml:"version,omitempty" json:"version,omitempty"` // e.g. "16-alpine"
	Placement string `yaml:"placement,omitempty" json:"placement,omitempty"`
}

// VolumeSpec declares persistent storage.
type VolumeSpec struct {
	Size string `yaml:"size,omitempty" json:"size,omitempty"` // e.g. "5Gi" (0/empty = unbounded)
}

// RouteSpec binds one or more hostnames (and an optional path) to an
// application's port with a TLS mode — an HTTP routing rule. List every hostname
// the route should answer on, e.g. both example.com and www.example.com.
type RouteSpec struct {
	Hosts []string `yaml:"hosts" json:"hosts"`
	App   string   `yaml:"app" json:"app"` // target application name
	Port  int      `yaml:"port,omitempty" json:"port,omitempty"`
	Path  string   `yaml:"path,omitempty" json:"path,omitempty"`
	TLS   string   `yaml:"tls,omitempty" json:"tls,omitempty"` // acme|custom|off (default acme)
}

// DomainSpec declares an owned hostname/zone. The hostname is the resource's
// metadata.name (a real FQDN, e.g. shop.example.com). tls is the default
// certificate policy routes under it inherit; wildcard covers *.name too.
// Ownership verification (publishing the TXT token) is a runtime action.
type DomainSpec struct {
	TLS      string `yaml:"tls,omitempty" json:"tls,omitempty"` // acme|custom (default acme)
	Wildcard bool   `yaml:"wildcard,omitempty" json:"wildcard,omitempty"`
}

// SecretSpec is a write-only secret value. Value is never read back; the diff
// engine treats secrets as opaque (presence/absence only).
type SecretSpec struct {
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
	Generate bool   `yaml:"generate,omitempty" json:"generate,omitempty"`
	Length   int    `yaml:"length,omitempty" json:"length,omitempty"`
}

// ProjectSpec bundles a set of resources authored in one place. Child resources
// may be inlined; the parser flattens them into the document set.
type ProjectSpec struct {
	Description string     `yaml:"description,omitempty" json:"description,omitempty"`
	Resources   []Resource `yaml:"-" json:"resources,omitempty"`
}
