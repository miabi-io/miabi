// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package components describes the parts of Miabi that are versioned on their own, so the About
// page can show how far each has come independently of the Miabi release number.
package components

import (
	"time"

	"github.com/miabi-io/miabi/internal/config"
)

// Stability labels how settled a component is.
type Stability string

const (
	StabilityPreview Stability = "preview"
	StabilityBeta    Stability = "beta"
	StabilityStable  Stability = "stable"
)

// Status is whether a component can be used on this instance.
type Status string

const (
	StatusOn         Status = "on"
	StatusOff        Status = "off"        // turned off in configuration or settings, or not in this build
	StatusEnterprise Status = "enterprise" // needs an Enterprise license this instance lacks
)

// historyURL is the repository's commit history, filtered to a component's code by appending a path.
const historyURL = "https://github.com/miabi-io/miabi/commits/main/"

// Component is one independently versioned part of Miabi.
type Component struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Stability Stability `json:"stability"`
	// FormatVersion is the apiVersion of the files users write for it, when it has any.
	FormatVersion string `json:"format_version,omitempty"`
	Status        Status `json:"status"`
	// Catalog is the synced marketplace catalog, on the Marketplace component only.
	Catalog      *Catalog `json:"catalog,omitempty"`
	ChangelogURL string   `json:"changelog_url"`
}

// Catalog reports the synced marketplace catalog: its template count, when it last synced (zero
// before the first sync), and when the marketplace generated it (empty when it does not say).
type Catalog struct {
	Templates   int        `json:"templates"`
	SyncedAt    *time.Time `json:"synced_at,omitempty"`
	GeneratedAt string     `json:"generated_at,omitempty"`
}

// Probes answer the runtime questions a component's status depends on. Any may be nil, which reads
// as "on" (or no detail), so a partially wired instance still lists every component.
type Probes struct {
	RegistryEnabled  func() bool
	AnalyticsEnabled func() bool
	// HealthProbeBundled reports whether this build embeds the probe binaries.
	HealthProbeBundled func() bool
	Catalog            func() Catalog
}

// Service lists the components with their live status.
type Service struct {
	probes Probes
}

func NewService(p Probes) *Service { return &Service{probes: p} }

// List returns every component, in display order.
func (s *Service) List() []Component {
	return []Component{
		{
			ID: "web", Name: "Web UI", Version: config.Version, Stability: StabilityStable,
			Status: StatusOn, ChangelogURL: historyURL + "web",
		},
		{
			ID: "gitops", Name: "GitOps", Version: GitOpsVersion, Stability: StabilityStable,
			FormatVersion: "miabi.io/v1", Status: StatusOn,
			ChangelogURL: historyURL + "internal/services/gitops",
		},
		{
			ID: "pipeline", Name: "Pipeline", Version: PipelineVersion, Stability: StabilityBeta,
			FormatVersion: "miabi.io/v1", Status: StatusOn,
			ChangelogURL: historyURL + "internal/services/pipeline",
		},
		{
			ID: "marketplace", Name: "Marketplace", Version: MarketplaceVersion, Stability: StabilityStable,
			FormatVersion: "miabi.io/v1", Status: StatusOn, Catalog: s.catalog(),
			ChangelogURL: historyURL + "internal/services/marketplace",
		},
		{
			// events/v1 is the request-event format Goma gateways, the agent's forwarder and the
			// rollup worker agree on; a change that breaks older gateways bumps it.
			ID: "analytics", Name: "Web Analytics", Version: AnalyticsVersion, Stability: StabilityBeta,
			FormatVersion: "events/v1", Status: onIf(s.probes.AnalyticsEnabled, StatusOff),
			ChangelogURL: historyURL + "internal/services/analytics",
		},
		{
			ID: "registry", Name: "Container Registry", Version: RegistryVersion, Stability: StabilityStable,
			Status:       onIf(s.probes.RegistryEnabled, StatusOff),
			ChangelogURL: historyURL + "internal/services/registryserver",
		},
		{
			ID: "storage-classes", Name: "Storage classes", Version: StorageClassesVersion, Stability: StabilityBeta,
			// Every edition may register classes; a license only lifts the Community cap.
			Status:       StatusOn,
			ChangelogURL: historyURL + "internal/services/storageclass",
		},
		{
			// Without the binaries (no `make build-probe`), HTTP checks fall back to a shell probe.
			ID: "healthprobe", Name: "Health probe", Version: HealthProbeVersion, Stability: StabilityBeta,
			Status:       onIf(s.probes.HealthProbeBundled, StatusOff),
			ChangelogURL: historyURL + "cmd/healthprobe",
		},
		{
			ID: "sealed-secrets", Name: "Sealed secrets", Version: SealedSecretsVersion, Stability: StabilityBeta,
			FormatVersion: "sealed:v1", Status: StatusOn,
			ChangelogURL: historyURL + "internal/services/sealing",
		},
	}
}

func onIf(probe func() bool, otherwise Status) Status {
	if probe == nil || probe() {
		return StatusOn
	}
	return otherwise
}

func (s *Service) catalog() *Catalog {
	if s.probes.Catalog == nil {
		return nil
	}
	c := s.probes.Catalog()
	return &c
}
