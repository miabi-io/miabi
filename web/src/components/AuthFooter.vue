<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Footer under the sign-in card, matching the console's. The attribution earns its
// place on a white-labelled install, where nothing else on the page says what the
// platform is; on an unbranded one it sits under a Miabi logo, so it is quiet
// enough to read as a footer rather than a second wordmark.
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
