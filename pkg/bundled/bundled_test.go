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

package bundled

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGatewayVersion(t *testing.T) {
	defer func(v string) { gatewayVersion = v }(gatewayVersion)
	cases := map[string]string{"v1.2.3": "1.2.3", "1.2.3": "1.2.3", " v1.2.3 ": "1.2.3", "": fallbackGatewayVersion, "v": fallbackGatewayVersion}
	for in, want := range cases {
		gatewayVersion = in
		if got := GatewayVersion(); got != want {
			t.Errorf("gatewayVersion %q: GatewayVersion() = %q, want %q", in, got, want)
		}
	}
	gatewayVersion = "v1.2.3"
	if got := GatewayImage(); got != "jkaninda/goma-gateway:1.2.3" {
		t.Errorf("GatewayImage() = %q", got)
	}
}

// The Compose files cannot import these constants, so they are held to them here.
func TestComposeFilesPinTheBundledDataStores(t *testing.T) {
	image := regexp.MustCompile(`(?m)^\s*image:\s*((?:postgres|redis):\S+)`)
	for _, f := range []string{
		filepath.Join("..", "..", "compose.dev.yaml"),
		filepath.Join("..", "..", "examples", "compose", "compose.yaml"),
		filepath.Join("..", "..", "examples", "compose", "compose.traefik.yaml"),
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		found := image.FindAllStringSubmatch(string(b), -1)
		if len(found) != 2 {
			t.Errorf("%s: want a postgres and a redis image, found %v", f, found)
		}
		for _, m := range found {
			if m[1] != PostgresImage && m[1] != RedisImage {
				t.Errorf("%s pins %s; pkg/bundled says %s and %s", f, m[1], PostgresImage, RedisImage)
			}
		}
	}
}
