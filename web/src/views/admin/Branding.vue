<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { authApi } from '@/api/auth'
import { brandingApi, type BrandAssetSlot, type BrandingSettings } from '@/api/resources'
import type { AccentCode, AccentPolicy, BrandLink } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useBrandStore } from '@/stores/brand'
import { useNotificationStore } from '@/stores/notification'
import { useThemeStore } from '@/stores/theme'
import { ACCENTS } from '@/theme/accents'

const MAX_LINKS = 6
const IMAGE_TYPES = 'image/png,image/jpeg,image/webp,image/svg+xml,image/x-icon,.ico'

interface BrandForm {
  name: string
  logo_url: string
  logo_dark_url: string
  accent: AccentCode
  accent_policy: AccentPolicy
  signin_notice: string
  links: BrandLink[]
}

const notify = useNotificationStore()
const auth = useAuthStore()
const theme = useThemeStore()
const brandStore = useBrandStore()

const brand = ref<BrandingSettings | null>(null)
const locked = ref(false)
const loading = ref(true)
const saving = ref(false)
const busySlot = ref<BrandAssetSlot | null>(null)
const form = ref<BrandForm>(toForm({}))
const saved = ref('')

const editable = computed(() => !!brand.value?.editable)
const dirty = computed(() => JSON.stringify(form.value) !== saved.value)
const accents = computed(() => ACCENTS.filter((a) => brand.value?.accents.includes(a.code)))
const noticeLength = computed(() => [...form.value.signin_notice].length)

const assetSlots: { slot: BrandAssetSlot; label: string; hint: string; urlKey?: 'logo_url' | 'logo_dark_url' }[] = [
  { slot: 'logo', label: 'Logo', hint: 'For light backgrounds: the sign-in page.', urlKey: 'logo_url' },
  {
    slot: 'logo_dark',
    label: 'Logo for dark backgrounds',
    hint: 'The sidebar and the dark sign-in page. Without one, the logo above is used.',
    urlKey: 'logo_dark_url',
  },
  { slot: 'favicon', label: 'Favicon', hint: 'The browser tab icon. SVG or ICO stay sharp at every size.' },
]

const policies: { value: AccentPolicy; label: string; hint: string }[] = [
  {
    value: 'default',
    label: 'Default',
    hint: 'Accounts start with this accent and can pick their own in Preferences.',
  },
  {
    value: 'enforced',
    label: 'Enforced',
    hint: 'Every account uses this accent. Personal picks are kept and come back if you switch to Default.',
  },
]

// Only preview a URL that could load, so a half-typed one does not fire a request
// per keystroke.
function previewable(url: string): string {
  const u = url.trim()
  return /^https?:\/\/[^/\s]+\/\S+$/i.test(u) ? u : ''
}

function assetUrl(slot: BrandAssetSlot): string {
  return brand.value?.assets?.[slot]?.url ?? ''
}

const lightLogo = computed(() => assetUrl('logo') || previewable(form.value.logo_url))
const lightPreview = computed(() => lightLogo.value || '/brand/miabi-mark.svg')
const darkPreview = computed(
  () => assetUrl('logo_dark') || previewable(form.value.logo_dark_url) || lightLogo.value || '/brand/miabi-mark-white.svg',
)
const faviconPreview = computed(() => assetUrl('favicon') || '/favicon.svg')

