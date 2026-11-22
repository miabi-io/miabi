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
import { useI18n } from 'vue-i18n'
import type { JobEnvVar } from '@/api/types'

const props = defineProps<{ disabled?: boolean }>()
const env = defineModel<JobEnvVar[]>({ required: true })
const inherit = defineModel<boolean>('inherit', { required: true })
const { t } = useI18n()

const MASK = '••••••••'
const secretRef = '${{ secrets.NAME }}'

function add() {
  env.value = [...env.value, { key: '', value: '', is_secret: false }]
}
function remove(i: number) {
  env.value = env.value.filter((_, idx) => idx !== i)
}
function update(i: number, patch: Partial<JobEnvVar>) {
  env.value = env.value.map((v, idx) => (idx === i ? { ...v, ...patch } : v))
}
</script>

<template>
  <div class="form-group">
    <label class="form-label">{{ t('jobs.env.label') }}<span class="text-muted">{{ t('jobs.optional') }}</span></label>
    <div v-for="(v, i) in env" :key="i" class="job-env-row">
      <input
        :value="v.key" class="form-input job-env-key" placeholder="KEY" :aria-label="t('jobs.env.key')" :disabled="props.disabled"
        @input="update(i, { key: ($event.target as HTMLInputElement).value })"
      />
      <input
        :value="v.value" class="form-input job-env-value" :type="v.is_secret ? 'password' : 'text'" :aria-label="t('jobs.env.value')"
        :placeholder="v.is_secret && v.value === MASK ? t('jobs.env.keepSecret') : t('jobs.env.valuePlaceholder')" :disabled="props.disabled"
        @focus="v.is_secret && v.value === MASK && ($event.target as HTMLInputElement).select()"
        @input="update(i, { value: ($event.target as HTMLInputElement).value })"
      />
      <label class="job-env-secret" :title="t('jobs.env.secretHint')">
        <input type="checkbox" :checked="!!v.is_secret" :disabled="props.disabled" @change="update(i, { is_secret: ($event.target as HTMLInputElement).checked })" />
        {{ t('jobs.env.secret') }}
      </label>
      <button
        v-if="!props.disabled" type="button" class="btn-icon btn-icon-danger" :title="t('action.delete')" :aria-label="t('action.delete')"
        @click="remove(i)"
      ><span class="mdi mdi-delete-outline"></span></button>
    </div>
    <button v-if="!props.disabled" type="button" class="btn btn-ghost btn-sm" @click="add"><span class="mdi mdi-plus"></span>{{ t('jobs.env.add') }}</button>
    <p class="form-hint">
      <i18n-t keypath="jobs.env.hint" tag="span"><template #ref><code>{{ secretRef }}</code></template></i18n-t>
    </p>
    <label class="checkbox-row" style="margin-top: 8px">
      <input v-model="inherit" type="checkbox" :disabled="props.disabled" />{{ t('jobs.env.inherit') }}
    </label>
    <p class="form-hint">{{ t('jobs.env.inheritHint') }}</p>
  </div>
</template>

<style scoped>
.job-env-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; }
.job-env-key { flex: 0 1 180px; font-family: monospace; }
.job-env-value { flex: 1 1 200px; font-family: monospace; }
.job-env-secret { display: inline-flex; align-items: center; gap: 4px; font-size: 13px; white-space: nowrap; }
</style>
