// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/transfer"
)

type localEngine struct{ dc docker.Client }

func (l localEngine) For(uint) (docker.Client, error) { return l.dc, nil }

// engineCase starts two servers of one engine on a private network, as two instances in two locations.
type engineCase struct {
	engine models.DBEngine
	image  string
	port   int
	admin  string
	env    func(pass string) []string
	seed   []string
	count  string
	want   string
}

func TestDumpRestoreStreamsBetweenInstances(t *testing.T) {
	dc, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	cases := []engineCase{
		{
			engine: models.DBEnginePostgres, image: "postgres:17-alpine", port: 5432, admin: "postgres",
			env: func(p string) []string { return []string{"POSTGRES_PASSWORD=" + p} },
			seed: []string{
				"CREATE TABLE items (id serial primary key, name text)",
				"INSERT INTO items (name) SELECT 'item-' || g FROM generate_series(1, 2500) g",
				"CREATE VIEW item_names AS SELECT name FROM items",
			},
			count: "SELECT count(*) FROM item_names",
			want:  "2500",
		},
		{
			engine: models.DBEngineMariaDB, image: "mariadb:11", port: 3306, admin: "root",
			env: func(p string) []string { return []string{"MARIADB_ROOT_PASSWORD=" + p} },
			seed: []string{
				"CREATE TABLE items (id int auto_increment primary key, name varchar(40))",
				"INSERT INTO items (name) VALUES ('a'),('b'),('c')",
			},
			count: "SELECT count(*) FROM items",
			want:  "3",
		},
	}
	for _, c := range cases {
		t.Run(string(c.engine), func(t *testing.T) { runStreamCase(t, dc, c) })
	}
}

func runStreamCase(t *testing.T, dc docker.Client, c engineCase) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	net := "mb-it-db-" + id
	if _, err := dc.CreateNetwork(ctx, net, "bridge", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.RemoveNetwork(context.Background(), net) })
	if err := dc.PullImage(ctx, c.image, nil); err != nil {
		t.Skipf("pull %s: %v", c.image, err)
	}
	const pass = "adminpw123"
	inst := func(name string) *models.DatabaseInstance {
		cid, err := dc.RunContainer(ctx, docker.RunSpec{Name: name, Image: c.image, Env: c.env(pass), Networks: []string{net}, NetworkAliases: []string{name}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = dc.RemoveContainer(context.Background(), cid, true) })
		return &models.DatabaseInstance{Engine: c.engine, Host: name, Port: c.port, AdminUser: c.admin, NetworkName: net}
	}
	src, dst := inst("mb-it-src-"+id), inst("mb-it-dst-"+id)

	exec := func(i *models.DatabaseInstance, stmts []string) string {
		var out string
		for try := 0; try < 60; try++ {
			cmd, env := clientInvocation(i, stmts, pass)
			code, o, err := dc.RunOneShot(ctx, docker.RunSpec{Image: c.image, Cmd: cmd, Env: env, Networks: []string{net}})
			out = o
			if err == nil && code == 0 {
				return strings.TrimSpace(o)
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("%v: %s", stmts, out)
		return ""
	}
	db := &models.Database{Name: "shop", Username: "u_shop", PasswordEnc: ""}
	exec(src, createDDL(c.engine, db.Name, db.Username, "userpw123"))
	exec(dst, createDDL(c.engine, db.Name, db.Username, "userpw456"))
	inDB := func(stmts []string) []string {
		if c.engine == models.DBEnginePostgres {
			return append([]string{"\\c shop"}, stmts...)
		}
		return append([]string{"USE shop"}, stmts...)
	}
	exec(src, inDB(c.seed))

	dumpCmd, dumpEnv := streamInvocation(src, db, pass, true)
	loadCmd, loadEnv := streamInvocation(dst, db, pass, false)
	svc := transfer.New(localEngine{dc}, func() string { return c.image }, func() string { return c.image })
	n, err := svc.Stream(ctx,
		transfer.StreamEnd{ServerID: 1, Spec: docker.RunSpec{Name: "mb-it-dump-" + id, Image: c.image, Cmd: dumpCmd, Env: dumpEnv, Networks: []string{net}}},
		transfer.StreamEnd{ServerID: 2, Spec: docker.RunSpec{Name: "mb-it-load-" + id, Image: c.image, Cmd: loadCmd, Env: loadEnv, Networks: []string{net}}},
		nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n == 0 {
		t.Fatal("nothing was streamed")
	}
	got := exec(dst, inDB([]string{c.count}))
	if !strings.Contains(got, c.want) {
		t.Errorf("restored count: %q, want %s", got, c.want)
	}
}
