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

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
)

// The first existing path wins; a repo carrying several is not an error.
var SourcePaths = []string{
	".miabi/pipeline.yaml",
	".miabi/pipeline.yml",
	".miabi/pipelines.yaml",
	".miabi/pipelines.yml",
}

// Prefix makes a leaked probe worktree identifiable.
const probeDirPrefix = "mb-pipeline-probe-"

// A repo with no pipeline is not an error: Path is empty, Spec nil, and the caller builds normally.
type Found struct {
	// Ref is the ref that was probed, Commit the concrete SHA it resolved to.
	Ref    string
	Commit string
	// What the "auto" build method keys off, so the caller can explain the no-pipeline fallback.
	HasDockerfile bool
	// Path is the repo-relative path the document was read from ("" = none found).
	Path string
	// Stored verbatim so the definition's spec is byte-identical to the repo file.
	Raw string
	// Spec is the parsed document, nil when none was found or it failed to parse.
	Spec *Spec
	// Carried rather than returned so a broken file degrades to "build normally, tell the user why".
	SpecError string
}

// HasPipeline reports whether a usable pipeline document was found.
func (f *Found) HasPipeline() bool { return f != nil && f.Spec != nil }

// DiscoverFS returns zero values when no document exists; a parse failure still returns path and bytes.
func DiscoverFS(fsys fs.FS) (string, []byte, *Spec, error) {
	for _, p := range SourcePaths {
		raw, err := fs.ReadFile(fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, nil, fmt.Errorf("read %s: %w", p, err)
		}
		spec, err := ParseSpec(raw)
		if err != nil {
			return p, raw, nil, fmt.Errorf("%s: %w", p, err)
		}
		return p, raw, spec, nil
	}
	return "", nil, nil, nil
}

// Discover errors only on infrastructure failures; auth keeps the credential out of the URL and errors.
func Discover(ctx context.Context, url, ref string, auth transport.AuthMethod) (*Found, error) {
	dir, commit, cleanup, err := cloneWorktree(ctx, url, ref, auth)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// os.Root, not os.DirFS: a committed symlink must not read files outside the checkout.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open probe dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	f := &Found{Ref: ref, Commit: commit}
	fsys := root.FS()
	if st, err := fs.Stat(fsys, "Dockerfile"); err == nil && !st.IsDir() {
		f.HasDockerfile = true
	}

	path, raw, spec, err := DiscoverFS(fsys)
	f.Path, f.Raw, f.Spec = path, string(raw), spec
	if err != nil {
		f.SpecError = err.Error()
	}
	return f, nil
}

// Depth-1 single-branch is the only fast path on large repos; tags or SHAs fall back to a full clone.
func cloneWorktree(ctx context.Context, url, ref string, auth transport.AuthMethod) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", probeDirPrefix)
	if err != nil {
		return "", "", nil, fmt.Errorf("create probe dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	opts := &gogit.CloneOptions{URL: url, Auth: auth, Depth: 1, SingleBranch: true, Tags: gogit.NoTags}
	if ref != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(ref)
	}
	repo, cloneErr := gogit.PlainCloneContext(ctx, dir, false, opts)
	if cloneErr == nil {
		head, err := repo.Head()
		if err != nil {
			cleanup()
			return "", "", nil, fmt.Errorf("resolve HEAD: %w", err)
		}
		return dir, head.Hash().String(), cleanup, nil
	}
	cleanup()

	if ref == "" || ctx.Err() != nil {
		return "", "", nil, fmt.Errorf("git clone: %w", cloneErr)
	}

	dir, err = os.MkdirTemp("", probeDirPrefix)
	if err != nil {
		return "", "", nil, fmt.Errorf("create probe dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	commit, err := gitrepo.Checkout(ctx, dir, url, ref, auth, nil)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return dir, commit, cleanup, nil
}
