// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"io"
	"net/http"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/logstore"
)

// replayLogHistory returns the log lines to replay for a resource's SSE stream: a finished run's
// full log from the store when a ref is present and readable, otherwise the bounded DB tail.
// This preserves the "replay history then stream live" contract with the store as history.
func replayLogHistory(store *logstore.Store, ref, tail string) []string {
	if store.Enabled() && ref != "" {
		rc, err := store.Open(ref)
		if err != nil {
			// The row references a stored log the store can't read — a swept object, or a volume not shared
			// with the process that wrote it. The caller only gets the bounded DB tail, so surface why the
			// full log is missing rather than silently degrading.
			logger.Warn("log store read failed; falling back to DB tail", "ref", ref, "error", err)
		} else {
			defer func() { _ = rc.Close() }()
			if data, err := io.ReadAll(rc); err == nil {
				return logstore.SplitLines(string(data))
			}
		}
	}
	return logstore.SplitLines(tail)
}

// streamLogDownload writes a resource's full log as a file download. It streams the stored object
// directly (gzipped when the store compresses, advertised via Content-Encoding) and falls back to
// the DB tail when the store is disabled, the ref is empty, or the object is gone.
func streamLogDownload(c *okapi.Context, store *logstore.Store, ref, tail, filename string) error {
	c.SetHeader("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")

	if store.Enabled() && ref != "" {
		if rc, err := store.OpenRaw(ref); err == nil {
			defer func() { _ = rc.Close() }()
			if store.Compressed() {
				c.SetHeader("Content-Encoding", "gzip")
			}
			w := c.Response()
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, rc)
			return nil
		}
	}
	// Fallback: the bounded tail is all we have.
	w := c.Response()
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, tail)
	return nil
}
