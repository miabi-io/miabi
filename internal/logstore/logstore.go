// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logstore is the shared store for execution logs. It externalizes a finished run's full
// log out of Postgres into a gzipped object on a shared volume, keeping only a bounded tail and a
// reference in the database. A nil *Store is a no-op, so it is never a hard boot dependency.
package logstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Backend is the pluggable object store behind the log store. The filesystem
// backend is the default; an S3/MinIO backend can be added later without
// touching producers or readers (they only see *Store).
type Backend interface {
	// Create returns a write sink for ref, replacing any existing object.
	Create(ref string) (io.WriteCloser, error)
	// Open returns the raw stored bytes for ref (still gzip-compressed when the
	// store compresses). ErrNotFound when the object is absent.
	Open(ref string) (io.ReadCloser, error)
	// Delete removes the object for ref. Absent is not an error.
	Delete(ref string) error
	// Sweep deletes every object last modified before cutoff and returns the
	// number removed.
	Sweep(cutoff time.Time) (int, error)
	// Name identifies the backend for logging (e.g. "filesystem").
	Name() string
}

// ErrNotFound is returned by a backend when an object does not exist.
var ErrNotFound = errors.New("logstore: object not found")

// Config bounds and shapes what the store writes. Zero values fall back to the
// defaults below.
type Config struct {
	// MaxBytes caps a single externalized log; past it the middle is dropped and
	// Truncated is set (0 = no cap).
	MaxBytes int64
	// TailBytes is the size of the bounded tail the store returns for the DB row.
	TailBytes int
	// Compress gzip-compresses objects at rest when true.
	Compress bool
	// RetentionDays is how long objects are kept by Sweep (0 = keep forever).
	RetentionDays int
}

const (
	defaultMaxBytes  = 32 << 20 // 32 MB
	defaultTailBytes = 16 << 10 // 16 KB
	truncationMarker = "\n\n... [log truncated: %d bytes omitted] ...\n\n"
)

func (c Config) maxBytes() int64 {
	if c.MaxBytes < 0 {
		return 0
	}
	if c.MaxBytes == 0 {
		return defaultMaxBytes
	}
	return c.MaxBytes
}

func (c Config) tailBytes() int {
	if c.TailBytes <= 0 {
		return defaultTailBytes
	}
	return c.TailBytes
}

// Store is the shared log store. A nil *Store is valid and disabled.
type Store struct {
	be  Backend
	cfg Config
}

// New builds a store over a backend. A nil backend yields a disabled store.
func New(be Backend, cfg Config) *Store {
	if be == nil {
		return nil
	}
	return &Store{be: be, cfg: cfg}
}

// Enabled reports whether the store has a backend (safe on a nil receiver).
func (s *Store) Enabled() bool { return s != nil && s.be != nil }

// Backend returns the backend name (for logging/status); "off" when disabled.
func (s *Store) Backend() string {
	if !s.Enabled() {
		return "off"
	}
	return s.be.Name()
}

// Result reports what Externalize wrote, to record on the DB row.
type Result struct {
	Ref       string // object key (empty when the store is disabled)
	Bytes     int64  // uncompressed size actually stored
	Lines     int    // line count actually stored
	Truncated bool   // middle was dropped to honor MaxBytes
	Tail      string // bounded last slice for the DB LogTail column
}

// Externalize writes the full log for ref and returns the counters + bounded
// tail to persist. On a disabled store it is a no-op returning a Result whose
// Tail is the (bounded) input, so callers can always trust Result.Tail.
func (s *Store) Externalize(ref, content string) (Result, error) {
	if !s.Enabled() || ref == "" {
		return Result{Tail: boundTail(content, Config{}.tailBytes())}, nil
	}
	tail := boundTail(content, s.tailBytes())

	stored, truncated := s.applyCap(content)
	w, err := s.be.Create(ref)
	if err != nil {
		return Result{Tail: tail}, fmt.Errorf("logstore: create %s: %w", ref, err)
	}
	enc := s.encoder(w)
	if _, err := io.WriteString(enc, stored); err != nil {
		_ = enc.Close()
		_ = w.Close()
		return Result{Tail: tail}, fmt.Errorf("logstore: write %s: %w", ref, err)
	}
	if err := enc.Close(); err != nil {
		_ = w.Close()
		return Result{Tail: tail}, fmt.Errorf("logstore: flush %s: %w", ref, err)
	}
	if err := w.Close(); err != nil {
		return Result{Tail: tail}, fmt.Errorf("logstore: close %s: %w", ref, err)
	}
	return Result{
		Ref:       ref,
		Bytes:     int64(len(stored)),
		Lines:     countLines(stored),
		Truncated: truncated,
		Tail:      boundTail(stored, s.tailBytes()),
	}, nil
}

