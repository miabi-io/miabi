// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package enterprise is the commercial-edition seam: one interface, EE, gating licensed features.
// Community links a deny-all stub; the Enterprise build links the license-verifying one. The HTTP
// layer is identical, so a CE binary simply returns 402 and contains no verification code.
package enterprise

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
)

// Edition names. The product ships in two editions: the open-source Community
// edition and the commercial Enterprise edition.
const (
	EditionCommunity  = "community"
	EditionEnterprise = "enterprise"
)

// Entitlement flags. These are the capability surfaces a license unlocks; they
// are referenced by services as stable strings so adding a paid feature is "add
// a constant + call Require", never a change to the license format.
const (
	FlagMultiSSO           = "multi_sso"            // more than one OAuth/OIDC provider
	FlagSSOHiddenProvider  = "sso_hidden_provider"  // hide a provider from the public login page
	FlagSSOSAML            = "sso_saml"             // SAML 2.0 + enforced SSO
	FlagSCIM               = "scim"                 // SCIM 2.0 provisioning
	FlagCustomRoles        = "custom_roles"         // data-driven RBAC roles
	FlagResourcePolicies   = "resource_policies"    // per-resource permission grants
	FlagQuotaOverride      = "quota_override"       // per-workspace plan quota overrides
	FlagAuditLog           = "audit_log"            // view the audit log
	FlagAuditExport        = "audit_export"         // audit log export + retention
	FlagSIEMStream         = "siem_stream"          // live audit streaming to a SIEM
	FlagHA                 = "ha"                   // HA control plane
	FlagDR                 = "dr"                   // cross-region DR
	FlagPlatformBackup     = "platform_backup"      // admin platform (control-plane) backup & restore
	FlagWhiteLabel         = "white_label"          // full white-label branding
	FlagPrivateRegistry    = "private_registry"     // private workspace template registry
	FlagSecurityProfile    = "security_profile"     // restricted (force non-root UID) profile
	FlagRegistryS3         = "registry_s3"          // S3/MinIO storage for the built-in registry
	FlagPlatformRunners    = "platform_runners"     // admin-managed platform-shared runner pool
	FlagUserWorkspaceLimit = "user_workspace_limit" // per-user workspace-count override
	// FlagUserWorkspaceMembershipLimit gates the per-user override of how many
	// workspaces a user may join as a member.
	FlagUserWorkspaceMembershipLimit = "user_workspace_membership_limit"
	FlagSSOLDAP                      = "sso_ldap"         // LDAP / Active Directory authentication
	FlagAnalyticsExport              = "analytics_export" // workspace analytics export (CSV) + extended retention
)

// FlagInfo describes one entitlement flag for tooling and documentation.
type FlagInfo struct {
	Name string
	Desc string
}

// AllFlags is the canonical, ordered list of entitlement flags a license may
// grant.
var AllFlags = []FlagInfo{
	{FlagMultiSSO, "more than one OAuth/OIDC provider"},
	{FlagSSOHiddenProvider, "hide a provider from the public login page"},
	{FlagSSOSAML, "SAML 2.0 + enforced SSO"},
	{FlagSCIM, "SCIM 2.0 provisioning"},
	{FlagCustomRoles, "data-driven RBAC roles"},
	{FlagResourcePolicies, "per-resource permission grants"},
	{FlagQuotaOverride, "per-workspace plan quota overrides"},
	{FlagAuditLog, "view the audit log"},
	{FlagAuditExport, "audit log export + retention"},
	{FlagSIEMStream, "live audit streaming to a SIEM"},
	{FlagHA, "HA control plane"},
	{FlagDR, "cross-region DR"},
	{FlagPlatformBackup, "admin platform (control-plane) backup & restore"},
	{FlagWhiteLabel, "full white-label branding"},
	{FlagPrivateRegistry, "private workspace template registry"},
	{FlagSecurityProfile, "restricted (force non-root UID) profile"},
	{FlagRegistryS3, "S3/MinIO storage for the built-in registry"},
	{FlagPlatformRunners, "admin-managed platform-shared runner pool"},
	{FlagUserWorkspaceLimit, "per-user workspace-count override"},
	{FlagUserWorkspaceMembershipLimit, "per-user workspace-membership override"},
	{FlagSSOLDAP, "LDAP / Active Directory authentication"},
	{FlagAnalyticsExport, "workspace analytics export (CSV) + extended retention"},
}

