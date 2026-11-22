<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { authApi } from '@/api/auth'
import { apiErrorMessage } from '@/api/client'
import type { Passkey } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useNotificationStore } from '@/stores/notification'
import { fmtDate } from '@/utils/datetime'
import { createPasskey, PasskeyCancelled, passkeysSupported } from '@/utils/webauthn'
import AppModal from '@/components/AppModal.vue'

const MAX_PASSKEYS = 10

const { t } = useI18n()
const auth = useAuthStore()
const notify = useNotificationStore()

const enabled = ref<boolean | null>(null)
const supported = passkeysSupported()
const passkeys = ref<Passkey[] | null>(null)

// Only local and directory accounts have a password to confirm a passkey change with.
const hasPassword = computed(() => ['', 'local', 'ldap'].includes(auth.user?.auth_source ?? ''))
const needsCode = computed(() => !!auth.user?.two_factor_enabled)

async function load() {
  try {
    passkeys.value = (await authApi.listPasskeys()).data.data ?? []
  } catch (e) {
    notify.apiError(e, t('security.passkeys.loadFailed'))
  }
}

onMounted(async () => {
  try {
    enabled.value = !!(await authApi.status()).data.data?.passkeys_enabled
  } catch {
    enabled.value = false
  }
  if (enabled.value) await load()
})

// A starting label the user can change; the browser doesn't say which authenticator it will use.
function guessName(): string {
  const ua = navigator.userAgent
  if (/iPhone|iPad/.test(ua)) return t('security.passkeys.deviceIos')
  if (/Android/.test(ua)) return 'Android'
  if (/Mac/.test(ua)) return 'Mac'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Linux/.test(ua)) return 'Linux'
  return t('security.passkeys.defaultName')
}

type Modal = null | 'add' | 'rename' | 'remove'
const modal = ref<Modal>(null)
const target = ref<Passkey | null>(null)
const busy = ref(false)
const error = ref('')
const password = ref('')
const code = ref('')
const name = ref('')

function open(m: Modal, p: Passkey | null = null) {
  error.value = password.value = code.value = ''
  target.value = p
  name.value = m === 'add' ? guessName() : (p?.name ?? '')
  modal.value = m
}
function close() {
  modal.value = null
  target.value = null
  password.value = code.value = ''
}

async function run(fn: () => Promise<void>) {
  error.value = ''
  busy.value = true
  try {
    await fn()
  } catch (e) {
    error.value = e instanceof PasskeyCancelled ? t('security.passkeys.cancelled') : apiErrorMessage(e)
  } finally {
    busy.value = false
  }
}

const add = () =>
  run(async () => {
    const begin = (await authApi.passkeyRegisterBegin(password.value, code.value.trim())).data.data
    const credential = await createPasskey(begin.options.publicKey)
    const p = (await authApi.passkeyRegister(begin.token, name.value.trim(), credential)).data.data
    close()
    await load()
    notify.success(t('security.passkeys.added', { name: p.name }))
  })

const rename = () =>
  run(async () => {
    if (!target.value) return
    await authApi.renamePasskey(target.value.id, name.value.trim())
    close()
    await load()
  })

const remove = () =>
  run(async () => {
    if (!target.value) return
    await authApi.removePasskey(target.value.id, password.value)
    close()
    await load()
    notify.success(t('security.passkeys.removed'))
  })
</script>

