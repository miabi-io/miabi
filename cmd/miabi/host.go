// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
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

// restore stays in the server image because platformrestore needs GORM, blob storage and crypto.

// Cancels on Ctrl-C so a half-finished restore still runs its cleanup.
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

// Read back from Docker so a private registry ref survives; errors early rather than failing after the restore ran.
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

// Uses the installing image so the hint is correct on a private registry and assumes no host miabi binary.
func printManageHint(image, manifest string) {
	dir := filepath.Dir(manifest)
	run := fmt.Sprintf("docker run --rm -it \\\n"+
		"      -v /var/run/docker.sock:/var/run/docker.sock \\\n"+
		"      -v %s:/etc/miabi \\\n"+
		"      %s", dir, image)
	fmt.Printf("\n  Manage it:\n\n    %s status\n\n"+
		"    …and likewise `upgrade` (rolls the stack forward, rolling back if it fails)\n"+
		"    or `uninstall` (keeps your data; add --volumes to destroy it).\n\n"+
		"    Or install the miabi CLI for the shorter form: `miabi system status`.\n", run)
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Scanln(&s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}
