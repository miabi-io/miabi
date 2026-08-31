// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

// Package stackcmd is the behaviour behind the host-side verbs — install/converge, upgrade,
// restart, status and uninstall — with no command-line framework attached.
//
// Two front-ends drive it, and they must not drift:
//
//   - `miabi setup|upgrade|stack …` in the CLI (cobra), the normal path;
//   - `docker run miabi/miabi:<tag> install …` on the server image (okapicli), which is how a
//     user without root installs — the image runs as uid 10001 with the host's docker group, so
//     nothing has to be written to /usr/local/bin.
//
// Each front-end owns only its flag parsing and its rendering; everything that decides what
// happens to a host lives here, once.
package stackcmd

import (
	"context"
	"errors"
	"fmt"
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

	// DefaultImage supplies the control-plane image for a FRESH install when Image is empty. The
	// two front-ends answer this differently and neither answer generalizes: the server image
	// inspects the container it is running in (so `docker run miabi/miabi:1.8.0 install` lands
	// exactly 1.8.0, private registry included), while the CLI has no self-container and uses its
	// build stamp. Return "" when it cannot be determined.
	DefaultImage func() string
}

// SetupResult reports what a Setup did, so the caller can print its own next-steps hint — the one
// piece of the output that legitimately differs between the two front-ends.
type SetupResult struct {
	Manifest *stack.Manifest
	Path     string
	// NewInstall is false for a converge of an existing manifest.
	NewInstall bool
}

// Setup installs the stack, or converges an existing install to its manifest. It is idempotent:
// run against an existing manifest it keeps the stored secrets, because a regenerated database
// password would lock the new control plane out of its own data.
func Setup(ctx context.Context, svc *stack.Service, path string, o SetupOptions, ui UI) (*SetupResult, error) {
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
	default:
		return nil, err
	}

	applySetupOptions(m, o)

	if m.Domain == "" {
		return nil, errors.New("--domain is required (the panel's public hostname, e.g. miabi.example.com)")
	}
	if m.Images.Miabi == "" {
		return nil, errors.New("cannot determine which image to install — pass --image <repo>:<tag>")
	}
	// Normalize before showing the plan, so what is printed is what will run — including the
	// generated secrets, which must be persisted even if converge later fails. A stack whose
	// containers exist but whose password was never written down is unrecoverable.
	if err := m.Normalize(); err != nil {
		return nil, err
	}

	pctx, pcancel := context.WithTimeout(ctx, 90*time.Second)
	defer pcancel()

	// A fresh install onto an EXISTING Postgres volume can never work: Postgres keeps the password
	// its data dir was created with. Nothing downstream catches it (pg_isready does not check
	// credentials), so refuse and say how to recover.
	if newInstall {
		if err := svc.CheckOrphanedData(pctx); err != nil {
			return nil, err
		}
	}

	// Check the ports BEFORE creating anything. The gateway comes up last, so without this the
	// install gets through Postgres, Redis and the control plane and only then dies because
	// something else owns :443 — leaving a half-built stack.
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
	if !o.Yes && !ui.Confirm("Proceed?") {
		return nil, errors.New("cancelled")
	}

	// Persist the manifest BEFORE creating anything. The secrets are generated here and exist
	// nowhere else; if converge dies halfway the operator still has the database password. The
	// reverse order can strand a live Postgres whose password nobody knows.
	if err := stack.Save(path, m); err != nil {
		return nil, err
	}
	if err := svc.Converge(ctx, m); err != nil {
		return nil, err
	}
	// Converge may have filled in derived fields (docker GID, defaults). Persist again so the file
	// matches what actually ran.
	if err := stack.Save(path, m); err != nil {
		return nil, err
	}

	printResult(ui, m, path, newInstall)
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
	// install.sh pins every image in one place and CI bumps them there; without this the runner
	// would silently fall back to the Go default and drift from that pin.
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
	// --registry-host implies --registry: naming the host is only meaningful if the registry runs,
	// and silently ignoring the flag would be worse than assuming.
	if o.RegistryHost != "" {
		m.Registry.Host, m.Registry.Enabled = o.RegistryHost, true
	}
}

