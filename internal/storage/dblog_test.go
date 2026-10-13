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

package storage

import (
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type secretRow struct {
	ID    uint
	Token string `gorm:"uniqueIndex"`
}

type logLines []string

func openLogged(t *testing.T, level gormlogger.LogLevel) (*gorm.DB, *logLines) {
	t.Helper()
	lines := &logLines{}
	l := dbLogger{level: level, emit: func(lv gormlogger.LogLevel, msg string, args ...any) {
		*lines = append(*lines, fmt.Sprint(lv, " ", msg, " ", args))
	}}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: l})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&secretRow{}); err != nil {
		t.Fatal(err)
	}
	*lines = nil
	return db, lines
}

// A failed query is reported, but never with the values bound to it.
func TestDBLoggerReportsErrorsWithoutBoundValues(t *testing.T) {
	db, lines := openLogged(t, gormlogger.Error)
	db.Create(&secretRow{Token: "s3cr3t-token"})
	*lines = nil

	if err := db.Create(&secretRow{Token: "s3cr3t-token"}).Error; err == nil {
		t.Fatal("expected a unique-constraint error")
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "database query failed") {
		t.Fatalf("lines = %q, want one failed-query line", *lines)
	}
	if strings.Contains((*lines)[0], "s3cr3t-token") {
		t.Fatalf("a bound value leaked into the log: %s", (*lines)[0])
	}
}

func TestDBLoggerIgnoresRecordNotFound(t *testing.T) {
	db, lines := openLogged(t, gormlogger.Info)
	var row secretRow
	if err := db.Where("token = ?", "missing").First(&row).Error; err == nil {
		t.Fatal("expected record not found")
	}
	for _, l := range *lines {
		if strings.Contains(l, "failed") {
			t.Fatalf("record not found was reported as a failure: %s", l)
		}
	}
}

func TestDBLoggerLevels(t *testing.T) {
	for _, tc := range []struct {
		level gormlogger.LogLevel
		want  int
	}{{gormlogger.Silent, 0}, {gormlogger.Error, 0}, {gormlogger.Warn, 0}, {gormlogger.Info, 1}} {
		db, lines := openLogged(t, tc.level)
		var n int64
		db.Model(&secretRow{}).Where("token = ?", "s3cr3t-token").Count(&n)
		if len(*lines) != tc.want {
			t.Errorf("level %d: %d lines, want %d: %q", tc.level, len(*lines), tc.want, *lines)
		}
		for _, l := range *lines {
			if strings.Contains(l, "s3cr3t-token") {
				t.Errorf("level %d: a bound value leaked: %s", tc.level, l)
			}
		}
	}
}
