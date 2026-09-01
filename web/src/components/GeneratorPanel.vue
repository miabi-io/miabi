<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The generator UI: tabs, options and output. Deliberately owns no chrome of its
// own, so the page, the Vault modal and the inline popover all mount the same
// component rather than three drifting copies.
//
// Everything happens in the browser. No value is ever sent to the API unless the
// user explicitly saves it to the Vault.
import { computed, onMounted, ref, watch } from 'vue'
import {
  DEFAULT_PASSPHRASE_OPTIONS,
  DEFAULT_PASSWORD_OPTIONS,
  DEFAULT_TOKEN_OPTIONS,
  PASSPHRASE_MAX_WORDS,
  PASSPHRASE_MIN_WORDS,
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  TOKEN_MAX_BYTES,
  TOKEN_MIN_BYTES,
  generatePassphrase,
  generatePassword,
  generateToken,
  loadWordlist,
  normalizePasswordOptions,
  passphraseEntropyBits,
  passwordEntropyBits,
  strengthLabel,
  tokenEntropyBits,
  type PassphraseOptions,
  type PasswordOptions,
  type TokenOptions,
} from '@/composables/useGenerator'

export type GeneratorMode = 'password' | 'passphrase' | 'token'

const props = withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })

// The generated value is exposed so a host (the inline button, the modal) can
// use it without reaching into the panel.
const emit = defineEmits<{ (e: 'use', value: string): void; (e: 'save', value: string): void }>()

const OPTIONS_KEY = 'miabi.generator.options'

const mode = ref<GeneratorMode>('password')
const password = ref<PasswordOptions>({ ...DEFAULT_PASSWORD_OPTIONS })
const passphrase = ref<PassphraseOptions>({ ...DEFAULT_PASSPHRASE_OPTIONS })
const token = ref<TokenOptions>({ ...DEFAULT_TOKEN_OPTIONS })

const value = ref('')
const copied = ref(false)
const wordlist = ref<readonly string[]>([])
const wordlistLoading = ref(false)
const wordlistError = ref('')

// Options are remembered between visits; values never are. A generator history
// in localStorage is a pile of plaintext secrets, which is a strictly worse
// place for them than wherever the user was going to put them.
function restoreOptions() {
  try {
    const raw = localStorage.getItem(OPTIONS_KEY)
    if (!raw) return
    const saved = JSON.parse(raw) as {
      mode?: GeneratorMode
      password?: Partial<PasswordOptions>
      passphrase?: Partial<PassphraseOptions>
      token?: Partial<TokenOptions>
    }
    if (saved.mode) mode.value = saved.mode
    if (saved.password) password.value = { ...DEFAULT_PASSWORD_OPTIONS, ...saved.password }
    if (saved.passphrase) passphrase.value = { ...DEFAULT_PASSPHRASE_OPTIONS, ...saved.passphrase }
    if (saved.token) token.value = { ...DEFAULT_TOKEN_OPTIONS, ...saved.token }
  } catch {
    /* corrupt or unavailable storage just means defaults */
  }
}

function persistOptions() {
  try {
    localStorage.setItem(
      OPTIONS_KEY,
      JSON.stringify({ mode: mode.value, password: password.value, passphrase: passphrase.value, token: token.value }),
    )
  } catch {
    /* private mode / blocked storage: options simply are not remembered */
  }
}


async function ensureWordlist() {
  if (wordlist.value.length || wordlistLoading.value) return
  wordlistLoading.value = true
  wordlistError.value = ''
  try {
    wordlist.value = await loadWordlist()
  } catch {
    wordlistError.value = 'Could not load the wordlist. Check your connection and try again.'
  } finally {
    wordlistLoading.value = false
  }
}

function regenerate() {
  copied.value = false
  try {
    if (mode.value === 'password') value.value = generatePassword(password.value)
    else if (mode.value === 'token') value.value = generateToken(token.value)
    else if (wordlist.value.length) value.value = generatePassphrase(passphrase.value, wordlist.value)
    else value.value = ''
  } catch {
    value.value = ''
  }
}

async function selectMode(m: GeneratorMode) {
  mode.value = m
  if (m === 'passphrase') await ensureWordlist()
  regenerate()
}

onMounted(async () => {
  restoreOptions()
  if (mode.value === 'passphrase') await ensureWordlist()
  regenerate()
})

// Any option change regenerates, so the value on screen always matches the
// options beside it — a stale value that no longer obeys the visible settings is
// how someone ends up saving the wrong thing.
watch([password, passphrase, token], () => {
  persistOptions()
  regenerate()
}, { deep: true })
watch(mode, persistOptions)

