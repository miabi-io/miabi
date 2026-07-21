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

	// install / update / status / uninstall. These run on the host, outside the
	// control-plane container — which is what lets Miabi replace its own container.
	registerStackCommands(cli)

	cli.DefaultCommand("server")

	if err := cli.Execute(); err != nil {
		logger.Fatal(err.Error())
	}
}
