import { useState, useEffect, useCallback } from 'react'

export type CopilotMode = 'docked' | 'floating'

export interface CopilotLayout {
  mode: CopilotMode
  dockedWidth: number
  floatingWidth: number
  floatingHeight: number
}

export const COPILOT_LIMITS = {
  docked: { minWidth: 360, maxWidth: 800 },
  floating: {
    minWidth: 380,
    maxWidth: 1100,
    minHeight: 480,
    maxHeight: 950,
  },
}

// Floating-panel geometry, mirrored from CopilotPanel's containerStyle:
// the panel is anchored `right: 20px; bottom: 20px` and grows UP and LEFT.
// Its top edge therefore sits at `viewportHeight - MARGIN - height`, which is
// why height has to be bounded by the viewport and not only by a constant —
// COPILOT_LIMITS.floating.maxHeight (950) is taller than the usable area on
// any viewport under 1022px, and the panel slid under the Topbar.
const TOPBAR_HEIGHT = 52 // matches `top: 52` in CopilotPanel's docked style
const FLOATING_MARGIN = 20 // matches right/bottom: 20px

// floatingBounds returns the max size that keeps the panel fully inside the
// viewport and clear of the Topbar. Falls back to the static limits when there
// is no window (SSR, unit tests).
//
// The Math.max against the minimum matters: on a very short viewport the
// viewport-derived cap can fall below minHeight, and without the guard clamp()
// would receive max < min and pin the panel to its minimum anyway — better to
// be explicit than to rely on that. (Viewports that small are already blocked
// upstream by ViewportGate.)
function floatingBounds(): { maxWidth: number; maxHeight: number } {
  const f = COPILOT_LIMITS.floating
  if (typeof window === 'undefined') return { maxWidth: f.maxWidth, maxHeight: f.maxHeight }
  return {
    maxWidth: Math.max(f.minWidth, Math.min(f.maxWidth, window.innerWidth - FLOATING_MARGIN * 2)),
    maxHeight: Math.max(
      f.minHeight,
      Math.min(f.maxHeight, window.innerHeight - TOPBAR_HEIGHT - FLOATING_MARGIN),
    ),
  }
}

const DEFAULT_LAYOUT: CopilotLayout = {
  mode: 'floating',
  dockedWidth: 460,
  floatingWidth: 480,
  floatingHeight: 620,
}

const STORAGE_KEY = 'kubebolt-copilot-layout'

function loadLayout(): CopilotLayout {
  if (typeof window === 'undefined') return DEFAULT_LAYOUT
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_LAYOUT
    const parsed = JSON.parse(raw)
    // Bound the RESTORED size too, not just live drags: a size saved on a big
    // monitor would otherwise reopen overlapping the Topbar on a laptop.
    const bounds = floatingBounds()
    return {
      mode: parsed.mode === 'docked' ? 'docked' : 'floating',
      dockedWidth: clamp(parsed.dockedWidth ?? DEFAULT_LAYOUT.dockedWidth, COPILOT_LIMITS.docked.minWidth, COPILOT_LIMITS.docked.maxWidth),
      floatingWidth: clamp(parsed.floatingWidth ?? DEFAULT_LAYOUT.floatingWidth, COPILOT_LIMITS.floating.minWidth, bounds.maxWidth),
      floatingHeight: clamp(parsed.floatingHeight ?? DEFAULT_LAYOUT.floatingHeight, COPILOT_LIMITS.floating.minHeight, bounds.maxHeight),
    }
  } catch {
    return DEFAULT_LAYOUT
  }
}

function clamp(n: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, n))
}

export function useCopilotLayout() {
  const [layout, setLayout] = useState<CopilotLayout>(() => loadLayout())

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(layout))
    } catch {
      // ignore quota errors
    }
  }, [layout])

  const setMode = useCallback((mode: CopilotMode) => {
    setLayout((prev) => ({ ...prev, mode }))
  }, [])

  const toggleMode = useCallback(() => {
    setLayout((prev) => ({ ...prev, mode: prev.mode === 'floating' ? 'docked' : 'floating' }))
  }, [])

  const setDockedWidth = useCallback((width: number) => {
    setLayout((prev) => ({
      ...prev,
      dockedWidth: clamp(width, COPILOT_LIMITS.docked.minWidth, COPILOT_LIMITS.docked.maxWidth),
    }))
  }, [])

  const setFloatingSize = useCallback((width: number, height: number) => {
    const bounds = floatingBounds()
    setLayout((prev) => ({
      ...prev,
      floatingWidth: clamp(width, COPILOT_LIMITS.floating.minWidth, bounds.maxWidth),
      floatingHeight: clamp(height, COPILOT_LIMITS.floating.minHeight, bounds.maxHeight),
    }))
  }, [])

  // Shrinking the WINDOW must not leave the panel overlapping the Topbar — the
  // panel grows upward from a fixed bottom edge, so a viewport that gets shorter
  // pushes its top edge off-screen without any user interaction. Re-clamp on
  // resize, and only write state when a bound actually bites so an ordinary
  // resize doesn't churn renders or localStorage.
  useEffect(() => {
    if (typeof window === 'undefined') return
    function onResize() {
      const bounds = floatingBounds()
      setLayout((prev) => {
        const w = Math.min(prev.floatingWidth, bounds.maxWidth)
        const h = Math.min(prev.floatingHeight, bounds.maxHeight)
        if (w === prev.floatingWidth && h === prev.floatingHeight) return prev
        return { ...prev, floatingWidth: w, floatingHeight: h }
      })
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  return { layout, setMode, toggleMode, setDockedWidth, setFloatingSize }
}
