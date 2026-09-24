<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import { useI18n } from 'vue-i18n'
import type { AdminWorkspace } from '@/api/types'
import { workspaceTrait } from '@/data/workspaceTrait'

const notify = useNotificationStore()
const router = useRouter()
const { t } = useI18n()
const traitOf = (w: AdminWorkspace) => workspaceTrait(w, t)

const workspaces = ref<AdminWorkspace[]>([])
const loading = ref(false)
const search = ref('')

let searchTimer: ReturnType<typeof setTimeout> | undefined

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listWorkspaces(page, pageable.value.size, search.value.trim())
    workspaces.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

function reload() {
  goToPage(pageable.value.current_page)
}

function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}

function fmtDate(s: string): string {
  return new Date(s).toLocaleDateString()
}

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Workspaces</h1>
        <p class="text-muted">All workspaces on the platform.</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="reload">
        <span class="mdi mdi-refresh"></span> Refresh
      </button>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input
            v-model="search"
            class="form-input"
            type="search"
            placeholder="Search workspaces by name or slug…"
            aria-label="Search workspaces"
            @input="onSearchInput"
          />
        </div>
        <span class="text-muted">{{ pageable.total_elements }} workspace{{ pageable.total_elements === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && workspaces.length === 0" class="card-body"><span class="spinner"></span></div>

      <div v-else-if="workspaces.length === 0" class="empty-state">
        <span class="mdi mdi-briefcase-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ search.trim() ? 'No workspaces match your search.' : 'No workspaces.' }}</h3>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Workspace</th>
              <th>Owner</th>
              <th>Apps</th>
              <th>Databases</th>
              <th>Stacks</th>
              <th>Members</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="w in workspaces" :key="w.id" class="row-clickable" @click="router.push(`/admin/workspaces/${w.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm" :class="traitOf(w) ? `avatar-${traitOf(w)!.key}` : ''"
                    :title="traitOf(w)?.title">
                    <span v-if="traitOf(w)" class="mdi" :class="traitOf(w)!.icon" role="img"
                      :aria-label="traitOf(w)!.label"></span>
                    <template v-else>{{ (w.display_name || w.name).charAt(0).toUpperCase() }}</template>
                  </span>
                  <span class="cell-text">
                    <span class="cell-title">
                      {{ w.display_name || w.name }}
                      <span v-if="traitOf(w)" class="badge" :class="traitOf(w)!.badgeClass"
                        :title="traitOf(w)!.title">{{ traitOf(w)!.label }}</span>
                    </span>
                    <span class="cell-sub">{{ w.name }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span class="cell-text">
                  <span class="cell-title">{{ w.owner_name }}</span>
                  <span class="cell-sub">{{ w.owner_email }}</span>
                </span>
              </td>
              <td>{{ w.apps_count }}</td>
              <td>{{ w.databases_count }}</td>
              <td>{{ w.stacks_count }}</td>
              <td>{{ w.members_count }}</td>
              <td class="cell-sub">{{ fmtDate(w.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.search {
  position: relative;
  flex: 1;
  max-width: 360px;
}
.search .mdi {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  pointer-events: none;
}
.search .form-input {
  padding-left: 32px;
}
</style>
