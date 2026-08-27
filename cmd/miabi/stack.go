// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
	"github.com/miabi-io/miabi/pkg/stack/stackcmd"
)

// registerStackCommands wires `install|update|restart|status|uninstall` onto the server image.
//
// The `miabi` CLI is the normal way to drive a host, and it does strictly more. This path exists
// for the operator who cannot or will not put a binary in /usr/local/bin: the image runs as uid
// 10001 with the host's docker group, so
//
//	docker run --rm -it -v /var/run/docker.sock:/var/run/docker.sock \
//	  -v /etc/miabi:/etc/miabi miabi/miabi:<tag> install --domain …
//
// installs Miabi with no root and nothing written outside /etc/miabi.
//
// These commands run on the HOST, not inside the control-plane container: the control plane cannot
// replace itself, so the actor that updates it lives outside it and keeps the desired state in a
// file. The behaviour is [stackcmd]'s, shared verbatim with the CLI — this file is flag parsing and
// rendering only, which is what keeps the two from drifting.
func registerStackCommands(cli *okapicli.CLI) {
	cli.Command("install", "Install the Miabi stack on this host, or converge an existing install", runInstall).
		String("domain", "d", "", "Public hostname for the panel, e.g. miabi.example.com").
		String("admin-email", "", "", "First admin's email (default admin@<domain>)").
		String("acme-email", "", "", "Contact for Let's Encrypt (default admin@<domain>)").
		String("control-url", "", "", "URL remote nodes and agents dial back on (default: the panel's own URL)").
		String("image", "", "", "Control-plane image (default: the image this installer itself runs from)").
		String("gateway-image", "", "", "Goma Gateway image").
		String("runner-image", "", "", "CI runner image (shown in the runner enrollment command)").
		String("goma-config", "", "", "Gateway config file, relative to the manifest's directory (default goma.yml)").
		Bool("registry", "", false, "Enable the built-in container registry").
		Bool("no-host-proc", "", false, "Do not bind the host's /proc into the control plane (for hosts that refuse the bind; host metrics fall back to the container's /proc)").
		String("registry-host", "", "", "Registry hostname (default registry.<domain>); implies --registry").
		String("subnet", "", "", "CIDR for the shared `miabi` network — apps and the gateway (default "+stack.DefaultSubnet+")").
		String("internal-subnet", "", "", "CIDR for the private `"+stack.DefaultInternalNetwork+"` network — control plane, database, cache (default "+stack.DefaultInternalSubnet+")").
		String("file", "f", "", "Manifest path (default "+stack.DefaultConfigPath+")").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("upgrade", "Move the installed stack to a newer version (all components, or one by name)", runUpgrade).
		String("version", "", "", "Roll out this version, keeping the current registry and repository (1.8.0 or v1.8.0)").
		String("image", "", "", "Roll out this exact image instead of the one this installer runs from").
		String("file", "f", "", "Manifest path").
		Bool("yes", "y", false, "Do not prompt")

	// `update` is what every pre-1.8 runbook and doc says, so it keeps working. okapicli has no
	// hidden-command support, and a visible "Deprecated:" line is the better trade anyway — it is
	// how an operator reading `--help` finds out about the rename.
	cli.Command("update", "Deprecated: use `upgrade`", runUpdate).
		String("version", "", "", "Roll out this version, keeping the current registry and repository (1.8.0 or v1.8.0)").
		String("image", "", "", "Roll out this exact image instead of the one this installer runs from").
		String("file", "f", "", "Manifest path").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("restart", "Restart the stack, or one component (miabi, miabi-gateway, …)", runRestart).
		String("file", "f", "", "Manifest path").
		Bool("yes", "y", false, "Do not prompt")

	cli.Command("status", "Show the installed stack against its manifest", runStatus).
		String("file", "f", "", "Manifest path")

	cli.Command("uninstall", "Remove the Miabi stack's containers (volumes are KEPT unless --volumes)", runUninstall).
		String("file", "f", "", "Manifest path").
		Bool("volumes", "", false, "ALSO delete the data volumes — this destroys the database").
		Bool("yes", "y", false, "Do not prompt")
}

// imageDefault adapts defaultImage to stackcmd's signature. A resolution failure becomes "", which
// stackcmd reports as "pass --image <repo>:<tag>" — the same advice, one layer up.
func imageDefault(dc docker.Client) func() string {
	return func() string {
		img, err := defaultImage(context.Background(), dc)
		if err != nil {
			return ""
		}
		return img
	}
}

