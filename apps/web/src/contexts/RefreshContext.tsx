import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { useUIConfig } from '@/hooks/useUIConfig'

type RefreshInterval = 5_000 | 10_000 | 15_000 | 30_000 | 60_000 | 120_000

interface RefreshContextValue {
  interval: RefreshInterval
  setInterval: (interval: RefreshInterval) => void
}

const RefreshContext = createContext<RefreshContextValue>({
  interval: 30_000,
  setInterval: () => {},
})

const STORAGE_KEY = 'kb-refresh-interval'

const VALID_INTERVALS: RefreshInterval[] = [5_000, 10_000, 15_000, 30_000, 60_000, 120_000]

function loadStoredInterval(key = STORAGE_KEY): RefreshInterval | null {
  try {
    const stored = localStorage.getItem(key)
    if (stored) {
      const parsed = Number(stored) as RefreshInterval
      if (VALID_INTERVALS.includes(parsed)) return parsed
    }
  } catch {}
  return null
}

export function RefreshProvider({ children }: { children: ReactNode }) {
  // Initial state: localStorage if the user has a saved preference,
  // otherwise the hardcoded 30s baseline. The server default loaded
  // below replaces this baseline ONLY when the user has no saved
  // preference — explicit per-user choice always wins over the
  // operator's "team default".
  const [interval, setIntervalState] = useState<RefreshInterval>(
    () => loadStoredInterval() ?? 30_000,
  )
  const uiConfig = useUIConfig()

  useEffect(() => {
    if (loadStoredInterval() !== null) return // user has a saved choice; respect it
    const serverDefault = (uiConfig.defaultRefreshIntervalSeconds * 1000) as RefreshInterval
    if (VALID_INTERVALS.includes(serverDefault)) {
      setIntervalState(serverDefault)
    }
  }, [uiConfig.defaultRefreshIntervalSeconds])

  function setInterval(value: RefreshInterval) {
    setIntervalState(value)
    try { localStorage.setItem(STORAGE_KEY, String(value)) } catch {}
  }

  return (
    <RefreshContext.Provider value={{ interval, setInterval }}>
      {children}
    </RefreshContext.Provider>
  )
}

// ScopedRefreshProvider gives one surface its own refresh interval, apart from
// the app-wide one the cluster pages share: a choice made there does not carry
// over, and the org's default (a cluster-dashboard setting) does not apply.
// Everything under it — the freshness picker included — reads and sets this one.
export function ScopedRefreshProvider({
  storageKey,
  defaultInterval,
  children,
}: {
  storageKey: string
  defaultInterval: RefreshInterval
  children: ReactNode
}) {
  const [interval, setIntervalState] = useState<RefreshInterval>(
    () => loadStoredInterval(storageKey) ?? defaultInterval,
  )

  function setInterval(value: RefreshInterval) {
    setIntervalState(value)
    try { localStorage.setItem(storageKey, String(value)) } catch {}
  }

  return (
    <RefreshContext.Provider value={{ interval, setInterval }}>
      {children}
    </RefreshContext.Provider>
  )
}

export function useRefreshInterval() {
  return useContext(RefreshContext)
}
