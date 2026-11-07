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

package stack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// pg_dump's custom format is compressed and lets a restore load into a fresh database.
const DBBackupExt = ".dump"

const (
	dbName        = "miabi"
	dbUser        = "miabi"
	dbRestoreName = "miabi_restore"
	dumpMount     = "/backup"
)

// Uses the manifest's Postgres image so pg_dump is never older than the server; renamed only on clean exit.
func (s *Service) DumpDatabase(ctx context.Context, m *Manifest, dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	final := filepath.Join(dir, name)
	partial := final + ".partial"
	_ = os.Remove(partial)

	script := fmt.Sprintf("pg_dump -h %s -U %s -d %s -Fc -f %s",
		ContainerPostgres, dbUser, dbName, shQuote(dumpMount+"/"+filepath.Base(partial)))
	if err := s.runPGClient(ctx, m, "db-dump", dir, false, script); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("pg_dump: %w", err)
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	return final, nil
}

// Loaded into a scratch DB and swapped in only on success, so a bad dump leaves the live database untouched.
func (s *Service) RestoreDatabase(ctx context.Context, m *Manifest, file string) error {
	if _, err := os.Stat(file); err != nil {
		return err
	}
	psql := fmt.Sprintf("psql -q -v ON_ERROR_STOP=1 -h %s -U %s -d postgres", ContainerPostgres, dbUser)
	script := strings.Join([]string{
		"set -e",
		fmt.Sprintf("%s -c 'DROP DATABASE IF EXISTS %s WITH (FORCE)'", psql, dbRestoreName),
		fmt.Sprintf("createdb -h %s -U %s -O %s %s", ContainerPostgres, dbUser, dbUser, dbRestoreName),
		fmt.Sprintf("pg_restore -h %s -U %s -d %s --no-owner --exit-on-error --single-transaction %s || { %s -c 'DROP DATABASE %s'; exit 1; }",
			ContainerPostgres, dbUser, dbRestoreName, shQuote(dumpMount+"/"+filepath.Base(file)), psql, dbRestoreName),
		fmt.Sprintf("%s -c 'DROP DATABASE %s WITH (FORCE)'", psql, dbName),
		fmt.Sprintf("%s -c 'ALTER DATABASE %s RENAME TO %s'", psql, dbRestoreName, dbName),
	}, "\n")
	if err := s.runPGClient(ctx, m, "db-restore", filepath.Dir(file), true, script); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return nil
}

// The control plane is stateless; Converge recreates it from the manifest.
func (s *Service) StopControlPlane(ctx context.Context) error {
	if err := s.dc.RemoveContainer(ctx, ContainerControlPlane, true); err != nil && !isNotFound(err) {
		return fmt.Errorf("stop %s: %w", ContainerControlPlane, err)
	}
	return nil
}

// Runs as the calling user so the dump is owned by whoever asked for it, not root.
func (s *Service) runPGClient(ctx context.Context, m *Manifest, role, dir string, readOnly bool, script string) error {
	pg, err := s.dc.InspectContainer(ctx, ContainerPostgres)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%s is not running — start the stack first (miabi setup)", ContainerPostgres)
		}
		return err
	}
	if !strings.EqualFold(pg.State, "running") && pg.State != "" {
		return fmt.Errorf("%s is %s — start the stack first (miabi setup)", ContainerPostgres, pg.State)
	}
	// An install predating the internal network still has Postgres on the proxy network until its next converge.
	network := m.InternalNetwork.Name
	if len(pg.Networks) > 0 && !onNetwork(pg, network) {
		network = pg.Networks[0].Name
	}

	host, err := s.requireHostPath(ctx, dir)
	if err != nil {
		return err
	}
	if err := ensureImage(ctx, s.dc, m.Images.Postgres, s.log); err != nil {
		return err
	}
	spec := docker.RunSpec{
		Name:       "mb-" + role,
		Image:      m.Images.Postgres,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{script},
		// HOME: the calling uid has no passwd entry in the image, and psql looks for ~/.psqlrc.
		Env:      []string{"PGPASSWORD=" + m.Secrets.DBPassword, "HOME=/tmp", "PGOPTIONS=-c client_min_messages=warning"},
		User:     fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Networks: []string{network},
		Binds:    []docker.BindMount{{Source: host, Target: dumpMount, ReadOnly: readOnly}},
		Labels:   map[string]string{docker.LabelPartOf: docker.PartOfMiabi, docker.LabelRole: role},
	}
	_ = s.dc.RemoveContainer(ctx, spec.Name, true) // a leftover from a killed run

	code, out, err := s.dc.RunOneShot(ctx, spec)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", code)
		}
		return errors.New(msg)
	}
	return nil
}

func onNetwork(c docker.Container, name string) bool {
	for _, n := range c.Networks {
		if n.Name == name {
			return true
		}
	}
	return false
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
