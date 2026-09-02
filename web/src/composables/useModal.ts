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

// Marks a region whose own keyboard handling owns Tab — a shell completes a path with it.
const TAB_THROUGH = '[data-modal-tab-through]'

// Marks a region that owns Escape too, for content where Escape is a working key rather than a
// dismissal: a terminal running vim, most obviously. Shift+Tab is deliberately NOT forwarded to
// such a region (see onKeydown), so it always leads back out to the dialog's own controls — which
// is what keeps the keyboard from being trapped once Escape no longer closes the dialog.
const ESCAPE_THROUGH = '[data-modal-escape-through]'

/**
 * ModalKeyAction is what a modal should do with a key press:
 * - `close` — dismiss the dialog
 * - `trap-tab` — run the focus trap
 * - `ignore` — leave the key to the focused content
 */
export type ModalKeyAction = 'close' | 'trap-tab' | 'ignore'

/**
 * modalKeyAction is the keyboard policy, separated from the DOM so it can be read
 * and tested on its own. The rules it encodes, in order:
 *
 * 1. A dialog mid-action (`escapable: false`) ignores Escape entirely.
 * 2. Content that owns Escape keeps it — a terminal running vim needs Escape to
 *    leave insert mode, and dismissing the dialog instead loses the session.
 * 3. Content that owns Tab keeps it, so a shell can complete a path.
 * 4. Shift+Tab always runs the trap, even inside that content. It is the way back
 *    out to the dialog's controls, and it is what stops rule 2 from trapping a
 *    keyboard user in a dialog they can no longer dismiss.
 */
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
