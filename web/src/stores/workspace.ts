import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { workspaceApi, type CreateWorkspaceInput } from '@/api/workspaces'
import { authApi } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import type { Workspace, WorkspaceRole } from '@/api/types'

const STORAGE_KEY = 'mb_workspace_id'

export const useWorkspaceStore = defineStore('workspace', () => {
  const workspaces = ref<Workspace[]>([])
  const currentWorkspaceId = ref<number | null>(
    localStorage.getItem(STORAGE_KEY) ? Number(localStorage.getItem(STORAGE_KEY)) : null,
  )
  const loaded = ref(false)

  const currentWorkspace = computed(
    () => workspaces.value.find((w) => w.id === currentWorkspaceId.value) ?? null,
  )

  const currentRole = computed<WorkspaceRole | null>(() => currentWorkspace.value?.role ?? null)
  const contextLabel = computed(
    () => currentWorkspace.value?.display_name || currentWorkspace.value?.name || 'Select workspace',
  )
  const isWorkspaceContext = computed(() => currentWorkspace.value !== null)
  const isWorkspaceAdmin = computed(
    () => currentRole.value === 'owner' || currentRole.value === 'admin',
  )
  const isWorkspaceOwner = computed(() => currentRole.value === 'owner')
  // Roles allowed to mutate resources (deploy, create, edit).
  const canEdit = computed(() => {
    const role = currentRole.value
    return role === 'owner' || role === 'admin' || role === 'developer'
  })

  function setWorkspace(id: number | null) {
    currentWorkspaceId.value = id
    if (id) localStorage.setItem(STORAGE_KEY, String(id))
    else localStorage.removeItem(STORAGE_KEY)
  }

  // valid reports whether an id names a workspace this user can actually open, so a
  // stale localStorage value or a revoked membership never strands the console.
  function valid(id: number | null | undefined): boolean {
    return !!id && workspaces.value.some((w) => w.id === id)
  }

  async function fetchWorkspaces() {
    const res = await workspaceApi.list()
    workspaces.value = res.data.data ?? []
    // Precedence: this session's own choice, then this device's last one, then the
    // user's default from the server, then oldest-first. localStorage is a
    // same-device fast path, not the source of truth — it does not follow the user
    // to a new browser, and the server's default does.
    if (!valid(currentWorkspaceId.value)) {
      const serverDefault = useAuthStore().user?.default_workspace_id
      setWorkspace(
        (valid(serverDefault) ? serverDefault : null) ?? oldestWorkspaceId() ?? null,
      )
    }
    loaded.value = true
  }

  // oldestWorkspaceId is the fallback when nothing else names a workspace. The list
  // arrives newest-first for display, which is the wrong guess to land on: a user's
  // oldest workspace is almost always their primary one.
  function oldestWorkspaceId(): number | null {
    if (workspaces.value.length === 0) return null
    return workspaces.value[workspaces.value.length - 1].id
  }

  // makeDefault pins where this user's future sessions land. Kept separate from
  // setWorkspace on purpose: switching workspaces is navigation, changing the default
  // is a deliberate choice, and conflating them makes the default unstable.
  async function makeDefault(id: number) {
    await authApi.setDefaultWorkspace(id)
    const auth = useAuthStore()
    if (auth.user) auth.setUser({ ...auth.user, default_workspace_id: id })
  }

  const isDefaultWorkspace = computed(
    () => !!currentWorkspaceId.value && useAuthStore().user?.default_workspace_id === currentWorkspaceId.value,
  )

  async function create(input: CreateWorkspaceInput | string) {
    const res = await workspaceApi.create(input)
    await fetchWorkspaces()
    setWorkspace(res.data.data.id)
    // The server claims a user's FIRST workspace as their default, so refresh the
    // cached profile rather than showing a stale "not default" state for it.
    await useAuthStore().fetchUser()
    return res.data.data
  }



  function clear() {
    workspaces.value = []
    setWorkspace(null)
    loaded.value = false
  }

  return {
    workspaces,
    currentWorkspaceId,
    currentWorkspace,
    currentRole,
    contextLabel,
    isWorkspaceContext,
    isWorkspaceAdmin,
    isWorkspaceOwner,
    canEdit,
    loaded,
    setWorkspace,
    fetchWorkspaces,
    makeDefault,
    isDefaultWorkspace,
    create,
    clear,
  }
})