// Commercial tier names. A tier is a preset bundle of flags and limits the issuer expands into a
// license; the runtime only reads the resolved flags/limits, so the tier is a label for the admin
// UI, support and pricing — and the single source of truth for what each plan includes.
const (
	TierProfessional = "professional" // freelancers & solo builders
	TierBusiness     = "business"     // small businesses & teams
	TierEnterprise   = "enterprise"   // enterprises: everything, unlimited
)

// Tier is a commercial plan preset: the flags it unlocks and the limits it sets.
type Tier struct {
	Name   string
	Desc   string
	Flags  []string
	Limits map[string]int
}

// Tiers is the canonical, ordered set of license presets, shared by the issuer, admin UI, docs and
// support tooling. Editing a tier here changes what its licenses grant everywhere. Professional is
// a lean SSO/audit bundle; Business adds directory SSO, RBAC and platform backup.
var Tiers = []Tier{
	{
		Name:  TierProfessional,
		Desc:  "Freelancers & solo builders",
		Flags: []string{FlagMultiSSO, FlagSSOHiddenProvider, FlagAuditLog, FlagRegistryS3},
		Limits: map[string]int{
			LimitNodeLimit: 3,
			LimitPlanLimit: 5,
		},
	},
	{
		Name: TierBusiness,
		Desc: "Small businesses & teams",
		Flags: []string{
			FlagMultiSSO, FlagSSOHiddenProvider, FlagSSOSAML, FlagSSOLDAP, FlagSCIM,
			FlagCustomRoles, FlagResourcePolicies, FlagQuotaOverride, FlagUserWorkspaceLimit,
			FlagUserWorkspaceMembershipLimit,
			FlagAuditLog, FlagAuditExport, FlagPlatformBackup,
			FlagPrivateRegistry, FlagRegistryS3, FlagPlatformRunners, FlagSecurityProfile,
		},
		Limits: map[string]int{
			LimitNodeLimit: 10,
			LimitPlanLimit: 15,
		},
	},
	{
		Name:  TierEnterprise,
		Desc:  "Enterprises: everything, unlimited",
		Flags: FlagNames(),
		Limits: map[string]int{
			LimitNodeLimit: -1,
			LimitPlanLimit: -1,
		},
	},
}

// TierByName returns the preset for a tier name (case-insensitive).
func TierByName(name string) (Tier, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, t := range Tiers {
		if t.Name == n {
			return t, true
		}
	}
	return Tier{}, false
}

// FlagNames returns just the flag identifiers from AllFlags, in canonical order.
func FlagNames() []string {
	out := make([]string, len(AllFlags))
	for i, f := range AllFlags {
		out[i] = f.Name
	}
	return out
}

