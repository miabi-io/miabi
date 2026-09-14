import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { authApi } from '@/api/auth'
import type { Brand } from '@/api/types'

// The operator's identity for the console chrome and the browser tab. Read from the
// public auth status, which carries a brand only under a white_label licence.
export const useBrandStore = defineStore('brand', () => {
  const brand = ref<Brand>({})
  const page = ref<string | undefined>()
  let loading: Promise<void> | null = null

  const name = computed(() => brand.value.name?.trim() ?? '')
  // The sidebar is dark in both themes, so it prefers the dark-background logo.
  const sidebarLogo = computed(() => brand.value.logo_dark_url?.trim() || brand.value.logo_url?.trim() || '')

  function set(b?: Brand) {
    brand.value = b ?? {}
  }

  function load(): Promise<void> {
    if (!loading) {
      loading = authApi
        .status()
        .then((res) => set(res.data.data?.brand))
        .catch(() => {
          loading = null
        })
    }
    return loading
  }

  // setTitle names the page; the suffix follows the brand, including when the brand
  // arrives after the first navigation.
  function setTitle(base?: string) {
    page.value = base
  }

  watch([page, name], ([p, n]) => {
    const suffix = n || 'Miabi'
    document.title = p ? `${p} — ${suffix}` : suffix
  }, { flush: 'sync' })

  return { brand, name, sidebarLogo, set, load, setTitle }
})
