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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
)

// slowQueryThreshold is how long a query runs before the warn level reports it.
const slowQueryThreshold = time.Second

// dbLogger sends GORM's output through Miabi's own logger, so it shares the JSON format and level of
// every other log line instead of printing plain text beside them.
type dbLogger struct {
	level gormlogger.LogLevel
	emit  func(level gormlogger.LogLevel, msg string, args ...any)
}

// NewDBLogger returns a GORM logger at the given level. It never logs bound values, which can carry
// passwords and secrets, and never reports "record not found", which Miabi uses as control flow.
func NewDBLogger(level gormlogger.LogLevel) gormlogger.Interface {
	return dbLogger{level: level, emit: emitToLogger}
}

func emitToLogger(level gormlogger.LogLevel, msg string, args ...any) {
	switch level {
	case gormlogger.Error:
		logger.Error(msg, args...)
	case gormlogger.Warn:
		logger.Warn(msg, args...)
	default:
		// Every-query output is only wanted while debugging.
		logger.Debug(msg, args...)
	}
}

func (l dbLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.level = level
	return l
}

func (l dbLogger) Info(_ context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Info {
		l.emit(gormlogger.Info, "database: "+fmt.Sprintf(msg, args...), "caller", utils.FileWithLineNum())
	}
}

func (l dbLogger) Warn(_ context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Warn {
		l.emit(gormlogger.Warn, "database: "+fmt.Sprintf(msg, args...), "caller", utils.FileWithLineNum())
	}
}

func (l dbLogger) Error(_ context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Error {
		l.emit(gormlogger.Error, "database: "+fmt.Sprintf(msg, args...), "caller", utils.FileWithLineNum())
	}
}

func (l dbLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && l.level >= gormlogger.Error && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		l.emit(gormlogger.Error, "database query failed", "error", err, "sql", sql, "rows", rows,
			"elapsed_ms", elapsed.Milliseconds(), "caller", utils.FileWithLineNum())
	case elapsed > slowQueryThreshold && l.level >= gormlogger.Warn:
		sql, rows := fc()
		l.emit(gormlogger.Warn, "slow database query", "sql", sql, "rows", rows,
			"elapsed_ms", elapsed.Milliseconds(), "caller", utils.FileWithLineNum())
	case l.level >= gormlogger.Info:
		sql, rows := fc()
		l.emit(gormlogger.Info, "database query", "sql", sql, "rows", rows,
			"elapsed_ms", elapsed.Milliseconds(), "caller", utils.FileWithLineNum())
	}
}

// ParamsFilter drops bound values, so a logged query shows placeholders instead of the data.
func (l dbLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}
