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
    return {
      mode: parsed.mode === 'docked' ? 'docked' : 'floating',
      dockedWidth: clamp(parsed.dockedWidth ?? DEFAULT_LAYOUT.dockedWidth, COPILOT_LIMITS.docked.minWidth, COPILOT_LIMITS.docked.maxWidth),
      floatingWidth: clamp(parsed.floatingWidth ?? DEFAULT_LAYOUT.floatingWidth, COPILOT_LIMITS.floating.minWidth, COPILOT_LIMITS.floating.maxWidth),
      floatingHeight: clamp(parsed.floatingHeight ?? DEFAULT_LAYOUT.floatingHeight, COPILOT_LIMITS.floating.minHeight, COPILOT_LIMITS.floating.maxHeight),
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
    setLayout((prev) => ({
      ...prev,
      floatingWidth: clamp(width, COPILOT_LIMITS.floating.minWidth, COPILOT_LIMITS.floating.maxWidth),
      floatingHeight: clamp(height, COPILOT_LIMITS.floating.minHeight, COPILOT_LIMITS.floating.maxHeight),
    }))
  }, [])

  return { layout, setMode, toggleMode, setDockedWidth, setFloatingSize }
}
