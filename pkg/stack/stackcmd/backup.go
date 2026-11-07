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

package stackcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/pkg/stack"
)

// Only these are pruned; a dump the operator asked for is never deleted.
const LabelPreRestore = "pre-restore"

// autoKeep is how many automatic dumps of each label are kept.
const autoKeep = 5

const backupTimeLayout = "20060102T150405Z"

// The version is the control plane's tag at dump time, which restore compares against the running one.
var backupName = regexp.MustCompile(`^miabi-(.+)-(\d{8}T\d{6}Z)(?:-([a-z-]+))?\` + stack.DBBackupExt + `$`)

// Beside the manifest, which is already writable and bind-mounted by the containerized installer.
func BackupDir(manifestPath string) string {
	return filepath.Join(filepath.Dir(manifestPath), "backups")
}

// DBBackup is one dump found on disk.
type DBBackup struct {
	Path    string
	Version string
	Label   string
	Taken   time.Time
	Size    int64
}

// BackupOptions is the flag surface of `stack backup`, already parsed.
type BackupOptions struct {
	// Dir defaults to BackupDir.
	Dir   string
	Label string
}

// Database only: not volumes, certificates or the manifest, whose encryption key the dump needs.
func Backup(ctx context.Context, svc *stack.Service, path string, o BackupOptions, ui UI) (string, error) {
	m, err := stack.Load(path)
	if err != nil {
		return "", WithInstallHint(err)
	}
	if err := m.Normalize(); err != nil {
		return "", err
	}
	return dump(ctx, svc, m, path, o.Dir, o.Label, ui)
}

func dump(ctx context.Context, svc *stack.Service, m *stack.Manifest, path, dir, label string, ui UI) (string, error) {
	if dir == "" {
		dir = BackupDir(path)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("miabi-%s-%s", versionOf(m.Images.Miabi), time.Now().UTC().Format(backupTimeLayout))
	if label != "" {
		name += "-" + label
	}
	ui.Info("Dumping the platform database to %s …", dir)
	file, err := svc.DumpDatabase(ctx, m, dir, name+stack.DBBackupExt)
	if err != nil {
		return "", err
	}
	if st, serr := os.Stat(file); serr == nil {
		ui.Success("Database saved: %s (%s)", file, humanSize(st.Size()))
	} else {
		ui.Success("Database saved: %s", file)
	}
	return file, nil
}

// Files not following the naming scheme are ignored rather than guessed at.
func ListBackups(dir string) ([]DBBackup, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []DBBackup
	for _, e := range entries {
		b, ok := parseBackupName(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		b.Path = filepath.Join(dir, e.Name())
		if info, ierr := e.Info(); ierr == nil {
			b.Size = info.Size()
		}
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b DBBackup) int { return b.Taken.Compare(a.Taken) })
	return out, nil
}

func parseBackupName(name string) (DBBackup, bool) {
	sm := backupName.FindStringSubmatch(name)
	if sm == nil {
		return DBBackup{}, false
	}
	t, err := time.Parse(backupTimeLayout, sm[2])
	if err != nil {
		return DBBackup{}, false
	}
	return DBBackup{Version: sm[1], Taken: t, Label: sm[3]}, true
}

// pruneAuto keeps the newest autoKeep dumps carrying label and deletes the rest.
func pruneAuto(dir, label string, ui UI) {
	all, err := ListBackups(dir)
	if err != nil {
		return
	}
	kept := 0
	for _, b := range all {
		if b.Label != label {
			continue
		}
		if kept++; kept <= autoKeep {
			continue
		}
		if err := os.Remove(b.Path); err != nil {
			ui.Warn("could not prune %s: %v", b.Path, err)
		}
	}
}

// RestoreOptions is the flag surface of `stack restore`, already parsed.
type RestoreOptions struct {
	// File is a path, or the bare name of a dump in BackupDir.
	File string
	Yes  bool
	// Force restores a dump taken by a newer Miabi than the one running.
	Force bool
}

// The current database is dumped first, so a restore can itself be undone.
func Restore(ctx context.Context, svc *stack.Service, path string, o RestoreOptions, ui UI) error {
	m, err := stack.Load(path)
	if err != nil {
		return WithInstallHint(err)
	}
	if err := m.Normalize(); err != nil {
		return err
	}
	file, err := resolveBackup(path, o.File)
	if err != nil {
		return err
	}

	running := versionOf(m.Images.Miabi)
	b, named := parseBackupName(filepath.Base(file))
	switch {
	case !named:
		ui.Warn("%s does not follow the miabi-<version>-<time>.dump naming, so its Miabi version is unknown.", filepath.Base(file))
	case newerThan(b.Version, running) && !o.Force:
		// The control plane only migrates forward, so it has no code for a newer schema.
		return fmt.Errorf("%s was taken by Miabi %s, but this host runs %s — an older control plane cannot "+
			"read a newer schema.\n\n  Upgrade first:  miabi upgrade --version %s\n  Or override:    --force",
			filepath.Base(file), b.Version, running, b.Version)
	}

	ui.Printf("\nMiabi will restore the platform database:\n\n")
	ui.Printf("  from        %s\n", file)
	if named {
		ui.Printf("  taken       %s by Miabi %s\n", b.Taken.Local().Format(time.DateTime), b.Version)
	}
	ui.Printf("  running     %s\n", m.Images.Miabi)
	ui.Printf("\n  Everything written since that dump is replaced. The panel is down while it loads;\n" +
		"  the current database is dumped first, so this can be undone.\n" +
		"  The dump's secrets only decrypt with this host's encryption key (in the manifest).\n\n")
	if !o.Yes && !ui.Confirm("Proceed?") {
		return errors.New("cancelled")
	}

	safety, err := dump(ctx, svc, m, path, "", LabelPreRestore, ui)
	if err != nil {
		return fmt.Errorf("could not save the current database before replacing it, so nothing was changed: %w", err)
	}
	pruneAuto(BackupDir(path), LabelPreRestore, ui)

	ui.Info("Stopping %s …", stack.ContainerControlPlane)
	if err := svc.StopControlPlane(ctx); err != nil {
		return err
	}
	rerr := svc.RestoreDatabase(ctx, m, file)
	if rerr != nil {
		// The swap is the last step, so a failed load left the live database as it was.
		ui.Warn("restore failed; the database was not changed. Bringing %s back …", stack.ContainerControlPlane)
	} else {
		ui.Info("Database loaded. Starting %s …", stack.ContainerControlPlane)
	}
	if err := convergeRest(ctx, svc, m, path); err != nil {
		return errors.Join(rerr, err)
	}
	if rerr != nil {
		return rerr
	}
	ui.Printf("\n")
	ui.Success("Restored. %s", m.WebURL)
	ui.Printf("  To undo, restore %s\n", filepath.Base(safety))
	return nil
}

func resolveBackup(manifestPath, file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", errors.New("name the dump to restore — `miabi stack backups` lists them")
	}
	if _, err := os.Stat(file); err == nil {
		return filepath.Abs(file)
	}
	if !strings.ContainsRune(file, filepath.Separator) {
		candidate := filepath.Join(BackupDir(manifestPath), file)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no dump at %s — `miabi stack backups` lists them", file)
}

// A pinned digest carries no version to compare, hence "digest".
func versionOf(image string) string {
	t := tagOf(image)
	if t == "" {
		return "digest"
	}
	return t
}

// Non-x.y.z versions (floating tags, digests) compare as unknown, which is never newer.
func newerThan(a, b string) bool {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	return slices.Compare(pa, pb) > 0
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
