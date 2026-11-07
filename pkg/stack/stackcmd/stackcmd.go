// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package stackcmd holds host-side verb behaviour shared by the cobra CLI and the image's okapicli, so they can't drift.
package stackcmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/miabi-io/miabi/pkg/stack"
)

// UI is the front-end's output. The CLI colors it; the server image prints plainly.
type UI interface {
	Printf(format string, a ...any)
	Info(format string, a ...any)
	Success(format string, a ...any)
	Warn(format string, a ...any)
	// Confirm asks a yes/no question. It is never called when the caller passed Yes.
	Confirm(prompt string) bool
}

// SetupOptions is the flag surface of install/setup, already parsed.
type SetupOptions struct {
	Domain, AdminEmail, ACMEEmail, ControlURL    string
	Image, GatewayImage, RunnerImage, GomaConfig string
	RegistryHost, Subnet, InternalSubnet         string
	Registry, NoHostProc, Yes                    bool
	// IPv6 gives the shared app network and every workspace network a dual-stack address space.
	IPv6 bool
	// AdminPassword is the first admin's password; empty generates one. Only a fresh install uses it.
	AdminPassword string
	DefaultImage  func() string
}

type SetupResult struct {
	Manifest *stack.Manifest
	Path     string
	// NewInstall is false for a converge of an existing manifest.
	NewInstall bool
}

// Idempotent: an existing manifest keeps its secrets, since a regenerated DB password locks out the data.
func Setup(ctx context.Context, svc *stack.Service, path string, o SetupOptions, ui UI) (*SetupResult, error) {
	if o.AdminPassword != "" {
		if err := stack.CheckAdminPassword(o.AdminPassword); err != nil {
			return nil, err
		}
	}
	m, err := stack.Load(path)
	newInstall := false
	switch {
	case err == nil:
		ui.Info("Found an existing install (%s) — converging it to match.", path)
	case errors.Is(err, stack.ErrNotInstalled):
		def := ""
		if o.DefaultImage != nil {
			def = o.DefaultImage()
		}
		m, newInstall = stack.Defaults(def), true
		// Fresh installs only: an existing host may run an imported gateway Miabi could never hand the key to.
		if kerr := m.GenerateGatewayConfigKey(); kerr != nil {
			return nil, kerr
		}
	default:
		return nil, err
	}

	applySetupOptions(m, o)
	if o.AdminPassword != "" {
		if newInstall {
			m.Secrets.AdminPassword = o.AdminPassword
		} else if o.AdminPassword != m.Secrets.AdminPassword {
			// The password only seeds the first admin; the account already exists and keeps its own.
			ui.Warn("%s is ignored: this install already has an admin. Change the password in the panel.", stack.AdminPasswordEnv)
		}
	}

	if m.Domain == "" {
		return nil, errors.New("--domain is required (the panel's public hostname, e.g. miabi.example.com)")
	}
	if m.Images.Miabi == "" {
		return nil, errors.New("cannot determine which image to install — pass --image <repo>:<tag>")
	}
	// Normalize first so the plan is what will run, and generated secrets get persisted even if converge fails.
	if err := m.Normalize(); err != nil {
		return nil, err
	}

	pctx, pcancel := context.WithTimeout(ctx, 90*time.Second)
	defer pcancel()

	if err := svc.CheckDaemon(pctx); err != nil {
		return nil, err
	}

	// Postgres keeps its original password and pg_isready doesn't check credentials, so refuse and explain.
	if newInstall {
		if err := svc.CheckOrphanedData(pctx); err != nil {
			return nil, err
		}
	}

	// The gateway comes up last, so a taken :443 would otherwise leave a half-built stack.
	conflicts, perr := svc.CheckPorts(pctx)
	if perr != nil {
		return nil, perr
	}
	if len(conflicts) > 0 {
		return nil, stack.PortConflictError(conflicts)
	}

	printPlan(ui, m, path)
	for _, ref := range []string{m.Images.Miabi, m.Images.Postgres, m.Images.Redis, m.Images.Gateway} {
		warnFloatingTag(ui, ref)
	}
	recreate, err := svc.IPv6Upgrades(pctx, m)
	if err != nil {
		return nil, err
	}
	for _, n := range recreate {
		ui.Warn("Network %s will be recreated to turn IPv6 on. Every container on it is detached for a few seconds,\n"+
			"  so routed apps briefly stop answering. Existing workspace networks stay IPv4; new ones get IPv6.", n)
	}
	if !o.Yes && !ui.Confirm("Proceed?") {
		return nil, errors.New("cancelled")
	}
	if len(recreate) > 0 {
		svc.AllowNetworkRecreate()
	}

	// Persist first: the generated secrets exist nowhere else, and a live Postgres with an unknown password is stranded.
	if err := stack.Save(path, m); err != nil {
		return nil, err
	}
	if err := svc.Converge(ctx, m); err != nil {
		return nil, err
	}
	// Converge may have filled derived fields, so save again to match what actually ran.
	if err := stack.Save(path, m); err != nil {
		return nil, err
	}

	printResult(ui, m, path, newInstall, o.AdminPassword != "")
	return &SetupResult{Manifest: m, Path: path, NewInstall: newInstall}, nil
}

