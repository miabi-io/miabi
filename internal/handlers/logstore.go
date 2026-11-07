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

package handlers

import (
	"io"
	"net/http"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/logstore"
)

func replayLogHistory(store *logstore.Store, ref, tail string) []string {
	if store.Enabled() && ref != "" {
		rc, err := store.Open(ref)
		if err != nil {
			// The caller only gets the bounded DB tail, so log why the full log is missing.
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

// Falls back to the DB tail when the store is disabled, the ref is empty, or the object is gone.
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
