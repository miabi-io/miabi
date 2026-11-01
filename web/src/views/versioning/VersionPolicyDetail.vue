<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import VersionPolicyPanel from '@/components/VersionPolicyPanel.vue'
import type { VersionPolicy } from '@/api/types'

const route = useRoute()
const router = useRouter()
const uid = computed(() => String(route.params.uid))
const policy = ref<VersionPolicy | null>(null)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link :to="{ name: 'pipelines', query: { tab: 'versioning' } }" class="back-link"><span class="mdi mdi-arrow-left"></span>{{ $t('versioning.title') }}</router-link>
        <h1>{{ policy?.repository || $t('versioning.policy') }}</h1>
        <p v-if="policy" class="subtitle">
          {{ policy.owner_kind === 'application' ? $t('versioning.ownerApp') : $t('versioning.ownerPipeline') }}
          <span v-if="policy.managed_by"> · {{ $t('versioning.managedHint', { pipeline: policy.managed_by }) }}</span>
        </p>
      </div>
    </div>
    <VersionPolicyPanel :uid="uid" @loaded="policy = $event" @deleted="router.push({ name: 'pipelines', query: { tab: 'versioning' } })" />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.back-link { font-size: 13px; color: var(--text-muted); text-decoration: none; display: inline-flex; align-items: center; gap: 4px; }
.back-link:hover { color: var(--primary-500); }
</style>