func applySetupOptions(m *stack.Manifest, o SetupOptions) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&m.Domain, o.Domain)
	set(&m.Secrets.AdminEmail, o.AdminEmail)
	set(&m.ACMEEmail, o.ACMEEmail)
	set(&m.ControlURL, o.ControlURL)
	set(&m.Images.Miabi, o.Image)
	set(&m.Images.Gateway, o.GatewayImage)
	set(&m.Gateway.Config, o.GomaConfig)
	// Without this the runner would fall back to the Go default and drift from install.sh's pin.
	set(&m.Images.Runner, o.RunnerImage)
	set(&m.Network.Subnet, o.Subnet)
	set(&m.InternalNetwork.Subnet, o.InternalSubnet)

	if o.Registry {
		m.Registry.Enabled = true
	}
	if o.NoHostProc {
		off := false
		m.HostProc = &off
	}
	if o.IPv6 {
		on := true
		m.Network.IPv6 = &on
	}
	// Naming the host is only meaningful if the registry runs, and ignoring the flag would be worse.
	if o.RegistryHost != "" {
		m.Registry.Host, m.Registry.Enabled = o.RegistryHost, true
	}
}

func printPlan(ui UI, m *stack.Manifest, path string) {
	ui.Printf("\nMiabi will install:\n\n")
	ui.Printf("  domain      %s  (%s)\n", m.Domain, m.WebURL)
	// Only when it is a second address.
	if cu := strings.TrimSpace(m.ControlURL); cu != "" && cu != m.WebURL {
		ui.Printf("  nodes       %s  — where agents and runners dial back\n", cu)
	}
	ui.Printf("  control     %s\n", m.Images.Miabi)
	ui.Printf("  gateway     %s\n", m.Images.Gateway)
	ui.Printf("  database    %s\n", m.Images.Postgres)
	ui.Printf("  cache       %s\n", m.Images.Redis)
	ipv6 := ""
	if m.Network.IPv6 != nil && *m.Network.IPv6 {
		ipv6 = " + IPv6"
	}
	ui.Printf("  network     %s (%s%s)  — apps + gateway\n", m.Network.Name, m.Network.Subnet, ipv6)
	ui.Printf("  private     %s (%s)  — control plane, database, cache\n",
		m.InternalNetwork.Name, m.InternalNetwork.Subnet)
	if m.Registry.Enabled {
		ui.Printf("  registry    %s\n", m.Registry.Host)
	}
	if m.HostProc != nil && !*m.HostProc {
		ui.Printf("  host /proc  not bound (host metrics fall back to the container's /proc)\n")
	}
	ui.Printf("  manifest    %s\n", path)
	ui.Printf("  ports       80, 443 (free)\n\n")
}

