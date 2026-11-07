<script setup lang="ts">
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

// The attribution matters on white-labelled installs, where nothing else says what the platform is.
const HOME = 'https://miabi.io'
const currentYear = new Date().getFullYear()
</script>

<template>
  <footer class="auth-foot">
    <span>&copy; {{ currentYear }} Miabi Project</span>
    <span class="auth-foot-sep">·</span>
    <i18n-t keypath="shell.footer.poweredBy" tag="span">
      <template #miabi>
        <a :href="HOME" target="_blank" rel="noopener noreferrer">Miabi</a>
      </template>
    </i18n-t>
  </footer>
</template>

<style scoped>
.auth-foot {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex-wrap: wrap;
  margin: 24px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}
.auth-foot-sep {
  opacity: 0.6;
}
.auth-foot a {
  color: inherit;
  font-weight: 600;
  text-decoration: none;
}
.auth-foot a:hover {
  color: var(--primary-600);
  text-decoration: underline;
}
</style>
