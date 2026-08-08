// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/jkaninda/okapi/okapicli"
)

func main() {
	app := okapi.New()
	cli := okapicli.New(app, "Miabi")

	cli.Command("server", "Start Miabi API server", func(cmd *okapicli.Command) error {
		logger.Info("Starting Miabi Server...")
		runServer(cli)
		return nil
	})

	cli.Command("worker", "Start Miabi background worker", func(cmd *okapicli.Command) error {
		logger.Info("Starting Miabi Worker...")
		if err := runWorker(); err != nil {
			logger.Fatal("worker server error", "error", err)
		}
		return nil
	})

	// install / update / restart / status / uninstall.
	registerStackCommands(cli)

	// Disaster recovery: rebuild this platform on a fresh host from a recovery
	// point. Also a host command — the machine it runs on has no Miabi yet.
	registerRestoreCommands(cli)

	cli.DefaultCommand("server")

	if err := cli.Execute(); err != nil {
		logger.Fatal(err.Error())
	}
}
