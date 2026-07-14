// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/selfcontainer"
	"github.com/miabi-io/miabi/internal/services/platformstack"
)

// registerStackCommands wires `miabi install|update|status|uninstall`.
//
// These run on the HOST, not inside the control-plane container — which is the
// entire point. The control plane cannot replace itself (`docker stop miabi` kills
// the process doing the stopping), so the actor that updates it has to live outside
// it. Compose used to be that actor; now Miabi is, and it needs no database to do it:
// the stack's desired state is a file, because Postgres is itself part of the stack.
func registerStackCommands(cli *okapicli.CLI) {
	cli.Command("install", "Install the Miabi stack on this host (Docker required)", runInstall).
		String("domain", "d", "", "Public hostname for the panel, e.g. miabi.example.com").
		String("admin-email", "", "", "First admin's email (default admin@<domain>)").
		String("acme-email", "", "", "Contact for Let's Encrypt (default admin@<domain>)").
		String("control-url", "", "", "URL remote nodes and agents dial back on (default: the panel's own URL)").
		String("image", "", "", "Control-plane image (default: the image this installer itself runs from)").
		String("gateway-image", "", "", "Goma Gateway image").
		String("goma-config", "", "", "Gateway config file, relative to the manifest's directory (default goma.yml)").
		Bool("registry", "", false, "Enable the built-in container registry").
		Bool("no-host-proc", "", false, "Do not bind the host's /proc into the control plane (for hosts that refuse the bind; host metrics fall back to the container's /proc)").
		String("registry-host", "", "", "Registry hostname (default registry.<domain>); implies --registry").
		String("subnet", "", "", "CIDR for the shared `miabi` network (default "+platformstack.DefaultSubnet+")").
		String("file", "f", "", "Manifest path (default "+platformstack.DefaultManifestPath+")").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("update", "Update the installed stack (all components, or one by name)", runUpdate).
		String("image", "", "", "Roll out this exact image instead of the one this installer runs from").
		String("file", "f", "", "Manifest path").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("status", "Show the installed stack against its manifest", runStatus).
		String("file", "f", "", "Manifest path")

	cli.Command("uninstall", "Remove the Miabi stack's containers (volumes are KEPT unless --volumes)", runUninstall).
		String("file", "f", "", "Manifest path").
		Bool("volumes", "", false, "ALSO delete the data volumes — this destroys the database").
		Bool("yes", "y", false, "Do not prompt")
}

// defaultImage is the control-plane image to install.
//
// The installer normally IS the Miabi image (`docker run miabi/miabi:1.4.0 install`),
// so the honest answer is: whatever image this very process was started from. Asking
// Docker for our own container's image ref gets that exactly — including the registry
// and the literal tag. Deriving it from the version stamp instead would hardcode
// Docker Hub, so an image pulled from a private registry
// (registry.example.com/miabi:v1.4.0-dev.1) would install `miabi/miabi:1.4.0-dev.1`
// — a different image, on a different registry, that probably does not exist.
//
// The version stamp is the fallback, for the binary-on-a-host case where there is no
// container to inspect.
func defaultImage(ctx context.Context, dc docker.Client) string {
	if id := selfcontainer.Detect(); id != "" {
		if cfg, err := dc.InspectContainerConfig(ctx, id); err == nil && cfg.Image != "" {
			return cfg.Image
		}
	}
	v := strings.TrimSpace(config.Version)
	if v == "" || v == "dev" {
		// A dev build, run outside a container, has no published image to point at. Say
		// so rather than installing something arbitrary the operator did not choose.
		return ""
	}
	return "miabi/miabi:" + strings.TrimPrefix(v, "v")
}

// stackCtx cancels on Ctrl-C, so a half-finished converge can still run its cleanup
// rather than leaving a container mid-pull.
func stackCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func stackService(cmd *okapicli.Command) (*platformstack.Service, docker.Client, error) {
	path := manifestPath(cmd)
	dc, err := docker.New()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach Docker (it must be installed, and this user must be able to use it — the `docker` group, or root): %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := dc.Ping(ctx); err != nil {
		_ = dc.Close()
		return nil, nil, fmt.Errorf("the Docker daemon is not responding: %w", err)
	}
	return platformstack.New(dc, func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }, path), dc, nil
}

func manifestPath(cmd *okapicli.Command) string {
	if p := strings.TrimSpace(cmd.GetString("file")); p != "" {
		return p
	}
	return platformstack.ManifestPath()
}