const bits = computed(() => {
  if (mode.value === 'password') return passwordEntropyBits(password.value)
  if (mode.value === 'token') return tokenEntropyBits(token.value)
  return passphraseEntropyBits(passphrase.value, wordlist.value.length)
})
const strength = computed(() => strengthLabel(bits.value))

// Shown so the length/minimum conflict is visible rather than silently resolved.
const adjusted = computed(() => {
  if (mode.value !== 'password') return ''
  const n = normalizePasswordOptions(password.value)
  const o = password.value
  if (n.minDigits !== o.minDigits || n.minSymbols !== o.minSymbols) {
    return `Minimums trimmed to fit the length: ${n.minDigits} digit(s), ${n.minSymbols} symbol(s).`
  }
  if (!o.upper && !o.lower && !o.digits && !o.symbols) return 'No character set selected — using lowercase letters.'
  return ''
})

async function copy() {
  try {
    await navigator.clipboard.writeText(value.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } catch {

    const el = document.getElementById('generator-output') as HTMLInputElement | null
    el?.select()
  }
}

defineExpose({ regenerate, value })
</script>

<template>
  <div class="generator" :class="{ compact }">
    <div class="gen-tabs" role="tablist" aria-label="Generator type">
      <button v-for="m in (['password', 'passphrase', 'token'] as GeneratorMode[])" :key="m" type="button" role="tab"
        class="gen-tab" :class="{ active: mode === m }" :aria-selected="mode === m" @click="selectMode(m)">
        {{ m === 'password' ? 'Password' : m === 'passphrase' ? 'Passphrase' : 'Token' }}
      </button>
    </div>

    <!-- Output first: it is what the user came for. -->
    <div class="gen-output">
      <input id="generator-output" class="form-input gen-value" :value="value" readonly aria-label="Generated value"
        :placeholder="wordlistLoading ? 'Loading wordlist…' : ''" @focus="($event.target as HTMLInputElement).select()" />
      <button type="button" class="btn btn-icon" title="Generate another" aria-label="Generate another"
        @click="regenerate">
        <span class="mdi mdi-refresh"></span>
      </button>
      <button type="button" class="btn btn-icon" :title="copied ? 'Copied' : 'Copy'" aria-label="Copy" :disabled="!value"
        @click="copy">
        <span class="mdi" :class="copied ? 'mdi-check' : 'mdi-content-copy'"></span>
      </button>
    </div>

    <div class="gen-meta">
      <span class="gen-bits" :class="`tone-${strength.tone}`">
        {{ Math.round(bits) }} bits
        <span class="text-muted">· {{ strength.label }}</span>
      </span>
      <span v-if="mode === 'passphrase' && wordlist.length" class="text-muted gen-note">
        {{ wordlist.length.toLocaleString() }}-word EFF list · {{ Math.log2(wordlist.length).toFixed(1) }} bits/word
      </span>
    </div>

    <p v-if="wordlistError" class="form-hint gen-error">{{ wordlistError }}</p>
    <p v-else-if="adjusted" class="form-hint gen-warn">{{ adjusted }}</p>

    <!-- Password -->
    <div v-if="mode === 'password'" class="gen-options">
      <label class="gen-range">
        <span>Length <strong>{{ password.length }}</strong></span>
        <input v-model.number="password.length" type="range" :min="PASSWORD_MIN_LENGTH" :max="PASSWORD_MAX_LENGTH" />
      </label>
      <div class="gen-checks">
        <label><input v-model="password.upper" type="checkbox" /> A–Z</label>
        <label><input v-model="password.lower" type="checkbox" /> a–z</label>
        <label><input v-model="password.digits" type="checkbox" /> 0–9</label>
        <label><input v-model="password.symbols" type="checkbox" /> Symbols</label>
      </div>
      <div class="gen-mins">
        <label>
          <span class="form-label">Min. digits</span>
          <input v-model.number="password.minDigits" type="number" min="0" :max="password.length" class="form-input"
            :disabled="!password.digits" />
        </label>
        <label>
          <span class="form-label">Min. symbols</span>
          <input v-model.number="password.minSymbols" type="number" min="0" :max="password.length" class="form-input"
            :disabled="!password.symbols" />
        </label>
      </div>
    </div>

    <!-- Passphrase -->
    <div v-else-if="mode === 'passphrase'" class="gen-options">
      <label class="gen-range">
        <span>Words <strong>{{ passphrase.words }}</strong></span>
        <input v-model.number="passphrase.words" type="range" :min="PASSPHRASE_MIN_WORDS" :max="PASSPHRASE_MAX_WORDS" />
      </label>
      <div class="gen-mins">
        <label>
          <span class="form-label">Separator</span>
          <input v-model="passphrase.separator" class="form-input" maxlength="3" />
        </label>
      </div>
      <div class="gen-checks">
        <label><input v-model="passphrase.capitalize" type="checkbox" /> Capitalise</label>
        <label><input v-model="passphrase.includeNumber" type="checkbox" /> Include a number</label>
      </div>
    </div>

    <!-- Token -->
    <div v-else class="gen-options">
      <label class="gen-range">
        <span>Bytes <strong>{{ token.bytes }}</strong></span>
        <input v-model.number="token.bytes" type="range" :min="TOKEN_MIN_BYTES" :max="TOKEN_MAX_BYTES" />
      </label>
      <div class="gen-radios" role="radiogroup" aria-label="Encoding">
        <label><input v-model="token.encoding" type="radio" value="base64url" /> base64url</label>
        <label><input v-model="token.encoding" type="radio" value="hex" /> hex</label>
      </div>
    </div>

    <div class="gen-actions">
      <slot name="actions" :value="value" :regenerate="regenerate">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="!value" @click="emit('use', value)">
          Use this value
        </button>
      </slot>
    </div>
  </div>
</template>

<style scoped>
.generator { display: flex; flex-direction: column; gap: 12px; }
.gen-tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--border-primary); }
.gen-tab {
  border: 0; background: transparent; color: var(--text-secondary, var(--text-muted));
  padding: 7px 12px; font-size: 13px; font-weight: 500; cursor: pointer;
  border-bottom: 2px solid transparent; margin-bottom: -1px;
}
.gen-tab:hover { color: var(--text-primary); }
.gen-tab.active { color: var(--primary-500, #6366f1); border-bottom-color: var(--primary-500, #6366f1); }

.gen-output { display: flex; align-items: center; gap: 6px; }
.gen-value { font-family: monospace; font-size: 14px; flex: 1; min-width: 0; }

.gen-meta { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; flex-wrap: wrap; margin-top: -4px; }
.gen-bits { font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; }
.gen-note { font-size: 11px; }
.tone-weak { color: var(--danger-500, #ef4444); }
.tone-fair { color: var(--warning-500, #f59e0b); }
.tone-good { color: var(--success-500, #22c55e); }
.tone-strong { color: var(--success-500, #22c55e); }
.gen-error { color: var(--danger-500, #ef4444); }
.gen-warn { color: var(--warning-500, #f59e0b); }

.gen-options { display: flex; flex-direction: column; gap: 12px; }
.gen-range { display: flex; flex-direction: column; gap: 5px; font-size: 12px; color: var(--text-secondary, var(--text-muted)); }
.gen-range input[type='range'] { width: 100%; accent-color: var(--primary-500, #6366f1); }
.gen-checks { display: flex; flex-wrap: wrap; gap: 10px 16px; font-size: 13px; color: var(--text-primary); }
.gen-checks label { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
.gen-checks input { width: auto; margin: 0; }
.gen-mins { display: flex; gap: 10px; flex-wrap: wrap; }
.gen-mins label { flex: 1; min-width: 120px; }
.gen-actions { display: flex; gap: 8px; flex-wrap: wrap; }

.gen-radios {display: flex;flex-wrap: wrap;gap: 10px 16px;font-size: 13px;color: var(--text-primary);}
.gen-radios label {display: inline-flex;align-items: center;gap: 8px;color: var(--text-secondary);cursor: pointer;user-select: none;}

.compact .gen-tab { padding: 6px 9px; font-size: 12px; }
.compact .gen-value { font-size: 13px; }

.btn-icon {width: 34px;height: 34px;padding: 0;display: inline-flex;align-items: center;justify-content: center;
background-color: var(--bg-tertiary);color: var(--text-secondary);border: 1px solid var(--border-primary);border-radius: var(--radius);
cursor: pointer;user-select: none;transition: background-color var(--transition),  border-color var(--transition),  color var(--transition),  transform 100ms ease;}
.btn-icon:hover:not(:disabled) {background-color: var(--bg-hover);color: var(--text-primary);border-color: var(--border-input);}
.btn-icon:active:not(:disabled) {transform: scale(0.95);background-color: var(--bg-secondary);}
.btn-icon:focus-visible {outline: none;border-color: var(--border-focus);box-shadow: var(--shadow-focus);}
.btn-icon:disabled {opacity: 0.4;cursor: not-allowed;background-color: var(--bg-tertiary);border-color: var(--border-primary);}
.btn-icon .mdi {font-size: 18px;line-height: 1;}
.btn-icon .mdi-check {color: var(--success-600);}
[data-theme="dark"] .btn-icon .mdi-check {color: var(--success-400);}
</style>
