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
	"bufio"
	"strconv"
	"strings"
)

const maxDockerfileBytes = 256 << 10

// ExposedPort is a port the Dockerfile's final stage declares with EXPOSE.
type ExposedPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// Only the last stage counts, since earlier stages never run; variable ports such as ${PORT} are skipped, as only the build knows them.
func parseExpose(dockerfile string) []ExposedPort {
	var out []ExposedPort
	seen := map[ExposedPort]bool{}
	sc := bufio.NewScanner(strings.NewReader(dockerfile))
	var logical strings.Builder
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			logical.WriteString(strings.TrimSuffix(line, "\\") + " ")
			continue
		}
		logical.WriteString(line)
		fields := strings.Fields(logical.String())
		logical.Reset()
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			out, seen = nil, map[ExposedPort]bool{}
		case "EXPOSE":
			for _, f := range fields[1:] {
				port, proto, _ := strings.Cut(strings.ToLower(f), "/")
				n, err := strconv.Atoi(port)
				if err != nil || n < 1 || n > 65535 {
					continue
				}
				if proto != "udp" {
					proto = "tcp"
				}
				p := ExposedPort{Port: n, Protocol: proto}
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}