func printResult(ui UI, m *stack.Manifest, path string, newInstall, suppliedPassword bool) {
	ui.Printf("\n")
	ui.Success("Miabi is up at %s", m.WebURL)

	if cu := strings.TrimSpace(m.ControlURL); cu != "" && cu != m.WebURL {
		ui.Printf("\n  Nodes and runners reach it at %s\n", cu)
	}
	if newInstall && suppliedPassword {
		// Automated installs capture this output in CI and provisioning logs, so a chosen password is not echoed.
		ui.Printf("\n  Sign in with:\n    Email      %s\n    Password   the one set in %s\n",
			m.Secrets.AdminEmail, stack.AdminPasswordEnv)
		ui.Printf("\n  %s (mode 0600) holds the database password and the encryption key — BACK THAT FILE UP.\n"+
			"  Without it the encrypted secrets in the database cannot be read back.\n", path)
	} else if newInstall {
		ui.Printf("\n  Sign in with:\n    Email      %s\n    Password   %s\n",
			m.Secrets.AdminEmail, m.Secrets.AdminPassword)
		ui.Printf("\n  This password is shown only now. It lives in %s (mode 0600),\n"+
			"  together with the database password and the encryption key — BACK THAT FILE UP.\n"+
			"  Without it the encrypted secrets in the database cannot be read back.\n", path)
	}

	names := m.Domain
	if m.Registry.Enabled {
		names = fmt.Sprintf("%s and %s", m.Domain, m.Registry.Host)
	}
	ui.Printf("\n  Point %s at this host's public IP; the gateway obtains a certificate\n"+
		"  from Let's Encrypt on the first request.\n", names)

	if h := controlHost(m); h != "" {
		ui.Printf("\n  %s only has to resolve from your NODES — it is where agents and runners dial\n"+
			"  back, and it may be a private address the panel's own hostname never reaches.\n", h)
	}
	if m.Registry.Enabled {
		ui.Printf("\n  Registry: docker login %s   (use a Miabi account or an API token)\n", m.Registry.Host)
	}
}

func controlHost(m *stack.Manifest) string {
	cu := strings.TrimSpace(m.ControlURL)
	if cu == "" || cu == m.WebURL {
		return ""
	}
	u, err := url.Parse(cu)
	if err != nil || u.Host == "" {
		return ""
	}
	// Repeating the same name would read as a second DNS record to create.
	if u.Hostname() == m.Domain {
		return ""
	}
	return u.Host
}

// UpgradeOptions is the flag surface of upgrade/update, already parsed.
type UpgradeOptions struct {
	// Component is empty for the whole stack, else one component by name.
	Component string
	Image     string
	Version   string
	Yes       bool
	// DefaultImage is as in SetupOptions.
	DefaultImage func() string
}

// The "v" is stripped only before a digit, so a tag like "vnext" survives.
func normalizeVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if strings.ContainsAny(v, "/@:") {
		return "", fmt.Errorf("--version takes a version like 1.8.0 or v1.8.0, not an image reference (%q) — use --image for that", v)
	}
	if len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9' {
		v = v[1:]
	}
	return v, nil
}

// A floating tag pulled a month apart yields different images, which the manifest exists to prevent.
var floatingTags = map[string]bool{
	"latest": true, "edge": true, "stable": true, "nightly": true,
	"main": true, "master": true, "dev": true, "devel": true, "canary": true,
}

func tagOf(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return "" // digest-pinned: the most fixed form there is
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[colon+1:]
	}
	return "latest"
}

// Floating tags defeat rollback (prev == new) and drift detection (identical ref strings).
func warnFloatingTag(ui UI, ref string) {
	tag := tagOf(ref)
	if !floatingTags[tag] {
		return
	}
	ui.Warn("%s is a floating tag: a failed rollout cannot roll back (there is no distinct previous\n"+
		"    image to return to), and drift against it cannot be detected. Pin a version instead — --version 1.8.0.", ref)
}

