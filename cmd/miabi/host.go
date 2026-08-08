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
	"time"

	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
	"github.com/miabi-io/miabi/pkg/stack/selfcontainer"
)

// Shared plumbing for the host-side commands that still ship in the server image. Installing and
// managing the stack moved to the `miabi` CLI; `restore` did not, because platformrestore needs
// most of the server (GORM, blob storage, crypto) and could not follow it into a standalone binary.

// stackCtx cancels on Ctrl-C, so a half-finished restore can still run its cleanup rather than
// leaving a container mid-pull.
func stackCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func stackService(cmd *okapicli.Command) (*stack.Service, docker.Client, error) {
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
	return stack.New(dc, func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }, path), dc, nil
}

func manifestPath(cmd *okapicli.Command) string {
	if p := strings.TrimSpace(cmd.GetString("file")); p != "" {
		return p
	}
	return stack.ManifestPath()
}

// defaultImage is the control-plane image to write into a restored manifest: whatever image this
// process was started from, read back from Docker so a private registry ref survives verbatim.
// Deriving it from the version stamp would hardcode Docker Hub. The stamp is the fallback.
//
// It returns an error rather than "" because the only thing downstream that rejects an empty image
// is Manifest.Normalize, and by then the restore has already recreated Postgres, replayed the dump
// and restored the volumes — it would fail on the last step for a reason knowable before the first.
func defaultImage(ctx context.Context, dc docker.Client) (string, error) {
	if id := selfcontainer.Detect(); id != "" {
		if cfg, err := dc.InspectContainerConfig(ctx, id); err == nil && cfg.Image != "" {
			return cfg.Image, nil
		}
	}
	v := strings.TrimSpace(config.Version)
	if v == "" || v == "dev" {
		return "", errors.New("cannot determine which control-plane image to restore: this build " +
			"carries no version, and it is not running as a container it could inspect — pass --image <repo>:<tag>")
	}
	return "miabi/miabi:" + strings.TrimPrefix(v, "v"), nil
}

// printManageHint shows the exact command to drive this install again — using the image that
// actually installed it, so it is correct on a private registry too. An operator who reached this
// path did so without a `miabi` binary on the host, so the hint must not assume one.
func printManageHint(image, manifest string) {
	dir := filepath.Dir(manifest)
	run := fmt.Sprintf("docker run --rm -it \\\n"+
		"      -v /var/run/docker.sock:/var/run/docker.sock \\\n"+
		"      -v %s:/etc/miabi \\\n"+
		"      %s", dir, image)
	fmt.Printf("\n  Manage it:\n\n    %s status\n\n"+
		"    …and likewise `upgrade` (rolls the stack forward, rolling back if it fails)\n"+
		"    or `uninstall` (keeps your data; add --volumes to destroy it).\n\n"+
		"    Or install the miabi CLI for the shorter form: `miabi stack status`.\n", run)
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Scanln(&s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}
