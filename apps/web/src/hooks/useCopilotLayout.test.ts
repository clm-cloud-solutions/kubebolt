import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { COPILOT_LIMITS, useCopilotLayout } from './useCopilotLayout'

// The floating Copilot panel is anchored `right: 20px; bottom: 20px` and grows
// UP and LEFT, so its top edge sits at `viewportHeight - 20 - height`. The size
// limits were plain constants (maxHeight 950), which is taller than the usable
// area on any viewport under 1022px — at full height the panel slid underneath
// the 52px Topbar.
//
// These lock the three moments the bound has to hold: while dragging, when a
// size is restored from localStorage, and when the WINDOW itself shrinks (which
// moves the top edge with no user interaction at all).

const STORAGE_KEY = 'kubebolt-copilot-layout'
const TOPBAR = 52
const MARGIN = 20

/** Usable height for the panel at a given viewport. */
const usableHeight = (vh: number) => vh - TOPBAR - MARGIN

function setViewport(width: number, height: number) {
  Object.defineProperty(window, 'innerWidth', { value: width, writable: true, configurable: true })
  Object.defineProperty(window, 'innerHeight', { value: height, writable: true, configurable: true })
}

const originalW = window.innerWidth
const originalH = window.innerHeight

beforeEach(() => {
  localStorage.clear()
  setViewport(1440, 900)
})

afterEach(() => {
  setViewport(originalW, originalH)
  vi.restoreAllMocks()
})

describe('useCopilotLayout — the panel never covers the Topbar', () => {
  it('caps a drag at the usable height, not at the static maxHeight', () => {
    // 900px viewport → 828 usable, well under the 950 constant.
    const { result } = renderHook(() => useCopilotLayout())

    act(() => result.current.setFloatingSize(500, 5000))

    expect(result.current.layout.floatingHeight).toBe(usableHeight(900))
    // And the static limit is genuinely the looser of the two here — proving the
    // viewport is what bit, not the constant.
    expect(usableHeight(900)).toBeLessThan(COPILOT_LIMITS.floating.maxHeight)
  })

  it('still honours the static maxHeight on a tall enough viewport', () => {
    setViewport(1920, 1400) // usable 1328 > 950
    const { result } = renderHook(() => useCopilotLayout())

    act(() => result.current.setFloatingSize(500, 5000))

    expect(result.current.layout.floatingHeight).toBe(COPILOT_LIMITS.floating.maxHeight)
  })

  it('bounds a size restored from localStorage', () => {
    // Saved on a big monitor, reopened on a laptop — must not reopen overlapping.
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ mode: 'floating', dockedWidth: 460, floatingWidth: 1100, floatingHeight: 950 }),
    )
    setViewport(1280, 800)

    const { result } = renderHook(() => useCopilotLayout())

    expect(result.current.layout.floatingHeight).toBe(usableHeight(800))
    expect(result.current.layout.floatingWidth).toBe(1100) // 1280-40 = 1240, so the static cap wins
  })

  it('re-clamps when the WINDOW shrinks, with no user interaction', () => {
    const { result } = renderHook(() => useCopilotLayout())
    act(() => result.current.setFloatingSize(500, 800))
    expect(result.current.layout.floatingHeight).toBe(800)

    act(() => {
      setViewport(1440, 600)
      window.dispatchEvent(new Event('resize'))
    })

    expect(result.current.layout.floatingHeight).toBe(usableHeight(600))
  })

  it('leaves the size alone when a resize does not bite', () => {
    // Guards against churning state (and localStorage) on every resize tick.
    const { result } = renderHook(() => useCopilotLayout())
    act(() => result.current.setFloatingSize(500, 600))

    const before = result.current.layout
    act(() => {
      setViewport(1600, 1000) // grew — nothing to clamp
      window.dispatchEvent(new Event('resize'))
    })

    expect(result.current.layout).toBe(before) // same object identity: no re-render churn
  })

  it('never returns a height below the minimum, even on a tiny viewport', () => {
    // The viewport-derived cap can fall under minHeight; the panel keeps its
    // minimum rather than collapsing. (ViewportGate blocks screens this small.)
    setViewport(800, 300)
    const { result } = renderHook(() => useCopilotLayout())

    act(() => result.current.setFloatingSize(500, 5000))

    expect(result.current.layout.floatingHeight).toBe(COPILOT_LIMITS.floating.minHeight)
  })
})
