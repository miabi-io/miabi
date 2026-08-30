// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, nextTick, onBeforeUnmount, ref, watch, type ComputedRef, type Ref } from 'vue'

type BoolSource = Ref<boolean> | (() => boolean)

// Nested modals share one body scroll lock: a confirm dialog raised from inside
// a form modal must not release the page's scroll when it closes.
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

// Marks a region whose own keyboard handling owns Tab. Escape still closes the dialog, so the
// keyboard is never trapped inside one.
const TAB_THROUGH = '[data-modal-tab-through]'

function focusableIn(container: HTMLElement | null): HTMLElement[] {
  if (!container) return []
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
}

interface UseModalOptions {
  /**
   * Called when the user asks to close with the keyboard. A modal holding
   * unsaved work should confirm here rather than closing outright — nothing
   * else dismisses a modal, by design: clicking the backdrop does not close it,
   * so an errant click next to a form can no longer discard what was typed.
   */
  onRequestClose?: () => void
  /** The dialog element, used to trap focus and to place it on open. */
  container?: Ref<HTMLElement | null>
  /** Set false to let Escape through (a modal mid-save, for instance). */
  escapable?: BoolSource
  /** Set false when the dialog places initial focus itself. */
  autoFocus?: boolean
}

/**
 * useModal gives a modal the behaviour every modal should have: the page behind
 * it stops scrolling, focus moves into it and stays there while it is open,
 * Escape asks it to close, and the element that opened it gets focus back.
 */
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
    if (event.key === 'Escape') {
      if (!escapable.value) return
      event.stopPropagation()
      event.preventDefault()
      options.onRequestClose?.()
      return
    }

    if (event.key !== 'Tab') return

    const active = document.activeElement as HTMLElement | null

    // Some content consumes Tab itself — a shell completes a path with it. The trap runs on capture,
    // so without this it wraps focus onto the dialog's own buttons before the terminal ever sees the key.
    if (active?.closest(TAB_THROUGH)) return

    // Keep Tab inside the dialog: a focus ring wandering onto the page behind an
    // overlay is disorienting with a mouse and a dead end with a screen reader.
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

/**
 * useDirtyGuard tracks whether a form has been edited since it was opened, so a
 * modal can tell the difference between "close this" and "throw away my work".
 * Snapshot with `reset()` whenever the form is (re)populated.
 */
export function useDirtyGuard(snapshot: () => unknown): {
  dirty: ComputedRef<boolean>
  reset: () => void
} {
  const baseline = ref('')

  const serialize = () => {
    try {
      return JSON.stringify(snapshot() ?? null)
    } catch {
      // A value that will not serialize cannot be compared; treat the form as
      // untouched rather than warning the user on every close.
      return baseline.value
    }
  }

  const reset = () => {
    baseline.value = serialize()
  }
  const dirty = computed(() => serialize() !== baseline.value)

  return { dirty, reset }
}