// Open returns a decompressed reader over ref's full log (history + download).
func (s *Store) Open(ref string) (io.ReadCloser, error) {
	if !s.Enabled() || ref == "" {
		return nil, ErrNotFound
	}
	rc, err := s.be.Open(ref)
	if err != nil {
		return nil, err
	}
	if !s.cfg.Compress {
		return rc, nil
	}
	gz, err := gzip.NewReader(rc)
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("logstore: gunzip %s: %w", ref, err)
	}
	gz.Multistream(true)
	return &gzReadCloser{gz: gz, under: rc}, nil
}

// Tail returns the last n bytes of ref's decompressed log, trimmed to a line
// boundary. Empty string (no error) when the object is absent or the store is
// disabled — callers fall back to the DB tail.
func (s *Store) Tail(ref string, n int) (string, error) {
	rc, err := s.Open(ref)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return boundTail(string(data), n), nil
}

// Delete removes ref's object (used on resource deletion / retention).
func (s *Store) Delete(ref string) error {
	if !s.Enabled() || ref == "" {
		return nil
	}
	return s.be.Delete(ref)
}

// Sweep removes objects older than the configured retention window and returns
// the count deleted. A zero/absent retention keeps everything (no-op).
func (s *Store) Sweep(now time.Time) (int, error) {
	if !s.Enabled() || s.cfg.RetentionDays <= 0 {
		return 0, nil
	}
	cutoff := now.AddDate(0, 0, -s.cfg.RetentionDays)
	return s.be.Sweep(cutoff)
}

// Compressed reports whether stored objects are gzip-compressed (for the
// download endpoint's Content-Encoding).
func (s *Store) Compressed() bool { return s.Enabled() && s.cfg.Compress }

// OpenRaw returns the raw stored bytes for ref (still gzipped when Compressed),
// for a streaming download that avoids server-side decompression.
func (s *Store) OpenRaw(ref string) (io.ReadCloser, error) {
	if !s.Enabled() || ref == "" {
		return nil, ErrNotFound
	}
	return s.be.Open(ref)
}

func (s *Store) tailBytes() int { return s.cfg.tailBytes() }

func (s *Store) encoder(w io.Writer) io.WriteCloser {
	if s.cfg.Compress {
		return gzip.NewWriter(w)
	}
	return nopWriteCloser{w}
}

// applyCap enforces MaxBytes by keeping the head and tail and dropping the
// middle, marking the gap. Returns the (possibly truncated) content and whether
// a cut was made.
func (s *Store) applyCap(content string) (string, bool) {
	max := s.cfg.maxBytes()
	if max <= 0 || int64(len(content)) <= max {
		return content, false
	}
	half := int(max / 2)
	omitted := len(content) - 2*half
	var b strings.Builder
	b.Grow(2*half + 64)
	b.WriteString(trimToLineStart(content[:half]))
	b.WriteString(fmt.Sprintf(truncationMarker, omitted))
	b.WriteString(trimToLineEnd(content[len(content)-half:]))
	return b.String(), true
}

// boundTail returns the last n bytes of s, trimmed forward to a line boundary
// so the tail never starts mid-line.
func boundTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return trimToLineStart(s[len(s)-n:])
}

// trimToLineStart drops a leading partial line (everything up to and including
// the first newline) when s was cut mid-line.
func trimToLineStart(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

func trimToLineEnd(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i:]
	}
	return s
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// SplitLines yields the non-empty lines of a stored/tail log, for SSE replay.
func SplitLines(s string) []string {
	out := []string{} // never nil, so a JSON `lines` field renders as [] not null
	sc := bufio.NewScanner(bytes.NewReader([]byte(s)))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			out = append(out, line)
		}
	}
	return out
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type gzReadCloser struct {
	gz    *gzip.Reader
	under io.ReadCloser
}

func (r *gzReadCloser) Read(p []byte) (int, error) { return r.gz.Read(p) }
func (r *gzReadCloser) Close() error {
	err := r.gz.Close()
	if cerr := r.under.Close(); err == nil {
		err = cerr
	}
	return err
}
