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

package stack

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParsePortMappings(t *testing.T) {
	cases := []struct {
		in   string
		want []portMapping
		ok   bool
	}{
		{"9000:9000", []portMapping{{9000, 9000, "tcp"}}, true},
		{"8080:80", []portMapping{{8080, 80, "tcp"}}, true},
		{"80", []portMapping{{0, 80, "tcp"}}, true}, // exposed, not published
		{"53:53/udp", []portMapping{{53, 53, "udp"}}, true},
		{"127.0.0.1:9000:9000", []portMapping{{9000, 9000, "tcp"}}, true},
		{"[::1]:9000:9000", []portMapping{{9000, 9000, "tcp"}}, true},
		{"127.0.0.1::80", []portMapping{{0, 80, "tcp"}}, true},
		{"8000-8002:9000-9002", []portMapping{{8000, 9000, "tcp"}, {8001, 9001, "tcp"}, {8002, 9002, "tcp"}}, true},
		{"3000-3001", []portMapping{{0, 3000, "tcp"}, {0, 3001, "tcp"}}, true},
		{"8000-8010:80", nil, false}, // Docker picks one host port; nothing to declare
		{"1-1000", nil, false},       // over maxPortRange
		{"80/sctp", nil, false},
		{"notaport", nil, false},
	}
	for _, c := range cases {
		got, err := parsePortMappings(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: err=%v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && !slices.Equal(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestComposeUnmarshal_LongForms(t *testing.T) {
	const y = `
services:
  web:
    image: nginx:1.25
    ports:
      - target: 80
        published: "8080"
        protocol: tcp
      - target: 53
        published: 5353
        protocol: udp
        host_ip: 127.0.0.1
      - target: 9090
      - "443:443"
    volumes:
      - type: volume
        source: data
        target: /data
      - type: bind
        source: config
        target: /etc/app
      - type: tmpfs
        target: /tmp
      - cache:/cache
`
	var cf composeFile
	if err := yaml.Unmarshal([]byte(y), &cf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	web := cf.Services["web"]
	if want := []string{"8080:80/tcp", "127.0.0.1:5353:53/udp", "9090", "443:443"}; !slices.Equal([]string(web.Ports), want) {
		t.Errorf("ports = %v, want %v", web.Ports, want)
	}
	mappings, bad := composePortMappings(web.Ports)
	if len(bad) != 0 || len(mappings) != 4 || mappings[1] != (portMapping{5353, 53, "udp"}) {
		t.Errorf("mappings = %v, bad = %v", mappings, bad)
	}
	if len(web.Volumes) != 4 {
		t.Fatalf("volumes = %+v", web.Volumes)
	}
	if m := web.Volumes[0].Mount; m.Source != "data" || m.Target != "/data" || m.Bind {
		t.Errorf("long volume = %+v", m)
	}
	if m := web.Volumes[1].Mount; !m.Bind {
		t.Errorf("a long-form bind with a bare source must stay a bind: %+v", m)
	}
	if m := web.Volumes[2].Mount; m.Unsupported != "tmpfs" {
		t.Errorf("tmpfs = %+v", m)
	}
	if v := web.Volumes[3]; !v.OK || v.Mount.Source != "cache" {
		t.Errorf("short volume = %+v", v)
	}
}

func TestParseComposeVolume(t *testing.T) {
	cases := []struct {
		in     string
		source string
		target string
		bind   bool
		ok     bool
	}{
		{"db_data:/var/lib/postgresql/data", "db_data", "/var/lib/postgresql/data", false, true},
		{"redis_data:/data", "redis_data", "/data", false, true},
		{"config:/etc/app:ro", "config", "/etc/app", false, true},
		{"/host/path:/container", "/host/path", "/container", true, true}, // bind
		{"./rel:/container", "./rel", "/container", true, true},           // bind
		{"/data", "", "/data", false, true},                               // anonymous
		{"", "", "", false, false},
	}
	for _, c := range cases {
		m, ok := parseComposeVolume(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (m.Source != c.source || m.Target != c.target || m.Bind != c.bind) {
			t.Errorf("%q: got {%q %q bind=%v} want {%q %q bind=%v}", c.in, m.Source, m.Target, m.Bind, c.source, c.target, c.bind)
		}
	}
}

func TestComposeUnmarshal_ParsesServiceVolumes(t *testing.T) {
	const y = `
services:
  posta-db:
    image: postgres:17-alpine
    volumes:
      - db_data:/var/lib/postgresql/data
  posta-redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data
volumes:
  db_data:
  redis_data:
`
	var cf composeFile
	if err := yaml.Unmarshal([]byte(y), &cf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	db := cf.Services["posta-db"]
	if len(db.Volumes) != 1 || db.Volumes[0].Raw != "db_data:/var/lib/postgresql/data" || db.Volumes[0].Mount.Source != "db_data" {
		t.Errorf("posta-db volumes = %v", db.Volumes)
	}
}

func TestComposeUnmarshal_EnvAndCommandForms(t *testing.T) {
	const y = `
services:
  web:
    image: nginx:1.25
    command: node server.js
    environment:
      FOO: bar
      BAZ: qux
  worker:
    image: myorg/worker
    command: ["celery", "worker"]
    environment:
      - A=1
      - B=2
`
	var cf composeFile
	if err := yaml.Unmarshal([]byte(y), &cf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cf.Services) != 2 {
		t.Fatalf("got %d services, want 2", len(cf.Services))
	}
	web := cf.Services["web"]
	if len(web.Command) != 2 || web.Command[0] != "node" || web.Command[1] != "server.js" {
		t.Errorf("web command string form = %v", web.Command)
	}
	if len(web.Environment) != 2 {
		t.Errorf("web env mapping form = %v", web.Environment)
	}
	worker := cf.Services["worker"]
	if len(worker.Command) != 2 || worker.Command[1] != "worker" {
		t.Errorf("worker command list form = %v", worker.Command)
	}
	if len(worker.Environment) != 2 || worker.Environment[0].Key != "A" || worker.Environment[0].Value != "1" {
		t.Errorf("worker env list form = %v", worker.Environment)
	}
}
