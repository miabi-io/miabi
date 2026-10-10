// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package components

// Component versions, bumped in the PR that changes the component: major when users must act (a
// migration, a manifest change, a removed option), minor for a feature, patch for a fix.
// scripts/components/check-component-versions.sh warns when a component's code changes without a bump here.
// The Web UI ships with Miabi and carries Miabi's own version.
const (
	GitOpsVersion         = "1.0.1"
	PipelineVersion       = "1.1.0"
	MarketplaceVersion    = "1.0.0"
	RegistryVersion       = "1.0.1"
	StorageClassesVersion = "1.0.0"
	AnalyticsVersion      = "1.0.0"
	HealthProbeVersion    = "1.0.0"
)