// IsKnownFlag reports whether name is a defined entitlement flag.
func IsKnownFlag(name string) bool {
	for _, f := range AllFlags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// LimitNodeLimit is the entitlement limit key bounding active server nodes.
const LimitNodeLimit = "node_limit"

// LimitPlanLimit is the entitlement limit key bounding the platform plan catalog.
const LimitPlanLimit = "plan_limit"

// LimitAnalyticsRetentionDays is the entitlement limit key bounding how many days
// of workspace-analytics rollups are retained.
const LimitAnalyticsRetentionDays = "analytics_retention_days"

// CommunityAnalyticsRetentionDays caps analytics retention in Community. The dashboards work in
// full; only the history window is bounded. Extended retention is an Enterprise entitlement, and
// a license may set an explicit limit that wins.
const CommunityAnalyticsRetentionDays = 7

// CommunityPlanLimit caps the Community plan catalog at the three seeded plans: admins may edit or
// delete them, but adding a fourth requires an Enterprise license. A license may set an explicit
// plan_limit that always wins via PlanLimit.
const CommunityPlanLimit = 3

// CommunityNodeLimit is the number of registered nodes allowed in Community. -1 means unlimited:
// CE is not node-capped. A license may still set an explicit node_limit entitlement, which always
// wins via NodeLimit.
const CommunityNodeLimit = -1

// CommunityRunnerLimit is the number of platform-shared runners allowed without the
// platform_runners entitlement (-1 = unlimited). The shared pool is uncapped in Community; the
// entitlement is reserved for future scheduling. Per-workspace runners are always unlimited.
const CommunityRunnerLimit = -1

// Entitlements is the resolved, point-in-time view of the installed license.
// State is one of "valid" | "grace" | "degraded" | "none" (community).
type Entitlements struct {
	Edition string `json:"edition"`
	Tier    string `json:"tier,omitempty"` // commercial plan label (professional|business|enterprise)
	// InstallID / URL are the instance and host the license is bound to (empty = not bound by that
	// dimension; both empty = unlimited). When a binding doesn't match, State is StateBindingMismatch,
	// BindingError names the failed binding, and no flags or limits are granted.
	InstallID    string          `json:"install_id,omitempty"`
	URL          string          `json:"url,omitempty"`
	BindingError string          `json:"binding_error,omitempty"`
	State        string          `json:"state"`
	Customer     string          `json:"customer,omitempty"`
	LicenseID    string          `json:"license_id,omitempty"`
	Flags        map[string]bool `json:"flags"`
	Limits       map[string]int  `json:"limits"`
	NotAfter     *time.Time      `json:"not_after,omitempty"`
	GraceEnds    *time.Time      `json:"grace_ends,omitempty"`
}

// NodeLimit returns the resolved node cap (-1 = unlimited). An explicit
// node_limit in the license always wins; otherwise Community is bounded by
// CommunityNodeLimit and a paid edition with no explicit cap is unlimited.
func (e Entitlements) NodeLimit() int {
	if e.Limits != nil {
		if v, ok := e.Limits[LimitNodeLimit]; ok {
			return v
		}
	}
	if e.Edition == "" || e.Edition == EditionCommunity {
		return CommunityNodeLimit
	}
	return -1
}

// PlanLimit returns the resolved plan-catalog cap (-1 = unlimited). An explicit
// plan_limit in the license always wins; otherwise Community is bounded by
// CommunityPlanLimit and a paid edition with no explicit cap is unlimited.
func (e Entitlements) PlanLimit() int {
	if e.Limits != nil {
		if v, ok := e.Limits[LimitPlanLimit]; ok {
			return v
		}
	}
	if e.Edition == "" || e.Edition == EditionCommunity {
		return CommunityPlanLimit
	}
	return -1
}

// AnalyticsRetentionDays returns the resolved retention cap in days (-1 = unlimited). An explicit
// analytics_retention_days limit always wins; otherwise analytics_export lifts the cap, Community
// is bounded by CommunityAnalyticsRetentionDays, and a paid edition with neither is unlimited.
func (e Entitlements) AnalyticsRetentionDays() int {
	if e.Limits != nil {
		if v, ok := e.Limits[LimitAnalyticsRetentionDays]; ok {
			return v
		}
	}
	if e.Flags[FlagAnalyticsExport] {
		return -1
	}
	if e.Edition == "" || e.Edition == EditionCommunity {
		return CommunityAnalyticsRetentionDays
	}
	return -1
}

// ClampAnalyticsRetention resolves the effective retention days from the operator's configured
// value and the entitlement cap. A negative cap is unlimited, so the config is honored as-is;
// otherwise it is bounded by the cap, and a "keep forever" config (<= 0) clamps down to it.
func ClampAnalyticsRetention(configured, capDays int) int {
	if capDays < 0 {
		return configured
	}
	if configured <= 0 || configured > capDays {
		return capDays
	}
	return configured
}

// EE gates licensed features. All methods are safe to call on the CE stub.
type EE interface {
	// Entitlements resolves the current license against time.Now(), so the
	// grace/degrade transition takes effect without a restart.
	Entitlements() Entitlements
	// Has reports whether a flag is usable at runtime (true in valid, grace, and
	// degraded states for an entitled flag; always false in CE).
	Has(flag string) bool
	// Mutable reports whether a flag's configuration may be CHANGED (true only in
	// valid/grace; false once degraded, so paid config goes read-only on expiry).
	Mutable(flag string) bool
	// Require returns ErrLicenseRequired (community) or ErrEntitlementDenied (a
	// licensed install lacking this flag) when the flag is not usable; else nil.
	Require(flag string) error
	// RequireMutable is Require plus a read-only guard: it returns ErrLicenseExpired
	// when the flag is entitled but the license has degraded past grace.
	RequireMutable(flag string) error
	// Install verifies and persists a signed license token. requestHost is the host the admin is
	// installing from; a URL-bound license must match it or the configured instance URL, so a license
	// for another deployment cannot be installed even when the instance URL is unset.
	Install(ctx context.Context, token string, requestHost string) (Entitlements, error)
	// Remove deletes the installed license, reverting to community.
	Remove(ctx context.Context) error

	// InitSSO wires the enterprise SAML/SCIM handlers using core-provided
	// dependencies. Called once after core services exist. No-op in Community.
	InitSSO(deps SSODeps)
	// SAML returns the SAML 2.0 service-provider handler set, or nil in Community
	// / when sso_saml is not entitled. Routes return 402 when it is nil.
	SAML() SAMLProvider
	// SCIM returns the SCIM 2.0 provisioning handler set, or nil in Community.
	SCIM() SCIMProvider
	// LDAP returns the LDAP/Active-Directory authenticator, or nil in Community or when sso_ldap is
	// not entitled. The core login handler calls it after a failed local-password check — a
	// fall-through, so local accounts and the bootstrap admin keep working even with LDAP down.
	LDAP() LDAPAuthenticator
}

// LDAPIdentity is a directory user resolved by a successful bind: mapped email and name, the
// directory uid, and the group DNs the user belongs to. It is plain data, so the enterprise
// package never touches core auth, user or session code.
type LDAPIdentity struct {
	Email    string
	Name     string
	Username string   // directory uid: sAMAccountName / uid
	Groups   []string // group DNs (or CNs) the user is a member of
	Provider string   // the matched LDAPConfig name/slug
}

// LDAPAuthenticator binds credentials against the configured directories. It is
// implemented only in the enterprise build; the CE stub's LDAP() returns nil so
// the go-ldap client is never linked into Community.
type LDAPAuthenticator interface {
	// Authenticate escapes and binds username/password against every enabled LDAP config, first match
	// winning. It returns the resolved identity, ErrLDAPNoMatch when no config matched (the caller
	// falls through), or an error on a failed bind. It never binds on an empty password.
	Authenticate(ctx context.Context, username, password string) (LDAPIdentity, error)
	// TestConnection dials and binds the service account for one config and reports the result. A
	// failed connection, bind or search is OK=false rather than a Go error; a Go error means the
	// config wasn't found.
	TestConnection(ctx context.Context, configID uint) (LDAPTestResult, error)
}

// LDAPTestResult is the outcome of an admin "Test connection": OK plus a human
// message, or an Error string when the dial/bind/search failed.
type LDAPTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ErrLDAPNoMatch signals that no enabled LDAP config matched the identifier, so the core login
// handler falls through to its normal local result rather than treating it as a directory auth
// failure. It is a plain error, not a gate error, so it is never surfaced to the client.
var ErrLDAPNoMatch = errors.New("no matching LDAP configuration")

// SSOIdentity is an authenticated assertion the core turns into a session.
type SSOIdentity struct {
	Email    string
	Name     string
	Provider string // SAML config slug
}

// SSODeps are the core dependencies the enterprise SSO handlers need, passed as
// closures/values so the enterprise package never imports auth/session/user
// code (keeping the CE build free of it).
type SSODeps struct {
	DB        any    // *gorm.DB, type-asserted by the enterprise build
	BaseURL   string // public base URL for SP entity/ACS/metadata URLs
	SPKeyPEM  string // SP signing key (PEM); generated in-memory if empty
	SPCertPEM string // SP certificate (PEM); generated in-memory if empty
	// Login maps an authenticated SSO identity to a Miabi session and returns the
	// post-login redirect URL (the SPA callback carrying the token).
	Login func(ctx context.Context, ident SSOIdentity) (redirectURL string, err error)
	// Decrypt reverses the core crypto service (used to read the LDAP bind
	// password stored encrypted at rest). Passed as a closure so the enterprise
	// package never imports the crypto service directly.
	Decrypt func(stored string) (string, error)
}

// SAMLProvider is the SAML 2.0 service-provider handler set, implemented only in
// the enterprise build. Methods are okapi handlers mounted by the routes layer.
type SAMLProvider interface {
	Metadata(c *okapi.Context) error // SP metadata XML
	Login(c *okapi.Context) error    // initiate (redirect to IdP)
	ACS(c *okapi.Context) error      // assertion consumer service (callback)
}

// SCIMProvider is the SCIM 2.0 provisioning handler set.
type SCIMProvider interface {
	Users(c *okapi.Context) error
	Groups(c *okapi.Context) error
}

// gateError carries a stable machine code and the HTTP status handlers should
// use. The Code() method is picked up by the custom error handler for the
// {success,data,error} envelope; Status() drives entitlementAbort in handlers.
type gateError struct {
	code   string
	msg    string
	status int
}

func (e *gateError) Error() string { return e.msg }
func (e *gateError) Code() string  { return e.code }
func (e *gateError) Status() int   { return e.status }

// Sentinel gate errors. They are package-level pointers so errors.Is matches by
// identity, and they expose Code()/Status() for the API envelope mapping.
var (
	ErrLicenseRequired        = &gateError{code: "LICENSE_REQUIRED", msg: "this feature requires an Enterprise license", status: 402}
	ErrEntitlementDenied      = &gateError{code: "ENTITLEMENT_DENIED", msg: "your license does not include this feature", status: 403}
	ErrLicenseExpired         = &gateError{code: "LICENSE_EXPIRED", msg: "your license has expired; this feature is read-only", status: 402}
	ErrCommunityEdition       = &gateError{code: "COMMUNITY_EDITION", msg: "license management requires the Enterprise build", status: 402}
	ErrPlanLimitReached       = &gateError{code: "PLAN_LIMIT_REACHED", msg: "the plan-catalog limit for your edition has been reached; upgrade your license to add more plans", status: 402}
	ErrRunnerLimitReached     = &gateError{code: "RUNNER_LIMIT_REACHED", msg: "the platform-shared runner limit for your edition has been reached; upgrade your license to add more", status: 402}
	ErrLicenseBindingMismatch = &gateError{code: "LICENSE_BINDING_MISMATCH", msg: "this license is bound to a different deployment (Install ID or URL)", status: 402}
)

// StateBindingMismatch is the Entitlements.State when the license is valid and signed but bound to
// a different instance than this one, so no features are granted. It is surfaced with the bound
// values and BindingError so an admin can see exactly why enterprise features are off.
const StateBindingMismatch = "binding_mismatch"

// Binding-error reasons (Entitlements.BindingError).
const (
	BindingErrorInstallID = "install_id"
	BindingErrorURL       = "url"
)
