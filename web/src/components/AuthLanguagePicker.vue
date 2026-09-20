<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Language picker for the sign-in pages, where there is no account to read a
// preference from. The pick is stored on the device and pushed to the account at
// the next sign-in (stores/language.ts), so choosing here before signing in is
// not lost.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { LANGUAGES } from '@/i18n/languages'
import { useLanguageStore } from '@/stores/language'

const language = useLanguageStore()
const open = ref(false)
const root = ref<HTMLElement | null>(null)

const current = computed(() => LANGUAGES.find((l) => l.code === language.current) ?? LANGUAGES[0])

async function choose(code: (typeof LANGUAGES)[number]['code']) {
  open.value = false
  if (code !== language.current) await language.set(code)
}

function onDocClick(e: MouseEvent) {
  if (open.value && !(e.target as HTMLElement).closest?.('.auth-lang')) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    open.value = false
    root.value?.querySelector<HTMLButtonElement>('.auth-lang-btn')?.focus()
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
  <div ref="root" class="auth-lang">
    <button
      class="auth-lang-btn"
      type="button"
      :title="$t('auth.language')"
      :aria-label="$t('auth.chooseLanguage')"
      :aria-expanded="open"
      aria-haspopup="menu"
      @click.stop="open = !open"
    >
      <span class="mdi mdi-translate"></span>
      <span class="auth-lang-code">{{ current.code.toUpperCase() }}</span>
    </button>

    <Transition name="lang-pop">
      <ul v-if="open" class="auth-lang-menu" role="menu">
        <li v-for="l in LANGUAGES" :key="l.code" role="none">
          <button
            class="auth-lang-item"
            :class="{ active: l.code === language.current }"
            type="button"
            role="menuitemradio"
            :aria-checked="l.code === language.current"
            :lang="l.code"
            @click="choose(l.code)"
          >
            <span class="auth-lang-name">{{ l.label }}</span>
            <span class="mdi mdi-check" :class="{ hidden: l.code !== language.current }"></span>
          </button>
        </li>
      </ul>
    </Transition>
  </div>
</template>

<style scoped>
/* Mirrors the theme button in the opposite corner, and stays put once the hero
   collapses on narrow screens. */
.auth-lang {
  position: fixed;
  bottom: 20px;
  left: 20px;
  z-index: 10;
}

.auth-lang-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  /* Sits over the hero's dark gradient, so it is white on a white-alpha outline rather than the
     theme's text and border colours, which are tuned for the page background. */
  border: 1px solid rgba(255, 255, 255, 0.28);
  border-radius: var(--radius);
  /* Transparent so the picker reads as part of the hero rather than a card on it. No shadow to
     match: one cast by a fill-less box renders as a glow around the outline. */
  background: transparent;
  color: #fff;
  cursor: pointer;
  transition: all var(--transition);
}
.auth-lang-btn:hover,
.auth-lang-btn[aria-expanded='true'] {
  border-color: rgba(255, 255, 255, 0.55);
}

/* Below 900px the hero is hidden (Login.vue) and this button, being fixed, lands on the form
   panel instead — where white on a light theme is invisible. Back to the theme's own colours. */
@media (max-width: 900px) {
  .auth-lang-btn {
    border-color: var(--border-primary);
    color: var(--text-tertiary);
  }
  .auth-lang-btn:hover,
  .auth-lang-btn[aria-expanded='true'] {
    border-color: var(--border-input);
    color: var(--text-primary);
  }
}
.auth-lang-btn .mdi {
  font-size: 18px;
  line-height: 1;
}
.auth-lang-code {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.auth-lang-menu {
  position: absolute;
  bottom: calc(100% + 8px);
  left: 0;
  min-width: 168px;
  margin: 0;
  padding: 4px;
  list-style: none;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  box-shadow: var(--shadow-lg, var(--shadow-sm));
}
.auth-lang-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  border-radius: calc(var(--radius) - 2px);
  background: transparent;
  color: var(--text-secondary, var(--text-primary));
  font-size: 14px;
  text-align: left;
  cursor: pointer;
}
.auth-lang-item:hover {
  background: var(--bg-secondary);
  color: var(--text-primary);
}
.auth-lang-item.active {
  color: var(--text-primary);
  font-weight: 600;
}
.auth-lang-item .mdi {
  font-size: 16px;
  color: var(--primary-600);
}
.auth-lang-item .mdi.hidden {
  visibility: hidden;
}

.lang-pop-enter-active,
.lang-pop-leave-active {
  transition: opacity 120ms ease, transform 120ms ease;
}
.lang-pop-enter-from,
.lang-pop-leave-to {
  opacity: 0;
  transform: translateY(4px);
}
@media (prefers-reduced-motion: reduce) {
  .lang-pop-enter-active,
  .lang-pop-leave-active {
    transition: none;
  }
}
</style>