func runInstall(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	path := manifestPath(cmd)

	// Re-installing on top of an existing manifest is a CONVERGE, not a fresh
	// install: it must keep the existing secrets, or the new control plane would come
	// up with a new database password and be locked out of its own data.
	m, err := platformstack.Load(path)
	newInstall := false
	switch {
	case err == nil:
		fmt.Printf("Found an existing install (%s) — converging it to match.\n", path)
	case errors.Is(err, platformstack.ErrNotInstalled):
		m, newInstall = platformstack.Defaults(defaultImage(context.Background(), dc)), true
	default:
		return err
	}

	if v := cmd.GetString("domain"); v != "" {
		m.Domain = v
	}
	if v := cmd.GetString("admin-email"); v != "" {
		m.Secrets.AdminEmail = v
	}
	if v := cmd.GetString("acme-email"); v != "" {
		m.ACMEEmail = v
	}
	if v := cmd.GetString("control-url"); v != "" {
		m.ControlURL = v
	}
	if v := cmd.GetString("image"); v != "" {
		m.Images.Miabi = v
	}
	if v := cmd.GetString("gateway-image"); v != "" {
		m.Images.Gateway = v
	}
	if v := cmd.GetString("goma-config"); v != "" {
		m.Gateway.Config = v
	}
	if v := cmd.GetString("subnet"); v != "" {
		m.Network.Subnet = v
	}
	// --registry-host implies --registry: naming the host is only meaningful if the
	// registry runs, and silently ignoring the flag would be worse than assuming.
	if cmd.GetBool("registry") {
		m.Registry.Enabled = true
	}
	if cmd.GetBool("no-host-proc") {
		off := false
		m.HostProc = &off
	}
	if v := cmd.GetString("registry-host"); v != "" {
		m.Registry.Host, m.Registry.Enabled = v, true
	}

	if m.Domain == "" {
		return errors.New("--domain is required (the panel's public hostname, e.g. miabi.example.com)")
	}
	if m.Images.Miabi == "" {
		return errors.New("cannot determine which image to install: this build carries no version, and it is not running as a container it could inspect — pass --image <repo>:<tag>")
	}
	if m.Secrets.AdminEmail == "" || m.Secrets.AdminEmail == "admin@example.com" {
		m.Secrets.AdminEmail = "admin@" + m.Domain
	}

	// Normalize before showing the plan, so what is printed is what will run —
	// including the generated secrets, which must be persisted even if converge later
	// fails. A stack whose containers exist but whose password was never written down
	// is unrecoverable.
	if err := m.Normalize(); err != nil {
		return err
	}

	pctx, pcancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer pcancel()

	// A FRESH install (no manifest) onto an EXISTING Postgres volume can never work:
	// Postgres keeps the password its data dir was created with, so the password this
	// install just generated will not open it. Nothing downstream catches it —
	// pg_isready does not check credentials — and the operator is left with a database
	// that is intact and unreadable. Refuse, and say how to recover.
	if newInstall {
		if err := svc.CheckOrphanedData(pctx); err != nil {
			return err
		}
	}

	// Check the ports BEFORE creating anything. The gateway is the last component to
	// come up, so without this the install gets all the way through Postgres, Redis
	// and the control plane, and only then dies because something else already owns
	// :443 — leaving a half-built stack and an error at the worst possible moment.
	conflicts, perr := svc.CheckPorts(pctx)
	if perr != nil {
		return perr
	}
	if len(conflicts) > 0 {
		return platformstack.PortConflictError(conflicts)
	}

	fmt.Printf("\nMiabi will install:\n\n")
	fmt.Printf("  domain      %s  (%s)\n", m.Domain, m.WebURL)
	fmt.Printf("  control     %s\n", m.Images.Miabi)
	fmt.Printf("  gateway     %s\n", m.Images.Gateway)
	fmt.Printf("  database    %s\n", m.Images.Postgres)
	fmt.Printf("  cache       %s\n", m.Images.Redis)
	fmt.Printf("  network     %s (%s)\n", m.Network.Name, m.Network.Subnet)
	if m.Registry.Enabled {
		fmt.Printf("  registry    %s\n", m.Registry.Host)
	}
	if m.HostProc != nil && !*m.HostProc {
		fmt.Printf("  host /proc  not bound (host metrics fall back to the container's /proc)\n")
	}
	fmt.Printf("  manifest    %s\n", path)
	fmt.Printf("  ports       80, 443 (free)\n\n")

	if !cmd.GetBool("yes") && !confirm("Proceed?") {
		return errors.New("cancelled")
	}

	// Persist the manifest BEFORE creating anything. The secrets are generated here
	// and exist nowhere else; if converge dies halfway, the operator still has the
	// database password and can re-run. The reverse order can strand a live Postgres
	// whose password nobody knows.
	if err := platformstack.Save(path, m); err != nil {
		return err
	}

	ctx, cancel := stackCtx()
	defer cancel()
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	// Converge may have filled in derived fields (docker GID, defaults). Persist again
	// so the file matches what actually ran.
	if err := platformstack.Save(path, m); err != nil {
		return err
	}

	fmt.Printf("\n✓ Miabi is up at %s\n", m.WebURL)
	if newInstall {
		fmt.Printf("\n  Sign in with:\n    %s\n    %s\n", m.Secrets.AdminEmail, m.Secrets.AdminPassword)
		fmt.Printf("\n  This password is shown only now. It lives in %s (mode 0600),\n"+
			"  together with the database password and the encryption key — BACK THAT FILE UP.\n"+
			"  Without it the encrypted secrets in the database cannot be read back.\n", path)
	}
	// The registry is served on its OWN hostname with its own certificate, so it needs
	// its own DNS record. Without one it simply never works, and the failure surfaces
	// far from here — as a docker push that cannot resolve the host.
	names := m.Domain
	if m.Registry.Enabled {
		names = fmt.Sprintf("%s and %s", m.Domain, m.Registry.Host)
	}
	fmt.Printf("\n  Point %s at this host's public IP; the gateway obtains a certificate\n"+
		"  from Let's Encrypt on the first request.\n", names)
	if m.Registry.Enabled {
		fmt.Printf("\n  Registry: docker login %s   (use a Miabi account or an API token)\n", m.Registry.Host)
	}

	// The operator most likely reached this by typing a `docker run` by hand. Nothing
	// has told them what the follow-up commands are, and they will not guess that the
	// image they just ran is also the tool that manages what it built.
	printManageHint(m.Images.Miabi, path)
	return nil
}

