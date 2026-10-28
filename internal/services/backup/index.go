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

package backup

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/miabi-io/miabi/internal/storage/blob"
)

// IndexEntry is one backup as the *-bkup tools record it in their index file
// ({engine}-<scope>-metadata.json, next to the artifacts).
type IndexEntry struct {
	File   string `json:"file"`
	Status string `json:"status"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ReadIndex reads the tool index files directly inside dir, found in objects (a
// listing of dir), and returns their entries by file name. One read per index
// file, usually one, instead of a lookup per artifact. Nil when there is none,
// which is the case for artifacts written by 1.x tools.
func ReadIndex(ctx context.Context, get func(context.Context, string) ([]byte, error), objects []blob.Object, dir string) map[string]IndexEntry {
	dir = strings.Trim(dir, "/")
	var out map[string]IndexEntry
	for _, o := range objects {
		if path.Dir(o.Key) != dir && !(dir == "" && !strings.Contains(o.Key, "/")) {
			continue
		}
		if !strings.HasSuffix(o.Key, "-metadata.json") {
			continue
		}
		body, err := get(ctx, o.Key)
		if err != nil {
			continue
		}
		var f struct {
			Entries []IndexEntry `json:"entries"`
		}
		if json.Unmarshal(body, &f) != nil {
			continue
		}
		if out == nil {
			out = map[string]IndexEntry{}
		}
		for _, e := range f.Entries {
			out[e.File] = e
		}
	}
	return out
}

// Index check outcomes.
const (
	IndexDeleted = "deleted"
	IndexResized = "resized"
	IndexChanged = "changed"
)

// CheckIndexed judges file against its index entry: removed (retention, delete,
// or found missing), stored at another size, or a checksum that differs from the
// one the backup reported. "" when it matches or the index does not know it.
func CheckIndexed(idx map[string]IndexEntry, file string, size int64, checksum string) string {
	e, ok := idx[file]
	switch {
	case !ok:
		return ""
	case e.Status != "" && e.Status != "success":
		return IndexDeleted
	case size > 0 && e.Size > 0 && e.Size != size:
		return IndexResized
	case checksum != "" && e.SHA256 != "" && !strings.EqualFold(checksum, e.SHA256):
		return IndexChanged
	}
	return ""
}
