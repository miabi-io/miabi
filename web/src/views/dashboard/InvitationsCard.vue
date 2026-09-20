<script setup lang="ts">
import type { PendingInvitation } from '@/api/types'

defineProps<{ invitations: PendingInvitation[]; acceptingId: number | null }>()
defineEmits<{ accept: [inv: PendingInvitation] }>()
</script>

<template>
  <div class="card invites-card">
    <div class="card-header">
      <h2><span class="mdi mdi-email-outline"></span> {{ $t('dashboard.invitations.title') }}</h2>
    </div>
    <ul class="invites">
      <li v-for="inv in invitations" :key="inv.id" class="invite">
        <div class="invite-info">
          <span class="invite-name">{{ inv.workspace_name }}</span>
          <span class="invite-sub">
            {{ $t('dashboard.invitations.invitedAs') }} <strong>{{ inv.role }}</strong>
            <template v-if="inv.invited_by_name"> by {{ inv.invited_by_name }}</template>
          </span>
        </div>
        <button class="btn btn-primary btn-sm" :disabled="acceptingId === inv.id" @click="$emit('accept', inv)">
          {{ acceptingId === inv.id ? 'Joining…' : 'Accept' }}
        </button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.invites-card { margin-bottom: 20px; border-color: var(--primary-500); }
[data-theme="dark"] .invites-card { border-color: var(--primary-900); }
.invites-card .card-header h2 { display: flex; align-items: center; gap: 8px; }
.invites { list-style: none; margin: 0; padding: 0; }
.invite { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 14px 20px; }
.invite + .invite { border-top: 1px solid var(--border-secondary); }
.invite-info { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.invite-name { font-size: 14px; font-weight: 600; color: var(--text-primary); }
.invite-sub { font-size: 13px; color: var(--text-muted); }
.invite-sub strong { color: var(--text-secondary); text-transform: capitalize; }
</style>
