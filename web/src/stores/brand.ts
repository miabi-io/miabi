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

  // force re-reads after an admin change: uploaded images reach the console only as
  // the resolved URLs status hands out.
  function load(force = false): Promise<void> {
    if (!loading || force) {
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

  // index.html ships Miabi's icons. They are repointed rather than replaced, so
  // clearing the brand favicon restores them; a stale type would make browsers skip one.
  const originalIcons = new Map<HTMLLinkElement, { href: string; type: string | null }>()

  watch(() => brand.value.favicon_url, (url) => {
    document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"], link[rel="apple-touch-icon"]').forEach((link) => {
      if (!originalIcons.has(link)) {
        originalIcons.set(link, { href: link.getAttribute('href') ?? '', type: link.getAttribute('type') })
      }
      const original = originalIcons.get(link)!
      link.setAttribute('href', url || original.href)
      if (url || original.type === null) link.removeAttribute('type')
      else link.setAttribute('type', original.type)
    })
  })

  return { brand, name, sidebarLogo, set, load, setTitle }
})
