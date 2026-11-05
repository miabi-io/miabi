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

package wsbackup

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// minRewriteLen keeps a short source value (a one-letter user, say) from being replaced inside unrelated
// text. Platform-minted hosts, users and passwords are all longer.
const minRewriteLen = 6

// mapConnections builds the source → target rewrite for every logical database the bundle carries a source
// connection for, whether this run created it or found it already here.
func (r *restoreRun) mapConnections() {
	var pairs []string
	for _, d := range r.state.Databases {
		instID := r.instanceIDs[d.Name]
		if instID == 0 {
			continue
		}
		inst, err := r.svc.Database.Get(r.target, instID)
		if err != nil {
			continue
		}
		live, err := r.svc.Database.ListDatabases(r.target, instID)
		if err != nil {
			continue
		}
		byName := make(map[string]*models.Database, len(live))
		for i := range live {
			byName[live[i].Name] = &live[i]
		}
		for _, ld := range d.Databases {
			db := byName[ld.Name]
			if ld.Source == nil || db == nil {
				continue
			}
			conn, err := r.svc.Database.DatabaseConnection(inst, db)
			if err != nil {
				continue
			}
			pairs = append(pairs, connectionPairs(*ld.Source, conn)...)
		}
	}
	r.conn = newRewriter(pairs)
}

// connectionPairs lists old, new for each connection value that differs between the two installs. The port
// and database name are left out: they are usually equal, and too short to replace safely.
func connectionPairs(src wsbundle.Connection, dst database.ConnectionInfo) []string {
	var out []string
	for _, p := range [][2]string{
		{src.URI, dst.URI}, {src.ReadOnlyURI, dst.ReadOnlyURI},
		{src.Password, dst.Password}, {src.ReadOnlyPassword, dst.ReadOnlyPassword},
		{src.Host, dst.Host}, {src.User, dst.Username}, {src.ReadOnlyUser, dst.ReadOnlyUsername},
	} {
		if len(p[0]) >= minRewriteLen && p[1] != "" && p[0] != p[1] {
			out = append(out, p[0], p[1])
		}
	}
	return out
}

// newRewriter orders the pairs longest source first, so a URI is replaced whole before the host and password
// inside it are considered. Nil when there is nothing to rewrite.
func newRewriter(pairs []string) *strings.Replacer {
	if len(pairs) == 0 {
		return nil
	}
	type pair struct{ old, new string }
	ps := make([]pair, 0, len(pairs)/2)
	seen := map[string]bool{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if !seen[pairs[i]] {
			seen[pairs[i]] = true
			ps = append(ps, pair{pairs[i], pairs[i+1]})
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return len(ps[i].old) > len(ps[j].old) })
	flat := make([]string, 0, len(ps)*2)
	for _, p := range ps {
		flat = append(flat, p.old, p.new)
	}
	return strings.NewReplacer(flat...)
}

func (r *restoreRun) rewrite(v string) string {
	if r.conn == nil {
		return v
	}
	out := r.conn.Replace(v)
	if out != v {
		r.rewritten++
	}
	return out
}

func (r *restoreRun) rewriteData(data map[string]string) map[string]string {
	if r.conn == nil {
		return data
	}
	out := make(map[string]string, len(data))
	for k, v := range data {
		out[k] = r.rewrite(v)
	}
	return out
}

// connectionNote reports how many values were pointed at this platform's databases.
func (r *restoreRun) connectionNote() {
	if r.rewritten > 0 {
		r.report.Note(fmt.Sprintf("%d environment or config value(s) held the source's database connection "+
			"and now point at this platform's databases.", r.rewritten))
	}
}
