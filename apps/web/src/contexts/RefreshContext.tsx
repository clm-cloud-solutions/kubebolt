import { createContext, useContext, useState, type ReactNode } from 'react'

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

function loadInterval(): RefreshInterval {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored) {
      const parsed = Number(stored) as RefreshInterval
      if (VALID_INTERVALS.includes(parsed)) return parsed
    }
  } catch {}
  return 30_000
}

export function RefreshProvider({ children }: { children: ReactNode }) {
  const [interval, setIntervalState] = useState<RefreshInterval>(loadInterval)

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

export function useRefreshInterval() {
  return useContext(RefreshContext)
}