// printManageHint shows the exact command to drive this install again — using the
// image that actually installed it, so it is correct on a private registry too.
func printManageHint(image, manifest string) {
	dir := filepath.Dir(manifest)
	run := fmt.Sprintf("docker run --rm -it \\\n"+
		"      -v /var/run/docker.sock:/var/run/docker.sock \\\n"+
		"      -v %s:/etc/miabi \\\n"+
		"      %s", dir, image)
	fmt.Printf("\n  Manage it:\n\n    %s status\n\n"+
		"    …and likewise `update` (rolls the stack forward, rolling back if it fails)\n"+
		"    or `uninstall` (keeps your data; add --volumes to destroy it).\n", run)
}

func runUpdate(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	path := manifestPath(cmd)
	m, err := platformstack.Load(path)
	if err != nil {
		return withInstallHint(err)
	}
	if err := m.Normalize(); err != nil {
		return err
	}

	// The control plane's new image is this CLI's version, unless overridden. That is
	// the intended flow: fetch the new miabi binary, run `miabi update`, and the stack
	// follows the CLI.
	target := strings.TrimSpace(cmd.GetString("image"))
	if target == "" {
		target = defaultImage(context.Background(), dc)
	}
	if target == "" {
		return errors.New("cannot determine which image to roll out: this build carries no version, and it is not running as a container it could inspect — pass --image <repo>:<tag>")
	}

	// Two shapes:
	//
	//   miabi update              → move the control plane to this CLI's version, then
	//                               re-converge the rest (picking up manifest edits).
	//   miabi update <component>  → roll out just that one, to whatever the manifest
	//                               pins (or --image).
	//
	// The other components are pinned in the manifest rather than tracked
	// automatically, deliberately: bumping Postgres is a database restart, and that
	// should be something the operator asked for by name, not a side effect of
	// updating the panel.
	ctx, cancel := stackCtx()
	defer cancel()

	wholeStack := len(cmd.Args()) == 0
	name := platformstack.ContainerControlPlane
	if !wholeStack {
		name = cmd.Args()[0]
	}

	pin, ok := m.ImagePin(name)
	if !ok {
		return fmt.Errorf("unknown component %q (have: %s)", name, strings.Join(svc.ComponentNames(m), ", "))
	}
	// Only the control plane follows the CLI's own version. Naming another component
	// rolls it out to whatever the manifest pins, unless --image says otherwise.
	if !wholeStack && cmd.GetString("image") == "" {
		target = *pin
	}
	prev := *pin

	if prev == target && !isDrifted(ctx, svc, name, target) {
		fmt.Printf("%s is already at %s.\n", name, target)
		if wholeStack {
			return convergeRest(svc, m, path)
		}
		return nil
	}

	fmt.Printf("\nMiabi will roll out:\n\n  %-14s %s → %s\n\n", name, prev, target)
	if !cmd.GetBool("yes") && !confirm("Proceed?") {
		return errors.New("cancelled")
	}

	*pin = target
	err = svc.Rollout(ctx, m, name, target, func(phase string, cause error) {
		if cause != nil {
			fmt.Printf("  %-13s %v\n", phase, cause)
			return
		}
		fmt.Printf("  %s\n", phase)
	})
	if err != nil {
		// Put the pin back. The manifest must describe what is RUNNING: a rollback
		// restored the old image, so recording the new one would leave the file lying
		// about the stack — and the next `miabi install` would then "reconcile" reality
		// to match the lie, re-applying the update that just failed.
		*pin = prev
		_ = platformstack.Save(path, m)
		return err
	}
	if err := platformstack.Save(path, m); err != nil {
		return err
	}

	if wholeStack {
		if err := convergeRest(svc, m, path); err != nil {
			return err
		}
	}
	fmt.Printf("\n✓ Updated. %s\n", m.WebURL)
	return nil
}