function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${Math.round(bytes / 1024)} KB`
}

function toForm(b: Partial<BrandingSettings>): BrandForm {
  return {
    name: b.name ?? '',
    logo_url: b.logo_url ?? '',
    logo_dark_url: b.logo_dark_url ?? '',
    accent: b.accent || 'default',
    accent_policy: b.accent_policy || 'default',
    signin_notice: b.signin_notice ?? '',
    links: (b.links ?? []).map((l) => ({ ...l })),
  }
}

function apply(b: BrandingSettings) {
  brand.value = b
  form.value = toForm(b)
  saved.value = JSON.stringify(form.value)
}

async function load() {
  try {
    apply((await brandingApi.get()).data.data)
  } catch (e) {
    // 402 is an install without white_label, which the page explains instead.
    if ((e as { response?: { status?: number } }).response?.status === 402) locked.value = true
    else notify.apiError(e)
  } finally {
    loading.value = false
  }
}

function addLink() {
  if (form.value.links.length < MAX_LINKS) form.value.links.push({ label: '', url: '' })
}

async function save() {
  saving.value = true
  try {
    apply((await brandingApi.update(form.value)).data.data)
    void brandStore.load(true)
    notify.success('Branding saved')
    void refreshOwnAccent()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

// Uploads apply at once, apart from the form, so unsaved edits there are kept.
function applyAssets(b: BrandingSettings) {
  if (brand.value) brand.value = { ...brand.value, assets: b.assets }
  void brandStore.load(true)
}

async function upload(slot: BrandAssetSlot, event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || !brand.value) return
  if (file.size > brand.value.max_asset_bytes) {
    notify.error(`Images may be at most ${formatSize(brand.value.max_asset_bytes)}.`)
    return
  }
  busySlot.value = slot
  try {
    applyAssets((await brandingApi.uploadAsset(slot, file)).data.data)
    notify.success('Image uploaded')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busySlot.value = null
  }
}

async function removeAsset(slot: BrandAssetSlot) {
  busySlot.value = slot
  try {
    applyAssets((await brandingApi.deleteAsset(slot)).data.data)
    notify.success('Image removed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busySlot.value = null
  }
}

// The brand accent and its policy cover this admin's account too, so the console
// changes now rather than at the next sign-in.
async function refreshOwnAccent() {
  try {
    const me = (await authApi.me()).data.data
    auth.setUser(me)
    const prefs = me.preferences
    if (prefs) theme.adopt(prefs.theme, prefs.accent, prefs.accent_locked)
  } catch {
    // Cosmetic: the next console load adopts it anyway.
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Branding</h1>
        <p class="text-muted">Your organization's name, logo and colour on the sign-in page and across the console.</p>
      </div>
      <button v-if="brand" class="btn btn-primary" :disabled="!editable || !dirty || saving" @click="save">
        <span class="mdi mdi-content-save"></span>
        {{ saving ? 'Saving…' : 'Save changes' }}
      </button>
    </div>

    <div v-if="loading" class="spinner"></div>

    <div v-else-if="locked" class="card">
      <div class="card-body locked">
        <span class="mdi mdi-lock-outline"></span>
        <div>
          <p>
            White-label branding is an Enterprise feature. Put your organization's name, logo and
            accent on the sign-in page and the console, and decide whether accounts may change the accent.
          </p>
          <router-link to="/admin/license" class="btn btn-secondary btn-sm">Manage license</router-link>
        </div>
      </div>
    </div>

    <div v-else-if="brand" class="space-y">
      <p v-if="!editable" class="readonly-note">
        <span class="mdi mdi-lock-outline"></span>
        Your licence can show this branding but not change it. What is already set stays in place — renew to edit it.
      </p>

      <div class="card">
        <div class="card-header"><h2>Identity</h2></div>
        <div class="card-body">
          <div class="form-group">
            <label class="form-label" for="brand-name">Name</label>
            <input id="brand-name" v-model="form.name" class="form-input" placeholder="Miabi" :disabled="!editable" />
            <p class="form-hint">Replaces "Miabi" on the sign-in page, in the sidebar and in browser tab titles. Blank keeps it.</p>
          </div>

          <div v-for="s in assetSlots" :key="s.slot" class="asset-row">
            <div class="asset-info">
              <span class="form-label">{{ s.label }}</span>
              <span class="form-hint">{{ s.hint }}</span>
              <input
                v-if="s.urlKey && !brand.assets[s.slot]"
                v-model="form[s.urlKey]"
                class="form-input asset-url"
                :aria-label="`${s.label} URL`"
                placeholder="Upload an image, or link to https://…"
                :disabled="!editable"
              />
            </div>
            <div class="asset-actions">
              <span v-if="brand.assets[s.slot]" class="asset-size">{{ formatSize(brand.assets[s.slot]?.size ?? 0) }}</span>
              <label class="btn btn-sm btn-secondary asset-pick" :class="{ disabled: !editable || busySlot !== null }">
                <input
                  type="file"
                  class="asset-file"
                  :accept="IMAGE_TYPES"
                  :disabled="!editable || busySlot !== null"
                  @change="upload(s.slot, $event)"
                />
                {{ busySlot === s.slot ? 'Working…' : brand.assets[s.slot] ? 'Replace' : 'Upload' }}
              </label>
              <button
                v-if="brand.assets[s.slot]"
                type="button"
                class="btn btn-sm btn-ghost"
                :disabled="!editable || busySlot !== null"
                @click="removeAsset(s.slot)"
              >
                Remove
              </button>
            </div>
          </div>

          <div class="logo-previews" aria-hidden="true">
            <div class="logo-preview logo-preview-light">
              <img :src="lightPreview" alt="" />
              <span class="logo-preview-name">{{ form.name || 'Miabi' }}</span>
              <span class="logo-preview-caption">Sign-in page</span>
            </div>
            <div class="logo-preview logo-preview-dark">
              <img :src="darkPreview" alt="" />
              <span class="logo-preview-name">{{ form.name || 'Miabi' }}</span>
              <span class="logo-preview-caption">Sidebar</span>
            </div>
            <div class="logo-preview logo-preview-tab">
              <img :src="faviconPreview" alt="" class="tab-icon" />
              <span class="tab-title">Dashboard — {{ form.name || 'Miabi' }}</span>
              <span class="logo-preview-caption">Browser tab</span>
            </div>
          </div>
          <p class="form-hint" style="margin-bottom: 0">
            PNG, JPEG, WebP, SVG or ICO, up to {{ formatSize(brand.max_asset_bytes) }}; use a square mark.
            Uploads apply at once. Prefer them to links: a linked image makes every visitor's browser
            contact that host.
          </p>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Accent</h2></div>
        <div class="card-body">
          <div class="form-group">
            <div class="accent-grid" role="radiogroup" aria-label="Brand accent">
              <button
                v-for="a in accents"
                :key="a.code"
                type="button"
                role="radio"
                class="accent-option"
                :class="{ active: form.accent === a.code }"
                :aria-checked="form.accent === a.code"
                :disabled="!editable"
                @click="form.accent = a.code"
              >
                <span class="accent-swatch" :style="{ background: a.swatch }"></span>
                <span>{{ a.label }}</span>
              </button>
            </div>
            <p class="form-hint">
              Colours the sign-in page and the console. Status colours keep their own meaning, and
              light or dark mode stays each person's choice.
            </p>
          </div>
          <div class="form-group" style="margin-bottom: 0">
            <span class="form-label">Who picks the accent</span>
            <div class="policy-list">
              <label
                v-for="p in policies"
                :key="p.value"
                class="policy-option"
                :class="{ active: form.accent_policy === p.value, disabled: !editable }"
              >
                <input v-model="form.accent_policy" type="radio" name="accent-policy" :value="p.value" :disabled="!editable" />
                <span>
                  <span class="policy-label">{{ p.label }}</span>
                  <span class="policy-hint">{{ p.hint }}</span>
                </span>
              </label>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Sign-in page</h2></div>
        <div class="card-body">
          <div class="form-group">
            <label class="form-label" for="brand-notice">Notice</label>
            <textarea
              id="brand-notice"
              v-model="form.signin_notice"
              class="form-textarea"
              rows="3"
              :maxlength="brand.max_notice_runes"
              placeholder="This system is for authorised use only. Activity may be monitored."
              :disabled="!editable"
            ></textarea>
            <p class="form-hint">
              Shown above the sign-in and sign-up forms as plain text, line breaks kept.
              {{ noticeLength }}/{{ brand.max_notice_runes }}
            </p>
          </div>
          <div class="form-group" style="margin-bottom: 0">
            <label class="form-label">Links</label>
            <div v-for="(l, i) in form.links" :key="i" class="flex items-center gap-2" style="margin-bottom: 8px">
              <input v-model="l.label" class="form-input" style="max-width: 160px" placeholder="Privacy" maxlength="32" :disabled="!editable" />
              <input v-model="l.url" class="form-input" placeholder="https://acme.example/privacy" :disabled="!editable" />
              <button class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove link" :disabled="!editable" @click="form.links.splice(i, 1)">
                <span class="mdi mdi-close"></span>
              </button>
            </div>
            <button class="btn btn-sm btn-secondary" :disabled="!editable || form.links.length >= MAX_LINKS" @click="addLink">
              Add link
            </button>
            <p class="form-hint">
              Shown below the sign-in form, up to six. Each opens in a new tab. Only http:// and
              https:// addresses are accepted.
            </p>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.space-y { display: flex; flex-direction: column; gap: 16px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.readonly-note { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 13px; color: var(--text-muted); }
.locked { display: flex; align-items: flex-start; gap: 14px; }
.locked > .mdi { font-size: 24px; color: var(--text-muted); }
.locked p { margin: 0 0 12px; font-size: 13px; color: var(--text-muted); }
.asset-row {
  display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap;
  padding: 12px 0; border-top: 1px solid var(--border-primary);
}
.asset-info { display: flex; flex-direction: column; flex: 1 1 260px; min-width: 0; }
.asset-info .form-label { margin-bottom: 0; }
.asset-info .form-hint { margin-top: 2px; }
.asset-url { margin-top: 8px; }
.asset-actions { display: flex; align-items: center; gap: 8px; }
.asset-size { font-size: 12px; color: var(--text-muted); }
.asset-pick { position: relative; cursor: pointer; }
.asset-pick.disabled { opacity: 0.6; cursor: default; }
.asset-pick:focus-within { box-shadow: var(--shadow-focus); }
.asset-file { position: absolute; width: 1px; height: 1px; opacity: 0; overflow: hidden; }
.logo-previews { display: flex; flex-wrap: wrap; gap: 10px; margin: 8px 0; }
.logo-preview {
  display: flex; align-items: center; gap: 10px; flex: 1 1 200px; min-width: 0;
  padding: 12px 14px; border: 1px solid var(--border-primary); border-radius: 8px;
}
.logo-preview img { width: 28px; height: 28px; object-fit: contain; flex: none; }
.logo-preview-light { background: #ffffff; color: #111827; }
.logo-preview-dark { background: var(--bg-sidebar); color: #ffffff; }
.logo-preview-tab { background: var(--bg-secondary); color: var(--text-primary); }
.logo-preview .tab-icon { width: 16px; height: 16px; }
.logo-preview-name { font-family: var(--font-brand); font-size: 15px; font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tab-title { font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.logo-preview-caption { margin-left: auto; font-size: 11px; opacity: 0.6; white-space: nowrap; }
.accent-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 10px; }
.accent-option {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px; border: 1px solid var(--border-primary); border-radius: 8px;
  background: var(--bg-primary); color: var(--text-primary); cursor: pointer;
  font-size: 13px; text-align: left;
}
.accent-option:not(:disabled):hover { border-color: var(--border-input); }
.accent-option.active { border-color: var(--primary-500); box-shadow: var(--shadow-focus); }
.accent-option:disabled { cursor: default; }
.accent-option:disabled:not(.active) { opacity: 0.5; }
.accent-swatch { width: 18px; height: 18px; border-radius: 50%; flex: none; border: 1px solid rgba(0, 0, 0, 0.12); }
.policy-list { display: grid; gap: 8px; }
.policy-option {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 10px 12px; border: 1px solid var(--border-primary); border-radius: 8px; cursor: pointer;
}
.policy-option.active { border-color: var(--primary-500); background: var(--primary-50); }
.policy-option.disabled { cursor: default; }
.policy-option input { margin-top: 3px; }
.policy-label { display: block; font-size: 13px; font-weight: 600; color: var(--text-primary); }
.policy-hint { display: block; margin-top: 2px; font-size: 12px; color: var(--text-muted); }
</style>