// Keeping registry and repository makes --version correct on private registries and other components.
func retag(ref, version string) string {
	repo := ref
	// A digest pin has no tag to replace; the version supersedes it.
	if at := strings.LastIndex(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	// The tag separator is the last ":" after the last "/", so a registry port isn't mistaken for one.
	slash := strings.LastIndex(repo, "/")
	if colon := strings.LastIndex(repo, ":"); colon > slash {
		repo = repo[:colon]
	}
	return repo + ":" + version
}

func Upgrade(ctx context.Context, svc *stack.Service, path string, o UpgradeOptions, ui UI) error {
	if strings.TrimSpace(o.Image) != "" && strings.TrimSpace(o.Version) != "" {
		return errors.New("--image and --version are mutually exclusive: --version retags the current reference, --image replaces it outright")
	}
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}
	if err := m.Normalize(); err != nil {
		return err
	}

	wholeStack := o.Component == ""
	name := stack.ContainerControlPlane
	if !wholeStack {
		name = o.Component
	}

	// A typo should be reported as such, not as a missing image the caller never had to supply.
	pin, ok := m.ImagePin(name)
	if !ok {
		return fmt.Errorf("unknown component %q (have: %s)", name, strings.Join(svc.ComponentNames(m), ", "))
	}

	// Only the control plane follows the default image; bumping Postgres is a restart the operator must ask for.
	target := strings.TrimSpace(o.Image)
	switch {
	case target != "":
		// an explicit reference wins
	case o.Version != "":
		ver, verr := normalizeVersion(o.Version)
		if verr != nil {
			return verr
		}
		target = retag(*pin, ver)
	case !wholeStack:
		target = *pin
	case o.DefaultImage != nil:
		target = o.DefaultImage()
	}
	if target == "" {
		return errors.New("cannot determine which image to roll out — pass --version <x.y.z> or --image <repo>:<tag>")
	}
	warnFloatingTag(ui, target)
	prev := *pin

	if prev == target && !isDrifted(ctx, svc, name, target) {
		ui.Info("%s is already at %s.", name, target)
		if wholeStack {
			if err := convertManifest(path, m, ui); err != nil {
				return err
			}
			return convergeRest(ctx, svc, m, path)
		}
		// An earlier gateway-only upgrade may have left node gateways on the old value.
		if name == stack.ContainerGateway {
			warnNodeGatewayDrift(ctx, svc, ui, target)
		}
		return nil
	}

	ui.Printf("\nMiabi will roll out:\n\n  %-14s %s → %s\n\n", name, prev, target)
	if !o.Yes && !ui.Confirm("Proceed?") {
		return errors.New("cancelled")
	}

	// Convert only once the upgrade proceeds, so a cancelled one leaves no stray copy.
	if err := convertManifest(path, m, ui); err != nil {
		return err
	}

	*pin = target
	err = svc.Rollout(ctx, m, name, target, func(phase string, cause error) {
		if cause != nil {
			ui.Printf("  %-13s %v\n", phase, cause)
			return
		}
		ui.Printf("  %s\n", phase)
	})
	if err != nil {
		// The manifest must describe what runs, or the next converge would re-apply the failed upgrade.
		*pin = prev
		_ = stack.Save(path, m)
		return err
	}
	if err := stack.Save(path, m); err != nil {
		return err
	}
	if wholeStack {
		if err := convergeRest(ctx, svc, m, path); err != nil {
			return err
		}
	} else if name == stack.ContainerGateway {
		warnNodeGatewayDrift(ctx, svc, ui, target)
	}
	ui.Printf("\n")
	ui.Success("Upgraded. %s", m.WebURL)
	return nil
}

// Upgrade is the one command every host eventually runs, so it is where the shape changes; `setup` never converts.
func convertManifest(path string, m *stack.Manifest, ui UI) error {
	converted, err := stack.ConvertFile(path, m)
	if err != nil {
		return err
	}
	if converted {
		ui.Info("Converted %s to %s. The previous file is kept as %s%s — an older CLI cannot read the new one.",
			path, stack.APIVersion, path, stack.BackupSuffix)
	}
	return nil
}

// Warns rather than recreating the control plane: rolling one component should not restart another.
func warnNodeGatewayDrift(ctx context.Context, svc *stack.Service, ui UI, target string) {
	stale := svc.NodeGatewayImageDrift(ctx, target)
	if stale == "" {
		return
	}
	ui.Info("Node gateways still run %s — they follow this image through the control plane, which "+
		"keeps its own copy until it is recreated. Run `miabi upgrade` to roll the whole stack and "+
		"propagate it.", stale)
}

func convergeRest(ctx context.Context, svc *stack.Service, m *stack.Manifest, path string) error {
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	return stack.Save(path, m)
}

func isDrifted(ctx context.Context, svc *stack.Service, name, want string) bool {
	found, err := svc.Discover(ctx)
	if err != nil {
		return false
	}
	for _, c := range found {
		if c.Name == name {
			return c.Image != want
		}
	}
	return true // not running at all: rolling it out is exactly right
}

