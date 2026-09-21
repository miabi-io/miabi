<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Theme and language for the sign-in pages, in one popover. Both are device
// settings until there is an account: the theme pick is held as unsynced and the
// language is stored locally, and each is pushed to the account at the next
// sign-in (stores/theme.ts, stores/language.ts).
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LANGUAGES } from '@/i18n/languages'
import { useLanguageStore } from '@/stores/language'
import { useThemeStore } from '@/stores/theme'
import type { ThemeMode } from '@/stores/theme'

const { t } = useI18n()
const language = useLanguageStore()
const theme = useThemeStore()

const open = ref(false)
const trigger = ref<HTMLButtonElement | null>(null)

// Same order and icons as the console's user menu, so the control is the one
// users already know once they are signed in.
const themeModes = computed<{ value: ThemeMode; label: string; icon: string }[]>(() => [
  { value: 'system', label: t('preferences.theme.system'), icon: 'mdi-monitor' },
  { value: 'light', label: t('preferences.theme.light'), icon: 'mdi-white-balance-sunny' },
  { value: 'dark', label: t('preferences.theme.dark'), icon: 'mdi-weather-night' },
])

async function chooseLanguage(code: (typeof LANGUAGES)[number]['code']) {
  if (code !== language.current) await language.set(code)
  close()
}

function close() {
  open.value = false
}

function onDocClick(e: MouseEvent) {
  if (open.value && !(e.target as HTMLElement).closest?.('.auth-options')) close()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    close()
    trigger.value?.focus()
  }
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="auth-options">
    <button
      ref="trigger"
      class="auth-options-btn"
      type="button"
      :title="$t('auth.displayOptions')"
      :aria-label="$t('auth.displayOptions')"
      :aria-expanded="open"
      aria-haspopup="true"
      @click.stop="open = !open"
    >
      <span class="mdi mdi-dots-vertical"></span>
    </button>

    <Transition name="options-pop">
      <div v-if="open" class="auth-options-menu">
        <div class="auth-options-group">
          <p class="auth-options-label" id="auth-appearance-label">
            <span class="mdi mdi-theme-light-dark"></span>{{ $t('preferences.appearance.title') }}
          </p>
          <div class="auth-options-segment" role="radiogroup" aria-labelledby="auth-appearance-label">
            <button
              v-for="m in themeModes"
              :key="m.value"
              class="auth-options-seg-btn"
              :class="{ active: theme.mode === m.value }"
              type="button"
              role="radio"
              :aria-checked="theme.mode === m.value"
              :title="m.label"
              :aria-label="m.label"
              @click="theme.setMode(m.value)"
            >
              <span class="mdi" :class="m.icon"></span>
            </button>
          </div>
        </div>

        <div class="auth-options-divider"></div>

        <div class="auth-options-group">
          <p class="auth-options-label" id="auth-language-label">
            <span class="mdi mdi-translate"></span>{{ $t('auth.language') }}
          </p>
          <div role="radiogroup" aria-labelledby="auth-language-label">
            <button
              v-for="l in LANGUAGES"
              :key="l.code"
              class="auth-options-item"
              :class="{ active: l.code === language.current }"
              type="button"
              role="radio"
              :aria-checked="l.code === language.current"
              :lang="l.code"
              @click="chooseLanguage(l.code)"
            >
              <span>{{ l.label }}</span>
              <span class="mdi mdi-check" :class="{ hidden: l.code !== language.current }"></span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
/* Fixed so it holds the corner of the form panel once the hero collapses. */
.auth-options {
  position: fixed;
  top: 20px;
  right: 20px;
  z-index: 20;
}

.auth-options-btn {
  display: flex;
  align-items: center;
  padding: 9px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  color: var(--text-tertiary);
  box-shadow: var(--shadow-sm);
  cursor: pointer;
  transition: all var(--transition);
}
.auth-options-btn:hover,
.auth-options-btn[aria-expanded='true'] {
  color: var(--text-primary);
  border-color: var(--border-input);
}
.auth-options-btn .mdi {
  font-size: 18px;
  line-height: 1;
}

.auth-options-menu {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  min-width: 208px;
  padding: 6px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  box-shadow: var(--shadow-lg, var(--shadow-sm));
}
.auth-options-group {
  padding: 2px;
}
.auth-options-label {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 2px 0 8px;
  padding: 0 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.auth-options-label .mdi {
  font-size: 14px;
}
.auth-options-divider {
  height: 1px;
  margin: 6px 0;
  background: var(--border-primary);
}

.auth-options-segment {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 2px;
  padding: 2px;
  border-radius: var(--radius);
  background: var(--bg-secondary);
}
.auth-options-seg-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 7px 0;
  border: 0;
  border-radius: calc(var(--radius) - 3px);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  transition: all var(--transition);
}
.auth-options-seg-btn:hover {
  color: var(--text-primary);
}
.auth-options-seg-btn.active {
  background: var(--bg-primary);
  color: var(--primary-600);
  box-shadow: var(--shadow-sm);
}
.auth-options-seg-btn .mdi {
  font-size: 17px;
  line-height: 1;
}

.auth-options-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  padding: 8px 8px;
  border: 0;
  border-radius: calc(var(--radius) - 2px);
  background: transparent;
  color: var(--text-secondary, var(--text-primary));
  font-size: 14px;
  text-align: left;
  cursor: pointer;
}
.auth-options-item:hover {
  background: var(--bg-secondary);
  color: var(--text-primary);
}
.auth-options-item.active {
  color: var(--text-primary);
  font-weight: 600;
}
.auth-options-item .mdi {
  font-size: 16px;
  color: var(--primary-600);
}
.auth-options-item .mdi.hidden {
  visibility: hidden;
}

.options-pop-enter-active,
.options-pop-leave-active {
  transition: opacity 120ms ease, transform 120ms ease;
}
.options-pop-enter-from,
.options-pop-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
@media (prefers-reduced-motion: reduce) {
  .options-pop-enter-active,
  .options-pop-leave-active {
    transition: none;
  }
}
</style>
