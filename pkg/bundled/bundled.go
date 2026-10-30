// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package bundled names the component versions a Miabi build provisions. Release builds set them
// from versions.miabi.io with -ldflags -X, so a component bump needs no code change; the fallbacks
// here are what a development build uses.
package bundled

import "strings"

// fallbackGatewayVersion is the Goma Gateway a build without -X provisions.
const fallbackGatewayVersion = "1.1.2"

// gatewayVersion is set at link time:
//
//	-X github.com/miabi-io/miabi/pkg/bundled.gatewayVersion=v1.1.2
//
// A var, as -X cannot set a constant.
var gatewayVersion = fallbackGatewayVersion

// GatewayVersion is the Goma Gateway release this build provisions and was tested with, without a
// leading "v". An empty -X value falls back rather than producing an image reference with no tag.
func GatewayVersion() string {
	if v := strings.TrimPrefix(strings.TrimSpace(gatewayVersion), "v"); v != "" {
		return v
	}
	return fallbackGatewayVersion
}

// GatewayImage is the Goma Gateway image this build provisions.
func GatewayImage() string { return "jkaninda/goma-gateway:" + GatewayVersion() }

const (
	PostgresVersion = "17-alpine"
	RedisVersion    = "7-alpine"

	PostgresImage = "postgres:" + PostgresVersion
	RedisImage    = "redis:" + RedisVersion
)
