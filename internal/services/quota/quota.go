// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package quota enforces per-workspace plan limits and capabilities. Resource
// services call CheckCreate before persisting, and Require before a gated
// action, so the same caps apply to every caller (web, CLI, CI, IaC). The
// effective limits resolve as: workspace override -> assigned plan -> default
// plan -> unlimited.
package quota

import (
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const (
	bytesPerMB  = 1024 * 1024
	nanoPerCore = 1_000_000_000
)

// Resource is a counted, per-workspace quota dimension.
type Resource string

const (
	ResourceApps                 Resource = "apps"
	ResourceDatabaseInstances    Resource = "database_instances"
	ResourceCronJobs             Resource = "cron_jobs"
	ResourceVolumes              Resource = "volumes"
	ResourceNetworks             Resource = "networks"
	ResourceAPIKeys              Resource = "api_keys"
	ResourceMembers              Resource = "members"
	ResourceDatabasesPerInstance Resource = "databases_per_instance"
	ResourceRunners              Resource = "runners"
	ResourceGPUs                 Resource = "gpus"
)

// Capability is a boolean plan feature gate.
type Capability string

const (
	CapCustomTLS            Capability = "custom_tls"
	CapPrivilegedHostMounts Capability = "privileged_host_mounts"
	CapShellExec            Capability = "shell_exec"
	CapSharedStorage        Capability = "shared_storage"
	CapDNSProviders         Capability = "dns_providers"
	CapCustomLabels         Capability = "custom_labels"
	CapPlatformRunners      Capability = "platform_runners"
	// CapCustomBuilder gates a per-app custom buildpack builder image. A custom
	// builder runs on the runner with docker-daemon access
	// (pack --trust-builder --docker-host inherit), so on shared/multi-tenant
	// runners it is an arbitrary-code vector; when not granted the platform default
	// builder is used instead.
	CapCustomBuilder Capability = "custom_builder"
	// CapGPU gates whether a workspace may request GPU devices for its apps.
	// Device passthrough is privileged, so this is a hard gate (and incompatible
	// with the restricted security profile); off by default like the others.
	CapGPU Capability = "gpu"
)

// ErrQuotaExceeded and ErrCapabilityDenied are the sentinels handlers map to a
// 403 response. QuotaError carries the structured detail for counted resources.
var (
	ErrQuotaExceeded    = errors.New("plan limit reached")
	ErrCapabilityDenied = errors.New("capability not allowed by plan")
)

// QuotaError is returned when creating one more of a counted resource would
// exceed the effective limit.
type QuotaError struct {
	Resource Resource
	Used     int
	Limit    int
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("plan limit reached: %d/%d %s", e.Used, e.Limit, e.Resource)
}
func (e *QuotaError) Is(target error) bool { return target == ErrQuotaExceeded }

// Code is the stable machine code surfaced in the API error envelope.
func (e *QuotaError) Code() string { return "QUOTA_EXCEEDED" }

// codedError wraps a quota/capability failure with a stable code and a sentinel
// for errors.Is matching.
type codedError struct {
	code string
	msg  string
	base error
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Code() string  { return e.code }
func (e *codedError) Unwrap() error { return e.base }

func quotaExceeded(format string, a ...any) error {
	return &codedError{code: "QUOTA_EXCEEDED", msg: fmt.Sprintf(format, a...), base: ErrQuotaExceeded}
}

// Limits is the fully-resolved effective limit set for a workspace. -1 means
// unlimited; 0 means none.
type Limits struct {
	MaxApps                   int    `json:"max_apps"`
	MaxDatabaseInstances      int    `json:"max_database_instances"`
	MaxCronJobs               int    `json:"max_cron_jobs"`
	MaxVolumes                int    `json:"max_volumes"`
	MaxNetworks               int    `json:"max_networks"`
	MaxAPIKeys                int    `json:"max_api_keys"`
	MaxMembers                int    `json:"max_members"`
	MaxDatabasesPerInstance   int    `json:"max_databases_per_instance"`
	MaxCPUCores               int    `json:"max_cpu_cores"`
	MaxMemoryMB               int    `json:"max_memory_mb"`
	MaxDatabaseInstanceSizeMB int    `json:"max_database_instance_size_mb"`
	MaxStorageMB              int    `json:"max_storage_mb"`
	MaxRunners                int    `json:"max_runners"`
	MaxGPUs                   int    `json:"max_gpus"`
	AllowCustomTLS            bool   `json:"allow_custom_tls"`
	AllowPrivilegedHostMounts bool   `json:"allow_privileged_host_mounts"`
	AllowShellExec            bool   `json:"allow_shell_exec"`
	AllowSharedStorage        bool   `json:"allow_shared_storage"`
	AllowDNSProviders         bool   `json:"allow_dns_providers"`
	AllowCustomLabels         bool   `json:"allow_custom_labels"`
	AllowPlatformRunners      bool   `json:"allow_platform_runners"`
	AllowCustomBuilder        bool   `json:"allow_custom_builder"`
	AllowGPU                  bool   `json:"allow_gpu"`
	SecurityProfile           string `json:"security_profile"`          // "default" | "restricted"
	AllowOfficialImageUser    bool   `json:"allow_official_image_user"` // exempt official-template apps from the restricted UID
}

func unlimited() Limits {
	return Limits{
		MaxApps: -1, MaxDatabaseInstances: -1, MaxCronJobs: -1, MaxVolumes: -1,
		MaxNetworks: -1, MaxAPIKeys: -1, MaxMembers: -1, MaxDatabasesPerInstance: -1,
		MaxCPUCores: -1, MaxMemoryMB: -1, MaxDatabaseInstanceSizeMB: -1, MaxStorageMB: -1,
		MaxRunners: -1, MaxGPUs: -1,
		AllowCustomTLS: true, AllowPrivilegedHostMounts: true, AllowShellExec: true,
		AllowSharedStorage: true, AllowDNSProviders: true, AllowCustomLabels: true,
		AllowPlatformRunners: true, AllowCustomBuilder: true, AllowGPU: true,
		SecurityProfile:        models.SecurityProfileDefault, // never restrict the platform workspace
		AllowOfficialImageUser: true,
	}
}

func limitsFromPlan(p *models.Plan) Limits {
	if p == nil {
		return unlimited()
	}
	return Limits{
		MaxApps: p.MaxApps, MaxDatabaseInstances: p.MaxDatabaseInstances, MaxCronJobs: p.MaxCronJobs,
		MaxVolumes: p.MaxVolumes, MaxNetworks: p.MaxNetworks, MaxAPIKeys: p.MaxAPIKeys, MaxMembers: p.MaxMembers,
		MaxDatabasesPerInstance: p.MaxDatabasesPerInstance, MaxCPUCores: p.MaxCPUCores, MaxMemoryMB: p.MaxMemoryMB,
		MaxDatabaseInstanceSizeMB: p.MaxDatabaseInstanceSizeMB, MaxStorageMB: p.MaxStorageMB,
		MaxRunners: p.MaxRunners, MaxGPUs: p.MaxGPUs,
		AllowCustomTLS: p.AllowCustomTLS, AllowPrivilegedHostMounts: p.AllowPrivilegedHostMounts,
		AllowShellExec:         p.AllowShellExec,
		AllowSharedStorage:     p.AllowSharedStorage,
		AllowDNSProviders:      p.AllowDNSProviders,
		AllowCustomLabels:      p.AllowCustomLabels,
		AllowPlatformRunners:   p.AllowPlatformRunners,
		AllowCustomBuilder:     p.AllowCustomBuilder,
		AllowGPU:               p.AllowGPU,
		SecurityProfile:        models.NormalizeSecurityProfile(p.SecurityProfile),
		AllowOfficialImageUser: p.AllowOfficialImageUser,
	}
}

func applyOverride(l Limits, o *models.WorkspaceQuota) Limits {
	if o == nil {
		return l
	}
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setb := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&l.MaxApps, o.MaxApps)
	set(&l.MaxDatabaseInstances, o.MaxDatabaseInstances)
	set(&l.MaxCronJobs, o.MaxCronJobs)
	set(&l.MaxVolumes, o.MaxVolumes)
	set(&l.MaxNetworks, o.MaxNetworks)
	set(&l.MaxAPIKeys, o.MaxAPIKeys)
	set(&l.MaxMembers, o.MaxMembers)
	set(&l.MaxDatabasesPerInstance, o.MaxDatabasesPerInstance)
	set(&l.MaxCPUCores, o.MaxCPUCores)
	set(&l.MaxMemoryMB, o.MaxMemoryMB)
	set(&l.MaxDatabaseInstanceSizeMB, o.MaxDatabaseInstanceSizeMB)
	set(&l.MaxStorageMB, o.MaxStorageMB)
	set(&l.MaxRunners, o.MaxRunners)
	set(&l.MaxGPUs, o.MaxGPUs)
	setb(&l.AllowCustomTLS, o.AllowCustomTLS)
	setb(&l.AllowPrivilegedHostMounts, o.AllowPrivilegedHostMounts)
	setb(&l.AllowShellExec, o.AllowShellExec)
	setb(&l.AllowSharedStorage, o.AllowSharedStorage)
	setb(&l.AllowDNSProviders, o.AllowDNSProviders)
	setb(&l.AllowCustomLabels, o.AllowCustomLabels)
	setb(&l.AllowPlatformRunners, o.AllowPlatformRunners)
	setb(&l.AllowCustomBuilder, o.AllowCustomBuilder)
	setb(&l.AllowGPU, o.AllowGPU)
	setb(&l.AllowOfficialImageUser, o.AllowOfficialImageUser)
	if o.SecurityProfile != nil {
		l.SecurityProfile = models.NormalizeSecurityProfile(*o.SecurityProfile)
	}
	return l
}

func resourceLimit(l Limits, r Resource) int {
	switch r {
	case ResourceApps:
		return l.MaxApps
	case ResourceDatabaseInstances:
		return l.MaxDatabaseInstances
	case ResourceCronJobs:
		return l.MaxCronJobs
	case ResourceVolumes:
		return l.MaxVolumes
	case ResourceNetworks:
		return l.MaxNetworks
	case ResourceAPIKeys:
		return l.MaxAPIKeys
	case ResourceMembers:
		return l.MaxMembers
	case ResourceDatabasesPerInstance:
		return l.MaxDatabasesPerInstance
	case ResourceRunners:
		return l.MaxRunners
	case ResourceGPUs:
		return l.MaxGPUs
	default:
		return -1
	}
}

// EditionGate reports whether a paid entitlement flag is usable at runtime.
// Satisfied by enterprise.EE; injected so the resolver can clamp the Enterprise-only
// restricted security profile back to the default in Community or once a license
// lapses.
type EditionGate interface {
	Has(flag string) bool
}

// flagSecurityProfile is a local copy of enterprise.FlagSecurityProfile so this
// package needs no enterprise import.
const flagSecurityProfile = "security_profile"

// Service resolves and enforces effective workspace limits.
type Service struct {
	plans     *repositories.PlanRepository
	overrides *repositories.WorkspaceQuotaRepository
	apps      *repositories.ApplicationRepository
	volumes   *repositories.VolumeRepository
	dbs       *repositories.DatabaseRepository
	enforce   bool
	ee        EditionGate
}

func NewService(plans *repositories.PlanRepository, overrides *repositories.WorkspaceQuotaRepository, apps *repositories.ApplicationRepository, volumes *repositories.VolumeRepository, dbs *repositories.DatabaseRepository, enforce bool) *Service {
	return &Service{plans: plans, overrides: overrides, apps: apps, volumes: volumes, dbs: dbs, enforce: enforce}
}

// SetEdition wires the entitlement gate used to clamp Enterprise-only workload
// security policies (nil-safe; nil treats every paid flag as unlicensed, i.e.
// shell access always allowed and the restricted profile never applied).
func (s *Service) SetEdition(g EditionGate) { s.ee = g }

// entitled reports whether a paid flag is usable. False when no gate is wired
// (Community) so the resolver falls back to the permissive default.
func (s *Service) entitled(flag string) bool { return s.ee != nil && s.ee.Has(flag) }

// Enabled reports whether enforcement is active.
func (s *Service) Enabled() bool { return s != nil && s.enforce }

func (s *Service) effectivePlan(workspaceID uint) *models.Plan {
	if p, err := s.plans.PlanForWorkspace(workspaceID); err == nil && p.IsActive {
		return p
	}
	if p, err := s.plans.FindDefault(); err == nil {
		return p
	}
	return nil
}

// EffectivePlanName returns the name of the workspace's resolved plan, or ""
// when no plan applies (unlimited).
func (s *Service) EffectivePlanName(workspaceID uint) string {
	if s == nil {
		return ""
	}
	if p := s.effectivePlan(workspaceID); p != nil {
		return p.Name
	}
	return ""
}

// EffectiveLimits resolves a workspace's limits (override -> plan -> default ->
// unlimited). Safe on a nil service (returns unlimited).
func (s *Service) EffectiveLimits(workspaceID uint) Limits {
	if s == nil {
		return unlimited()
	}
	l := limitsFromPlan(s.effectivePlan(workspaceID))
	if s.overrides != nil {
		if o, err := s.overrides.FindByWorkspace(workspaceID); err == nil {
			l = applyOverride(l, o)
		}
	}
	// The restricted security profile is Enterprise-only: clamp it back to the
	// default when not licensed, so a Community install (or a lapsed license)
	// behaves as if the plan never selected it, without rewriting stored plans.
	if !s.entitled(flagSecurityProfile) {
		l.SecurityProfile = models.SecurityProfileDefault
	}
	return l
}

// CheckCreate returns a QuotaError when creating one more of r would exceed the
// effective limit. currentCount is the live count the caller already has.
func (s *Service) CheckCreate(workspaceID uint, r Resource, currentCount int) error {
	if !s.Enabled() {
		return nil
	}
	limit := resourceLimit(s.EffectiveLimits(workspaceID), r)
	if limit < 0 {
		return nil
	}
	if currentCount >= limit {
		return &QuotaError{Resource: r, Used: currentCount, Limit: limit}
	}
	return nil
}

// RestrictedProfile reports whether a workspace's effective security profile is
// "restricted" (force non-root). It is always false when enforcement is disabled
// (single-tenant mode): the per-plan gate does not apply there, leaving the
// decision to the server-level default. Nil-safe (returns false).
func (s *Service) RestrictedProfile(workspaceID uint) bool {
	if !s.Enabled() {
		return false
	}
	return s.EffectiveLimits(workspaceID).SecurityProfile == models.SecurityProfileRestricted
}

// AllowOfficialImageUser reports whether the workspace's effective plan lets apps
// installed from an official marketplace template keep the image's own default
// user even under the restricted security profile. Only meaningful alongside a
// restricted profile (see RestrictedProfile); nil-safe. Permissive (true) when
// enforcement is disabled, mirroring the single-tenant "no gating" stance.
func (s *Service) AllowOfficialImageUser(workspaceID uint) bool {
	if !s.Enabled() {
		return true
	}
	return s.EffectiveLimits(workspaceID).AllowOfficialImageUser
}

// Require returns ErrCapabilityDenied when the workspace's effective limits do
// not grant the capability.
func (s *Service) Require(workspaceID uint, c Capability) error {
	if !s.Enabled() {
		return nil
	}
	l := s.EffectiveLimits(workspaceID)
	ok := true
	switch c {
	case CapCustomTLS:
		ok = l.AllowCustomTLS
	case CapPrivilegedHostMounts:
		ok = l.AllowPrivilegedHostMounts
	case CapShellExec:
		ok = l.AllowShellExec
	case CapSharedStorage:
		ok = l.AllowSharedStorage
	case CapDNSProviders:
		ok = l.AllowDNSProviders
	case CapCustomLabels:
		ok = l.AllowCustomLabels
	case CapPlatformRunners:
		ok = l.AllowPlatformRunners
	case CapCustomBuilder:
		ok = l.AllowCustomBuilder
	case CapGPU:
		ok = l.AllowGPU
	}
	if ok {
		return nil
	}
	return &codedError{code: "CAPABILITY_DENIED", msg: fmt.Sprintf("capability not allowed by plan: %s", c), base: ErrCapabilityDenied}
}

// CustomBuilderAllowed reports whether a workspace may use a custom buildpack
// builder image (the plan capability, or true when plan enforcement is off). Used
// by the deploy worker as defense-in-depth: a builder set while granted must stop
// being honored if the capability is later revoked (e.g. a plan downgrade).
func (s *Service) CustomBuilderAllowed(workspaceID uint) bool {
	return s.Require(workspaceID, CapCustomBuilder) == nil
}

// CheckComputeAdd verifies that adding the requested CPU (nanoCPUs) and memory
// (bytes) keeps the workspace's aggregate app compute within the plan caps. On
// an update, excludeAppID drops the app's current contribution from the sum.
func (s *Service) CheckComputeAdd(workspaceID uint, addNanoCPUs, addMemBytes int64, excludeAppID uint) error {
	if !s.Enabled() {
		return nil
	}
	l := s.EffectiveLimits(workspaceID)
	if l.MaxCPUCores < 0 && l.MaxMemoryMB < 0 {
		return nil
	}
	curCPU, curMem, err := s.apps.SumResourcesByWorkspace(workspaceID, excludeAppID)
	if err != nil {
		return nil // fail open on a count error
	}
	if l.MaxCPUCores >= 0 {
		capNano := int64(l.MaxCPUCores) * nanoPerCore
		if curCPU+addNanoCPUs > capNano {
			return quotaExceeded("workspace CPU %.2f cores exceeds the %d-core limit", float64(curCPU+addNanoCPUs)/nanoPerCore, l.MaxCPUCores)
		}
	}
	if l.MaxMemoryMB >= 0 {
		capBytes := int64(l.MaxMemoryMB) * bytesPerMB
		if curMem+addMemBytes > capBytes {
			return quotaExceeded("workspace memory %d MB exceeds the %d MB limit", (curMem+addMemBytes)/bytesPerMB, l.MaxMemoryMB)
		}
	}
	return nil
}

// CheckGPURequest verifies that granting `requested` GPU units to an app keeps
// the workspace's aggregate GPU allocation within the MaxGPUs cap. The current
// allocation is the sum of GPUCount × replicas over the workspace's *running*
// apps; excludeAppID drops the app being (re)deployed from that sum so a redeploy
// does not double-count its own held units. A stopped app holds nothing.
func (s *Service) CheckGPURequest(workspaceID uint, requested int, excludeAppID uint) error {
	if !s.Enabled() || requested <= 0 {
		return nil
	}
	limit := resourceLimit(s.EffectiveLimits(workspaceID), ResourceGPUs)
	if limit < 0 {
		return nil // unlimited
	}
	cur, err := s.apps.SumRunningGPUsByWorkspace(workspaceID, excludeAppID)
	if err != nil {
		return nil // fail open on a count error
	}
	if int(cur)+requested > limit {
		return &QuotaError{Resource: ResourceGPUs, Used: int(cur) + requested, Limit: limit}
	}
	return nil
}

// CheckInstanceSize verifies a single DB instance's declared data-volume size
// (bytes) is within the per-instance cap. A 0 size (unspecified) is allowed.
func (s *Service) CheckInstanceSize(workspaceID uint, sizeBytes int64) error {
	if !s.Enabled() || sizeBytes <= 0 {
		return nil
	}
	l := s.EffectiveLimits(workspaceID)
	if l.MaxDatabaseInstanceSizeMB < 0 {
		return nil
	}
	capBytes := int64(l.MaxDatabaseInstanceSizeMB) * bytesPerMB
	if sizeBytes > capBytes {
		return quotaExceeded("requested %d MB exceeds the per-instance size limit of %d MB", sizeBytes/bytesPerMB, l.MaxDatabaseInstanceSizeMB)
	}
	return nil
}

// CheckStorageAdd verifies that adding addBytes of declared storage keeps the
// workspace's aggregate (volumes + DB instance data volumes) within the cap.
func (s *Service) CheckStorageAdd(workspaceID uint, addBytes int64) error {
	if !s.Enabled() || addBytes <= 0 {
		return nil
	}
	l := s.EffectiveLimits(workspaceID)
	if l.MaxStorageMB < 0 {
		return nil
	}
	volTotal, _ := s.volumes.SumSizeByWorkspace(workspaceID)
	dbTotal, _ := s.dbs.SumVolumeSizeByWorkspace(workspaceID)
	cur := volTotal + dbTotal
	capBytes := int64(l.MaxStorageMB) * bytesPerMB
	if cur+addBytes > capBytes {
		return quotaExceeded("workspace storage %d MB exceeds the %d MB limit", (cur+addBytes)/bytesPerMB, l.MaxStorageMB)
	}
	return nil
}
