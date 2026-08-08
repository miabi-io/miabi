// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// SourcePaths lists, in priority order, the repository paths Miabi reads a
// pipeline-as-code document from. The first one that exists wins — a repo
// carrying several is not an error, it just has one that counts.
var SourcePaths = []string{
	".miabi/pipeline.yaml",
	".miabi/pipeline.yml",
	".miabi/pipelines.yaml",
	".miabi/pipelines.yml",
}

// probeDirPrefix names the temp worktrees Discover creates, so a leaked one is
// identifiable.
const probeDirPrefix = "mb-pipeline-probe-"

// Found is the result of probing a repository for pipeline-as-code. A repo with
// no pipeline is not an error: Path is empty and Spec is nil, and the caller
// falls back to plain build-and-deploy.
type Found struct {
	// Ref is the ref that was probed, Commit the concrete SHA it resolved to.
	Ref    string
	Commit string
	// HasDockerfile reports a Dockerfile at the repository root — what the "auto"
	// build method keys off, so the caller can explain what happens when the repo
	// carries no pipeline.
	HasDockerfile bool
	// Path is the repo-relative path the document was read from ("" = none found).
	Path string
	// Raw is the document verbatim — this is what gets stored as the definition's
	// spec, so the stored copy is byte-identical to the file in the repo.
	Raw string
	// Spec is the parsed document, nil when none was found or it failed to parse.
	Spec *Spec
	// SpecError describes why a document that *was* found failed to parse. It is
	// carried rather than returned so a broken file degrades to "build normally,
	// tell the user why" instead of blocking app creation.
	SpecError string
}

// HasPipeline reports whether a usable pipeline document was found.
func (f *Found) HasPipeline() bool { return f != nil && f.Spec != nil }

// DiscoverFS reads a pipeline-as-code document out of an already-checked-out tree, returning its path, bytes
// and parsed spec. A tree with no such document returns zero values and a nil error; one that exists but
// doesn't parse returns its path and bytes alongside the error, so callers can name the broken file.
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

// Discover clones url at ref into a throwaway worktree and reads its pipeline-as-code document. The error
// return covers only infrastructure failures: a repo with no pipeline, or one whose pipeline is malformed,
// comes back as a Found describing that. auth keeps the credential out of the URL and returned errors.
func Discover(ctx context.Context, url, ref string, auth transport.AuthMethod) (*Found, error) {
	dir, commit, cleanup, err := cloneWorktree(ctx, url, ref, auth)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	f := &Found{Ref: ref, Commit: commit}
	fsys := os.DirFS(dir)
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

// cloneWorktree clones url at ref into a fresh temp dir, returning the dir, the resolved commit and a cleanup
// func the caller must always run. It tries a depth-1 single-branch clone first — the only fast path on a large
// repo — and since that can only name a branch, a tag or SHA falls back to a full clone plus revision resolve.
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
	// A shallow clone of HEAD failing, or the caller giving up, is terminal —
	// only an unresolvable ref is worth a second, more expensive attempt.
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
