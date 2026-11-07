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

import { computed, nextTick, onBeforeUnmount, ref, watch, type ComputedRef, type Ref } from 'vue'

type BoolSource = Ref<boolean> | (() => boolean)

// Nested modals share one scroll lock so a nested confirm closing doesn't release the page's scroll.
let openModals = 0
let restoreOverflow = ''

function lockScroll() {
  if (openModals++ === 0) {
    restoreOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
}

function unlockScroll() {
  if (openModals > 0 && --openModals === 0) {
    document.body.style.overflow = restoreOverflow
  }
}

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

// Marks a region whose own keyboard handling owns Tab — a shell completes a path with it.
const TAB_THROUGH = '[data-modal-tab-through]'

// Escape-owning region (e.g. vim terminal); Shift+Tab is never forwarded, so the keyboard can always get out.
const ESCAPE_THROUGH = '[data-modal-escape-through]'

export type ModalKeyAction = 'close' | 'trap-tab' | 'ignore'

/** modalKeyAction is the keyboard policy, separated from the DOM so it can be tested on its own. */
export function modalKeyAction(opts: {
  key: string
  shiftKey: boolean
  escapable: boolean
  inEscapeThrough: boolean
  inTabThrough: boolean
}): ModalKeyAction {
  if (opts.key === 'Escape') {
    if (!opts.escapable || opts.inEscapeThrough) return 'ignore'
    return 'close'
  }
  if (opts.key !== 'Tab') return 'ignore'
  if (!opts.shiftKey && opts.inTabThrough) return 'ignore'
  return 'trap-tab'
}

function focusableIn(container: HTMLElement | null): HTMLElement[] {
  if (!container) return []
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
}

interface UseModalOptions {
  /** A modal with unsaved work should confirm here; nothing else dismisses a modal, by design. */
  onRequestClose?: () => void
  /** The dialog element, used to trap focus and to place it on open. */
  container?: Ref<HTMLElement | null>
  /** Set false to let Escape through (a modal mid-save, for instance). */
  escapable?: BoolSource
  /** Set false when the dialog places initial focus itself. */
  autoFocus?: boolean
}

/** useModal locks page scroll, traps focus, routes Escape to close, and restores focus to the opener. */
export function useModal(open: BoolSource, options: UseModalOptions = {}) {
  const isOpen = computed(() => (typeof open === 'function' ? open() : open.value))
  const escapable = computed(() =>
    options.escapable === undefined
      ? true
      : typeof options.escapable === 'function'
        ? options.escapable()
        : options.escapable.value,
  )

  // Whatever had focus before the modal opened, so it can be given back.
  const previouslyFocused = ref<HTMLElement | null>(null)
  let listening = false

  function onKeydown(event: KeyboardEvent) {
    const active = document.activeElement as HTMLElement | null

    const action = modalKeyAction({
      key: event.key,
      shiftKey: event.shiftKey,
      escapable: escapable.value,
      inEscapeThrough: !!active?.closest(ESCAPE_THROUGH),
      inTabThrough: !!active?.closest(TAB_THROUGH),
    })
    if (action === 'ignore') return

    if (action === 'close') {
      event.stopPropagation()
      event.preventDefault()
      options.onRequestClose?.()
      return
    }

    // A focus ring wandering behind an overlay is a dead end for a screen reader.
    const focusable = focusableIn(options.container?.value ?? null)
    if (focusable.length === 0) return

    const first = focusable[0]
    const last = focusable[focusable.length - 1]

    if (event.shiftKey && (active === first || !options.container?.value?.contains(active))) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && active === last) {
      event.preventDefault()
      first.focus()
    }
  }

  function activate() {
    if (listening) return
    listening = true
    previouslyFocused.value = document.activeElement as HTMLElement | null
    lockScroll()
    document.addEventListener('keydown', onKeydown, true)

    // Land on the first field rather than on the page behind the overlay.
    if (options.autoFocus === false) return
    void nextTick(() => {
      const container = options.container?.value
      if (!container) return
      const target = focusableIn(container).find((el) => !el.hasAttribute('data-modal-skip-focus'))
      ;(target ?? container).focus()
    })
  }

  function deactivate() {
    if (!listening) return
    listening = false
    document.removeEventListener('keydown', onKeydown, true)
    unlockScroll()
    previouslyFocused.value?.focus?.()
    previouslyFocused.value = null
  }

  watch(isOpen, (value) => (value ? activate() : deactivate()), { immediate: true })
  onBeforeUnmount(deactivate)

  return { isOpen }
}

/** useDirtyGuard tells "close this" from "throw away my work"; call reset() whenever the form is (re)populated. */
export function useDirtyGuard(snapshot: () => unknown): {
  dirty: ComputedRef<boolean>
  reset: () => void
} {
  const baseline = ref('')

  const serialize = () => {
    try {
      return JSON.stringify(snapshot() ?? null)
    } catch {
      // A value that will not serialize is treated as untouched rather than warning on every close.
      return baseline.value
    }
  }

  const reset = () => {
    baseline.value = serialize()
  }
  const dirty = computed(() => serialize() !== baseline.value)

  return { dirty, reset }
}
