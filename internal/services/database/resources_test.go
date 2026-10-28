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

package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestTuningArgs(t *testing.T) {
	const gb = int64(1 << 30)
	for _, tc := range []struct {
		engine models.DBEngine
		memory int64
		want   []string
	}{
		{models.DBEnginePostgres, gb, []string{"-c", "shared_buffers=256MB", "-c", "effective_cache_size=768MB"}},
		{models.DBEngineMySQL, gb / 4, []string{"--innodb-buffer-pool-size=64M", "--performance-schema=OFF"}},
		{models.DBEngineMySQL, gb, []string{"--innodb-buffer-pool-size=384M"}},
		{models.DBEngineMySQL, 2 * gb, []string{"--innodb-buffer-pool-size=1152M"}},
		{models.DBEngineMariaDB, gb / 4, []string{"--innodb-buffer-pool-size=96M"}},
		{models.DBEngineMariaDB, gb / 2, []string{"--innodb-buffer-pool-size=307M"}},
		{models.DBEngineRedis, gb / 2, []string{"--maxmemory", "402653184"}},
		{models.DBEngineMongoDB, gb / 2, []string{"--wiredTigerCacheSizeGB", "0.25"}},
		{models.DBEngineMongoDB, 4 * gb, []string{"--wiredTigerCacheSizeGB", "1.50"}},
		{models.DBEngineLibSQL, gb, nil},
		{models.DBEnginePostgres, 0, nil},
	} {
		if got := tuningArgs(tc.engine, tc.memory); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tuningArgs(%s, %d) = %v, want %v", tc.engine, tc.memory, got, tc.want)
		}
	}
}

func TestResourcesValidate(t *testing.T) {
	pg := specs[models.DBEnginePostgres]
	for _, bad := range []Resources{{MemoryBytes: 64 << 20}, {NanoCPUs: -1}, {MemoryBytes: -1}} {
		if err := bad.validate(models.DBEnginePostgres, pg); !errors.Is(err, ErrInvalidResources) {
			t.Errorf("%+v: err = %v, want ErrInvalidResources", bad, err)
		}
	}
	for _, good := range []Resources{{}, {MemoryBytes: 256 << 20, NanoCPUs: 500_000_000}} {
		if err := good.validate(models.DBEnginePostgres, pg); err != nil {
			t.Errorf("%+v: err = %v", good, err)
		}
	}
	mongo := specs[models.DBEngineMongoDB]
	if err := (Resources{MemoryBytes: 512 << 20, NanoCPUs: nanosPerCore / 4}).validate(models.DBEngineMongoDB, mongo); !errors.Is(err, ErrInvalidResources) || !strings.Contains(err.Error(), "0.5 CPU") {
		t.Errorf("a quarter core for mongodb: err = %v, want a 0.5 CPU minimum", err)
	}
	if err := (Resources{MemoryBytes: 512 << 20, NanoCPUs: nanosPerCore / 2}).validate(models.DBEngineMongoDB, mongo); err != nil {
		t.Errorf("mongodb at its minimum: err = %v", err)
	}
}

// An instance created without a size in a capped workspace must still count against the budget.
func TestResourcesWithDefaults(t *testing.T) {
	mysql := specs[models.DBEngineMySQL]
	if got := (Resources{}).withDefaults(mysql, true, true); got != (Resources{MemoryBytes: 1024 << 20, NanoCPUs: nanosPerCore}) {
		t.Errorf("capped defaults = %+v", got)
	}
	if got := (Resources{MemoryBytes: 2048 << 20}).withDefaults(mysql, false, true); got != (Resources{MemoryBytes: 2048 << 20}) {
		t.Errorf("a stated memory limit was replaced: %+v", got)
	}
	if got := (Resources{}).withDefaults(specs[models.DBEngineRedis], true, true); got != (Resources{MemoryBytes: 256 << 20, NanoCPUs: nanosPerCore / 4}) {
		t.Errorf("redis capped defaults = %+v", got)
	}
	if got := (Resources{}).withDefaults(mysql, false, false); got != (Resources{}) {
		t.Errorf("an uncapped workspace got a size: %+v", got)
	}
}