// Restarts without recreating, so on-disk changes like goma.yml are re-read but spec changes are not.
func Restart(ctx context.Context, svc *stack.Service, path, component string, yes bool, ui UI) error {
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}
	what := "the whole stack"
	if component != "" {
		what = component
	}
	if !yes && !ui.Confirm(fmt.Sprintf("Restart %s?", what)) {
		return errors.New("cancelled")
	}
	if err := svc.Restart(ctx, m, component); err != nil {
		return err
	}
	ui.Printf("\n")
	ui.Success("Restarted %s.", what)
	return nil
}

// The second half of `env set`, so a batch of edits costs one recreate per component.
func Apply(ctx context.Context, svc *stack.Service, path string, yes bool, ui UI) error {
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}
	pending, err := svc.Pending(ctx, m)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		ui.Success("The stack already matches the manifest.")
		return nil
	}
	ui.Printf("\nRecreating: %s\n\n", strings.Join(pending, ", "))
	if !yes && !ui.Confirm("Apply?") {
		return errors.New("cancelled")
	}
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	if err := stack.Save(path, m); err != nil {
		return err
	}
	ui.Success("Applied.")
	return nil
}

// The caller renders it; neither front-end decides what is in it.
type StatusReport struct {
	Manifest *stack.Manifest
	Path     string
	Found    []stack.Component
	// The host is installed; saying otherwise sends the operator hunting for a Compose stack.
	LegacyConfig error
	// Unmanaged is set when containers exist but no manifest does: a Compose or hand-rolled install.
	Unmanaged bool
	// Drift lists components running an image the manifest does not pin.
	Drift []string
}

// Status discovers the running stack and compares it against the manifest.
func Status(ctx context.Context, svc *stack.Service, path string) (*StatusReport, error) {
	m, mErr := stack.Load(path)
	found, err := svc.Discover(ctx)
	if err != nil {
		return nil, err
	}
	r := &StatusReport{Manifest: m, Path: path, Found: found}

	switch {
	case mErr != nil && len(found) == 0:
		return nil, WithInstallHint(mErr)
	case errors.Is(mErr, stack.ErrLegacyConfig):
		r.LegacyConfig = mErr
	case mErr != nil:
		r.Unmanaged = true
	}

	if m != nil {
		for _, c := range found {
			if want, ok := m.ImageFor(c.Name); ok && want != c.Image {
				r.Drift = append(r.Drift, fmt.Sprintf("  %s is running %s but the manifest says %s", c.Name, c.Image, want))
			}
		}
	}
	return r, nil
}

// With volumes removed the manifest goes too, since it describes an install that no longer exists.
func Uninstall(ctx context.Context, svc *stack.Service, path string, withVolumes, yes bool, ui UI, removeFile func(string) error) error {
	ui.Printf("This removes the Miabi stack's containers on this host.\n")
	if withVolumes {
		ui.Printf("\n  --volumes was given: the DATABASE AND ALL ITS DATA WILL BE DELETED.\n" +
			"  This cannot be undone. Your apps' own volumes are NOT touched.\n")
	} else {
		ui.Printf("  Data volumes are KEPT — re-install to bring the stack back.\n")
	}
	ui.Printf("\n")
	if !yes && !ui.Confirm("Proceed?") {
		return errors.New("cancelled")
	}
	if err := svc.Teardown(ctx, withVolumes); err != nil {
		return err
	}
	if withVolumes && removeFile != nil {
		if err := removeFile(path); err != nil {
			ui.Warn("could not remove %s: %v", path, err)
		}
	}
	ui.Printf("\n")
	ui.Success("Removed.")
	return nil
}

func WithInstallHint(err error) error {
	if errors.Is(err, stack.ErrNotInstalled) {
		// Non-root users can install under their own account, so only suggest sudo when needed.
		install := "sudo miabi setup --domain miabi.example.com"
		if os.Geteuid() != 0 {
			install = "miabi setup --domain miabi.example.com"
		}
		return fmt.Errorf("%w\n\n"+
			"  If you installed with Docker Compose, this is expected — that stack is managed\n"+
			"  by Compose, not by the CLI. Use `docker compose` in your install directory.\n\n"+
			"  To install:  %s", err, install)
	}
	return err
}
