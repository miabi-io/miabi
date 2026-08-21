<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import type { AdminUser } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useAuthStore } from '@/stores/auth'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import AppModal from '@/components/AppModal.vue'

const notify = useNotificationStore()
const auth = useAuthStore()
const router = useRouter()

const users = ref<AdminUser[]>([])
const loading = ref(false)
const search = ref('')

let searchTimer: ReturnType<typeof setTimeout> | undefined

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listUsers(search.value.trim(), page, pageable.value.size)
    users.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}

function isSelf(u: AdminUser): boolean {
  return auth.user?.id === u.id
}

function fmtDate(s?: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleString()
}

// --- Create modal ---
const showCreate = ref(false)
const creating = ref(false)
const createForm = ref<{ name: string; username: string; email: string; password: string; role: 'admin' | 'user'; notify: boolean }>({
  name: '',
  username: '',
  email: '',
  password: '',
  role: 'user',
  notify: true,
})

function openCreate() {
  createForm.value = { name: '', username: '', email: '', password: '', role: 'user', notify: true }
  showCreate.value = true
}

async function submitCreate() {
  if (!createForm.value.name.trim() || !createForm.value.email.trim() || createForm.value.password.length < 8) return
  creating.value = true
  try {
    await adminApi.createUser({
      name: createForm.value.name.trim(),
      username: createForm.value.username.trim() || undefined,
      email: createForm.value.email.trim(),
      password: createForm.value.password,
      role: createForm.value.role,
      notify: createForm.value.notify,
    })
    notify.success('User created')
    showCreate.value = false
    await goToPage(0)
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

// Per-user actions (edit role/status, verify email, revoke sessions, disable,
// delete) all live on the detail page now — rows link there. The list is
// read-only apart from creating a new user.

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Users</h1>
      <button class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New user
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
            placeholder="Search users by name or email…"
            aria-label="Search users by name or email"
            @input="onSearchInput"
          />
        </div>
        <span class="text-muted">{{ pageable.total_elements }} user{{ pageable.total_elements === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && users.length === 0" class="card-body"><span class="spinner"></span></div>

      <div v-else-if="users.length === 0" class="empty-state">
        <span class="mdi mdi-account-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No users found</h3>
        <p>{{ search.trim() ? 'No users match your search.' : 'Create the first user to get started.' }}</p>
        <button v-if="!search.trim()" class="btn btn-primary mt-4" @click="openCreate">Create a user</button>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Role</th>
              <th>Status</th>
              <th>Last login</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id" class="row-clickable" @click="router.push(`/admin/users/${u.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm">{{ u.name.charAt(0).toUpperCase() }}</span>
                  <span class="cell-text">
                    <span class="cell-title">{{ u.name }}<span v-if="u.username" class="text-muted"> @{{ u.username }}</span><span v-if="isSelf(u)" class="text-muted"> (you)</span></span>
                    <span class="cell-sub">{{ u.email }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span class="badge" :class="u.role === 'admin' ? 'badge-success' : 'badge-warning'">{{ u.role }}</span>
              </td>
              <td>
                <span v-if="u.scheduled_deletion_at" class="badge badge-danger">Pending deletion</span>
                <span v-else-if="u.active" class="badge badge-dot badge-success">Active</span>
                <span v-else class="badge badge-dot badge-danger">Inactive</span>
              </td>
              <td class="cell-sub">{{ fmtDate(u.last_login_at) }}</td>
              <td class="cell-sub">{{ fmtDate(u.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <!-- Create modal -->
    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>New user</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="submitCreate">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="createForm.name" class="form-input" placeholder="Jane Doe" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">Username <span class="text-muted">(optional)</span></label>
              <input v-model="createForm.username" class="form-input" placeholder="auto-derived from email" autocomplete="off" spellcheck="false" />
            </div>
            <div class="form-group">
              <label class="form-label">Email</label>
              <input v-model="createForm.email" class="form-input" type="email" placeholder="jane@example.com" required />
            </div>
            <div class="form-group">
              <label class="form-label">Password</label>
              <input
                v-model="createForm.password"
                class="form-input"
                type="password"
                placeholder="At least 8 characters"
                minlength="8"
                required
              />
              <span class="form-hint">Minimum 8 characters.</span>
            </div>
            <div class="form-group">
              <label class="form-label">Role</label>
              <select v-model="createForm.role" class="form-select">
                <option value="user">User</option>
                <option value="admin">Admin</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="checkbox-row">
                <input type="checkbox" v-model="createForm.notify" />
                <span>Send a welcome email with a sign-in link</span>
              </label>
              <p class="form-hint">Requires a configured system SMTP server (MIABI_SMTP_*).</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
            <button
              type="submit"
              class="btn btn-primary"
              :disabled="creating || !createForm.name.trim() || !createForm.email.trim() || createForm.password.length < 8"
            >
              {{ creating ? 'Creating…' : 'Create user' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

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
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
.pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
}
.text-right {
  text-align: right;
}
.checkbox-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  cursor: pointer;
}
</style>