<template>
  <div v-if="enabled || (enabled === false && auth.isAdmin)" class="card">
    <div class="card-header">
      <h2>{{ $t('security.passkeys.title') }}</h2>
      <span v-if="passkeys?.length" class="badge badge-success badge-dot">{{ passkeys.length }}</span>
    </div>
    <div class="card-body">
      <p class="sec-desc">{{ $t('security.passkeys.hint') }}</p>

      <div v-if="!enabled" class="app-banner app-banner--info">
        <span class="mdi mdi-information-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-text">{{ $t('security.passkeys.notConfigured') }}</p>
        </div>
      </div>

      <template v-else>
        <div v-if="!supported" class="app-banner app-banner--warning pk-banner">
          <span class="mdi mdi-alert-outline app-banner-icon"></span>
          <div class="app-banner-content">
            <p class="app-banner-text">{{ $t('security.passkeys.unsupported') }}</p>
          </div>
        </div>
        <div v-else-if="!hasPassword" class="app-banner app-banner--info pk-banner">
          <span class="mdi mdi-information-outline app-banner-icon"></span>
          <div class="app-banner-content">
            <p class="app-banner-text">{{ $t('security.passkeys.ssoAccount') }}</p>
          </div>
        </div>

        <ul v-if="passkeys?.length" class="pk-list">
          <li v-for="p in passkeys" :key="p.id" class="pk-item">
            <div class="pk-info">
              <p class="pk-name">
                <span class="mdi mdi-fingerprint"></span>
                <span>{{ p.name }}</span>
                <span v-if="p.synced" class="badge badge-neutral" :title="$t('security.passkeys.syncedHint')">{{ $t('security.passkeys.synced') }}</span>
              </p>
              <p class="pk-meta">
                {{ $t('security.passkeys.addedOn', { date: fmtDate(p.created_at) }) }} ·
                {{ p.last_used_at ? $t('security.passkeys.lastUsed', { date: fmtDate(p.last_used_at) }) : $t('security.passkeys.neverUsed') }}
              </p>
            </div>
            <div class="pk-actions">
              <button class="btn btn-secondary btn-sm" @click="open('rename', p)">{{ $t('security.passkeys.rename') }}</button>
              <button v-if="hasPassword" class="btn btn-danger btn-sm" @click="open('remove', p)">{{ $t('security.passkeys.remove') }}</button>
            </div>
          </li>
        </ul>
        <p v-else-if="passkeys" class="pk-empty">{{ $t('security.passkeys.empty') }}</p>
        <span v-else class="spinner"></span>

        <button
          v-if="supported && hasPassword && passkeys"
          class="btn btn-primary"
          :disabled="passkeys.length >= MAX_PASSKEYS"
          @click="open('add')"
        >
          <span class="mdi mdi-plus"></span>{{ $t('security.passkeys.add') }}</button>
      </template>
    </div>

    <Teleport to="body">
      <AppModal v-if="modal" max-width="460px" @close="close">
        <div class="modal-header">
          <h3>{{ $t(`security.passkeys.${modal}Title`) }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="close"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="modal === 'add' ? add() : modal === 'rename' ? rename() : remove()">
          <div class="modal-body">
            <div v-if="error" class="app-banner app-banner--danger pk-banner" role="alert">
              <span class="mdi mdi-alert-circle-outline app-banner-icon"></span>
              <div class="app-banner-content"><p class="app-banner-text">{{ error }}</p></div>
            </div>
            <p v-if="modal === 'add'" class="sec-desc">{{ $t(needsCode ? 'security.passkeys.addIntroTwoFactor' : 'security.passkeys.addIntro') }}</p>
            <p v-if="modal === 'remove'" class="sec-desc">{{ $t('security.passkeys.removeIntro', { name: target?.name }) }}</p>

            <div v-if="modal !== 'rename'" class="form-group">
              <label class="form-label" for="pk-password">{{ $t('security.password') }}</label>
              <input id="pk-password" v-model="password" type="password" class="form-input" autocomplete="current-password" required autofocus />
            </div>
            <div v-if="modal === 'add' && needsCode" class="form-group">
              <label class="form-label" for="pk-code">{{ $t('login.authenticationCode') }}</label>
              <input id="pk-code" v-model="code" class="form-input" inputmode="numeric" placeholder="123456" autocomplete="one-time-code" required />
              <p class="form-hint">{{ $t('login.totpHint') }}</p>
            </div>
            <div v-if="modal !== 'remove'" class="form-group" style="margin-bottom: 0">
              <label class="form-label" for="pk-name">{{ $t('security.passkeys.name') }}</label>
              <input id="pk-name" v-model="name" class="form-input" maxlength="64" :placeholder="$t('security.passkeys.namePlaceholder')" :required="modal === 'rename'" :autofocus="modal === 'rename'" />
              <p class="form-hint">{{ $t('security.passkeys.nameHint') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="close">{{ $t('action.cancel') }}</button>
            <button
              type="submit"
              class="btn"
              :class="modal === 'remove' ? 'btn-danger' : 'btn-primary'"
              :disabled="busy || (modal !== 'rename' && !password) || (modal === 'rename' && !name.trim())"
            >
              {{ busy ? $t(modal === 'add' ? 'login.waitingForPasskey' : 'security.passkeys.working') : $t(`security.passkeys.${modal}Submit`) }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sec-desc {
  color: var(--text-secondary);
  font-size: 14px;
  line-height: 1.5;
  margin-bottom: 18px;
  max-width: 640px;
}
.pk-banner {
  margin-bottom: 16px;
  max-width: 640px;
}
.pk-list {
  list-style: none;
  margin: 0 0 16px;
  padding: 0;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  max-width: 720px;
}
.pk-item {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
}
.pk-item + .pk-item {
  border-top: 1px solid var(--border-primary);
}
.pk-info {
  min-width: 0;
}
.pk-name {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}
.pk-name .mdi {
  font-size: 16px;
  color: var(--text-muted);
}
.pk-meta {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--text-muted);
}
.pk-actions {
  display: flex;
  gap: 8px;
}
.pk-empty {
  font-size: 14px;
  color: var(--text-muted);
  margin-bottom: 16px;
}
</style>
