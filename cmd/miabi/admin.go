// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/okapi/okapicli"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// registerAdminCommands adds the last-resort recovery for an admin locked out of the console
// unlock: host access to the control plane is the trust anchor, so it runs there, not over the API.
func registerAdminCommands(cli *okapicli.CLI) {
	cli.Command("reset-2fa", "Turn off two-factor authentication for a user (run inside the control-plane container)", runResetTwoFactor).
		String("email", "e", "", "The user's email address").
		Bool("yes", "y", false, "Do not prompt")
}

func runResetTwoFactor(cmd *okapicli.Command) error {
	email := strings.TrimSpace(cmd.GetString("email"))
	if email == "" {
		return errors.New("--email is required")
	}
	cfg := config.New()
	_ = cfg.InitWorker()
	cfg.InitStorage()
	db := cfg.Database.DB
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	users := repositories.NewUserRepository(db)
	u, err := users.FindByEmail(email)
	if err != nil {
		return fmt.Errorf("no user with email %s", email)
	}
	if !u.TwoFactorEnabled {
		fmt.Printf("%s does not have two-factor authentication enabled; nothing to do.\n", email)
		return nil
	}
	if !cmd.GetBool("yes") && !confirm(fmt.Sprintf("Turn off two-factor authentication for %s? They will set it up again at next sign-in to the admin console.", email)) {
		fmt.Println("Aborted.")
		return nil
	}
	u.TwoFactorEnabled = false
	u.TwoFactorSecret = ""
	if err := users.Update(u); err != nil {
		return err
	}
	if err := repositories.NewTwoFactorRecoveryRepository(db).DeleteForUser(u.ID); err != nil {
		return err
	}
	actor := u.ID
	_ = repositories.NewAuditLogRepository(db).Create(&models.AuditLog{
		ActorID: &actor, Action: "user.2fa_reset_from_host", TargetType: "user", TargetID: fmt.Sprint(u.ID),
	})
	fmt.Printf("Two-factor authentication turned off for %s.\n", email)
	return nil
}