func printPlan(ui UI, m *stack.Manifest, path string) {
	ui.Printf("\nMiabi will install:\n\n")
	ui.Printf("  domain      %s  (%s)\n", m.Domain, m.WebURL)
	ui.Printf("  control     %s\n", m.Images.Miabi)
	ui.Printf("  gateway     %s\n", m.Images.Gateway)
	ui.Printf("  database    %s\n", m.Images.Postgres)
	ui.Printf("  cache       %s\n", m.Images.Redis)
	ui.Printf("  network     %s (%s)  — apps + gateway\n", m.Network.Name, m.Network.Subnet)
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

func printResult(ui UI, m *stack.Manifest, path string, newInstall bool) {
	ui.Printf("\n")
	ui.Success("Miabi is up at %s", m.WebURL)
	if newInstall {
		ui.Printf("\n  Sign in with:\n    %s\n    %s\n", m.Secrets.AdminEmail, m.Secrets.AdminPassword)
		ui.Printf("\n  This password is shown only now. It lives in %s (mode 0600),\n"+
			"  together with the database password and the encryption key — BACK THAT FILE UP.\n"+
			"  Without it the encrypted secrets in the database cannot be read back.\n", path)
	}
	// The registry is served on its OWN hostname with its own certificate, so it needs its own DNS
	// record. Without one it simply never works, and the failure surfaces far from here — as a
	// docker push that cannot resolve the host.
	names := m.Domain
	if m.Registry.Enabled {
		names = fmt.Sprintf("%s and %s", m.Domain, m.Registry.Host)
	}
	ui.Printf("\n  Point %s at this host's public IP; the gateway obtains a certificate\n"+
		"  from Let's Encrypt on the first request.\n", names)
	if m.Registry.Enabled {
		ui.Printf("\n  Registry: docker login %s   (use a Miabi account or an API token)\n", m.Registry.Host)
	}
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

// normalizeVersion accepts a Docker tag or a Git tag: releases are cut as v1.8.0 and published as
// 1.8.0, and an operator reasonably types either. The "v" is stripped only when a digit follows, so
// a real tag like "vnext" survives.
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

// floatingTags never identify a fixed build. Pulling one twice a month apart gives two different
// images under the same name, which is exactly what the manifest exists to prevent.
var floatingTags = map[string]bool{
	"latest": true, "edge": true, "stable": true, "nightly": true,
	"main": true, "master": true, "dev": true, "devel": true, "canary": true,
}

// tagOf returns the tag on a reference, "" for a digest pin, and "latest" when none is given —
// which is what Docker resolves a bare repository to.
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

// warnFloatingTag flags a reference that cannot pin a build. This is not style advice — it breaks
// two guarantees the rollout depends on:
//
//   - saferollout skips the rollback when the previous reference equals the new one, so a failed
//     upgrade of :latest -> :latest has nothing to return to and leaves the component down;
//   - drift detection compares reference strings, so an old :latest and a new :latest look
//     identical and `upgrade` reports "already at" without doing anything.
func warnFloatingTag(ui UI, ref string) {
	tag := tagOf(ref)
	if !floatingTags[tag] {
		return
	}
	ui.Warn("%s is a floating tag: a failed rollout cannot roll back (there is no distinct previous\n"+
		"    image to return to), and drift against it cannot be detected. Pin a version instead — --version 1.8.0.", ref)
}

// retag swaps the tag on an image reference, keeping registry and repository — which is what makes
// --version correct on a private registry, and on components that are not miabi/miabi.
func retag(ref, version string) string {
	repo := ref
	// A digest pin has no tag to replace; the version supersedes it.
	if at := strings.LastIndex(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	// The tag separator is the last ":" AFTER the last "/", so the port in a reference like
	// registry.example.com:5000/miabi is not mistaken for one.
	slash := strings.LastIndex(repo, "/")
	if colon := strings.LastIndex(repo, ":"); colon > slash {
		repo = repo[:colon]
	}
	return repo + ":" + version
}

// Upgrade rolls a component to a newer image, rolling back automatically if the new one never
// becomes healthy. With no component named it moves the control plane and re-converges the rest.
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

	// Validate the component BEFORE resolving an image: a typo should say so, not complain about
	// an image the caller never had to supply.
	pin, ok := m.ImagePin(name)
	if !ok {
		return fmt.Errorf("unknown component %q (have: %s)", name, strings.Join(svc.ComponentNames(m), ", "))
	}

	// Only the control plane follows the default image. Naming another component rolls it out to
	// whatever the manifest pins, unless --image/--version says otherwise: bumping Postgres is a
	// database restart the operator has to ask for — and it needs no default image at all.
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
			return convergeRest(ctx, svc, m, path)
		}
		return nil
	}

	ui.Printf("\nMiabi will roll out:\n\n  %-14s %s → %s\n\n", name, prev, target)
	if !o.Yes && !ui.Confirm("Proceed?") {
		return errors.New("cancelled")
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
		// Put the pin back. The manifest must describe what is RUNNING: a rollback restored the old
		// image, so recording the new one would leave the file lying — and the next converge would
		// reconcile reality to match the lie, re-applying the upgrade that just failed.
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
	}
	ui.Printf("\n")
	ui.Success("Upgraded. %s", m.WebURL)
	return nil
}

// convergeRest reconciles the components the rollout did not touch, so a manifest edit (a new
// gateway pin, a rotated secret) takes effect without a second command. A no-op when nothing changed.
func convergeRest(ctx context.Context, svc *stack.Service, m *stack.Manifest, path string) error {
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	return stack.Save(path, m)
}

// isDrifted reports whether the running container is NOT on the image the manifest pins — in which
// case "already at that version" is false even though the pin says so, and the rollout should proceed.
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

// Restart restarts containers WITHOUT recreating them, so they re-read what is on disk. Editing the
// gateway's goma.yml is the obvious case (Goma watches its providers directory, not its base
// config); a spec change is the obvious non-case, and Service.Restart says so.
func Restart(ctx context.Context, svc *stack.Service, path, component string, yes bool, ui UI) error {
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}
	what := "the whole stack"
	if component != "" {
		what = component
	}
	// A whole-stack restart takes the panel down for as long as it takes to come back; one
	// component is a smaller ask. Either way, confirm — this is not a read.
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

// StatusReport is what status found. The caller renders it: the two front-ends draw different
// tables, but neither decides what is in one.
type StatusReport struct {
	Manifest *stack.Manifest
	Path     string
	Found    []stack.Component
	// LegacyConfig is set when the manifest is at the pre-rename path. The host IS installed —
	// saying otherwise sends its operator hunting for a Compose stack that never existed.
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

// Uninstall removes the stack's containers. Volumes are kept unless withVolumes, in which case the
// manifest goes too — it describes an install that no longer exists.
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

// WithInstallHint turns "no manifest" into an answer rather than a dead end. The same error means
// very different things depending on whether Miabi is running.
func WithInstallHint(err error) error {
	if errors.Is(err, stack.ErrNotInstalled) {
		return fmt.Errorf("%w\n\n"+
			"  If you installed with Docker Compose, this is expected — that stack is managed\n"+
			"  by Compose, not by the CLI. Use `docker compose` in your install directory.\n\n"+
			"  To install:  sudo miabi setup --domain miabi.example.com", err)
	}
	return err
}