// convergeRest reconciles the components the rollout did not touch, so a manifest
// edit (a new gateway pin, a rotated secret) takes effect without a second command.
// A no-op when nothing changed.
func convergeRest(svc *platformstack.Service, m *platformstack.Manifest, path string) error {
	ctx, cancel := stackCtx()
	defer cancel()
	if err := svc.Converge(ctx, m); err != nil {
		return err
	}
	return platformstack.Save(path, m)
}

// isDrifted reports whether the running container is NOT on the image the manifest
// pins — in which case "already at that version" is false even though the pin says
// so, and the rollout should proceed.
func isDrifted(ctx context.Context, svc *platformstack.Service, name, want string) bool {
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

func runStatus(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	path := manifestPath(cmd)
	m, mErr := platformstack.Load(path)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	found, err := svc.Discover(ctx)
	if err != nil {
		return err
	}

	if mErr != nil && len(found) == 0 {
		return withInstallHint(mErr)
	}
	if mErr != nil {
		// Containers exist but no manifest: this is a Compose install (or a hand-rolled
		// one). Say so plainly instead of implying it is broken.
		fmt.Printf("No stack manifest at %s — this stack was not installed by `miabi install`.\n", path)
		fmt.Printf("Manage it with the tool that created it (for Compose: docker compose up -d).\n\n")
	} else {
		fmt.Printf("%s  →  %s\n\n", m.Domain, path)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "COMPONENT\tSTATE\tHEALTH\tOWNER\tIMAGE")
	for _, c := range found {
		health := c.Health
		if health == "" {
			health = "—"
		}
		owner := c.ManagedBy
		if owner == "" {
			owner = "unlabeled"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.State, health, owner, c.Image)
	}
	_ = w.Flush()

	if m != nil {
		var drift []string
		for _, c := range found {
			if want, ok := m.ImageFor(c.Name); ok && want != c.Image {
				drift = append(drift, fmt.Sprintf("  %s is running %s but the manifest says %s", c.Name, c.Image, want))
			}
		}
		if len(drift) > 0 {
			fmt.Printf("\nDrift (run `miabi install` to reconcile):\n%s\n", strings.Join(drift, "\n"))
		}
	}
	return nil
}

func runUninstall(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	path := manifestPath(cmd)
	withVolumes := cmd.GetBool("volumes")

	fmt.Printf("This removes the Miabi stack's containers on this host.\n")
	if withVolumes {
		fmt.Printf("\n  --volumes was given: the DATABASE AND ALL ITS DATA WILL BE DELETED.\n" +
			"  This cannot be undone. Your apps' own volumes are NOT touched.\n")
	} else {
		fmt.Printf("  Data volumes are KEPT — re-run `miabi install` to bring the stack back.\n")
	}
	fmt.Println()
	if !cmd.GetBool("yes") && !confirm("Proceed?") {
		return errors.New("cancelled")
	}

	ctx, cancel := stackCtx()
	defer cancel()
	if err := svc.Teardown(ctx, withVolumes); err != nil {
		return err
	}
	if withVolumes {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Printf("note: could not remove %s: %v\n", path, err)
		}
	}
	fmt.Println("\n✓ Removed.")
	return nil
}

// withInstallHint turns "no manifest" into an answer rather than a dead end. The
// same error means very different things depending on whether Miabi is running.
func withInstallHint(err error) error {
	if errors.Is(err, platformstack.ErrNotInstalled) {
		return fmt.Errorf("%w\n\n"+
			"  If you installed with Docker Compose, this is expected — that stack is managed\n"+
			"  by Compose, not by the CLI. Use `docker compose` in your install directory.\n\n"+
			"  To install with the CLI:  miabi install --domain miabi.example.com", err)
	}
	return err
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Scanln(&s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}