// imageUI renders stackcmd's output plainly. The CLI colors the same calls; nothing else about the
// two front-ends differs.
type imageUI struct{}

func (imageUI) Printf(format string, a ...any)  { fmt.Printf(format, a...) }
func (imageUI) Info(format string, a ...any)    { fmt.Printf(format+"\n", a...) }
func (imageUI) Success(format string, a ...any) { fmt.Printf("✓ "+format+"\n", a...) }
func (imageUI) Warn(format string, a ...any)    { fmt.Fprintf(os.Stderr, "! "+format+"\n", a...) }
func (imageUI) Confirm(prompt string) bool      { return confirm(prompt) }

func runInstall(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	ctx, cancel := stackCtx()
	defer cancel()

	path := manifestPath(cmd)
	res, err := stackcmd.Setup(ctx, svc, path, stackcmd.SetupOptions{
		Domain:         cmd.GetString("domain"),
		AdminEmail:     cmd.GetString("admin-email"),
		ACMEEmail:      cmd.GetString("acme-email"),
		ControlURL:     cmd.GetString("control-url"),
		Image:          cmd.GetString("image"),
		GatewayImage:   cmd.GetString("gateway-image"),
		RunnerImage:    cmd.GetString("runner-image"),
		GomaConfig:     cmd.GetString("goma-config"),
		RegistryHost:   cmd.GetString("registry-host"),
		Subnet:         cmd.GetString("subnet"),
		InternalSubnet: cmd.GetString("internal-subnet"),
		Registry:       cmd.GetBool("registry"),
		NoHostProc:     cmd.GetBool("no-host-proc"),
		Yes:            cmd.GetBool("yes"),
		DefaultImage:   imageDefault(dc),
	}, imageUI{})
	if err != nil {
		return err
	}

	printManageHint(res.Manifest.Images.Miabi, res.Path)
	return nil
}

// runUpdate is the deprecated spelling of runUpgrade.
func runUpdate(cmd *okapicli.Command) error {
	fmt.Fprintln(os.Stderr, "! `update` is deprecated — use `upgrade`. It will be removed in a future release.")
	return runUpgrade(cmd)
}

func runUpgrade(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	ctx, cancel := stackCtx()
	defer cancel()

	component := ""
	if args := cmd.Args(); len(args) > 0 {
		component = args[0]
	}
	return stackcmd.Upgrade(ctx, svc, manifestPath(cmd), stackcmd.UpgradeOptions{
		Component:    component,
		Image:        cmd.GetString("image"),
		Version:      cmd.GetString("version"),
		Yes:          cmd.GetBool("yes"),
		DefaultImage: imageDefault(dc),
	}, imageUI{})
}

func runRestart(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	component := ""
	if args := cmd.Args(); len(args) > 0 {
		component = args[0]
	}
	ctx, cancel := stackCtx()
	defer cancel()
	return stackcmd.Restart(ctx, svc, manifestPath(cmd), component, cmd.GetBool("yes"), imageUI{})
}

func runStatus(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := stackcmd.Status(ctx, svc, manifestPath(cmd))
	if err != nil {
		return err
	}

	switch {
	case r.LegacyConfig != nil:
		fmt.Printf("! %v\n\n", r.LegacyConfig)
	case r.Unmanaged:
		// Containers exist but no manifest: this is a Compose install (or a hand-rolled one). Say
		// so plainly instead of implying it is broken.
		fmt.Printf("No stack manifest at %s — this stack was not installed by `install`.\n", r.Path)
		fmt.Printf("Manage it with the tool that created it (for Compose: docker compose up -d).\n\n")
	default:
		fmt.Printf("%s  →  %s\n\n", r.Manifest.Domain, r.Path)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "COMPONENT\tSTATE\tHEALTH\tOWNER\tIMAGE")
	for _, c := range r.Found {
		health, owner := c.Health, c.ManagedBy
		if health == "" {
			health = "—"
		}
		if owner == "" {
			owner = "unlabeled"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.State, health, owner, c.Image)
	}
	_ = w.Flush()

	if len(r.Drift) > 0 {
		fmt.Printf("\nDrift (re-run `install` to reconcile):\n%s\n", strings.Join(r.Drift, "\n"))
	}
	return nil
}

func runUninstall(cmd *okapicli.Command) error {
	svc, dc, err := stackService(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	ctx, cancel := stackCtx()
	defer cancel()
	return stackcmd.Uninstall(ctx, svc, manifestPath(cmd), cmd.GetBool("volumes"), cmd.GetBool("yes"), imageUI{},
		func(p string) error {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		})
}
