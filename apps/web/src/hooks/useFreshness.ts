import { useEffect, useState } from 'react'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import { freshnessOf, type Freshness } from '@/utils/freshness'

// useFreshness — freshnessOf for a polled query, re-read every few seconds so
// a poll that stops answering turns "delayed" without waiting for a re-render.
export function useFreshness(updatedAt: number, failed: boolean): Freshness {
  const { interval } = useRefreshInterval()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5_000)
    return () => clearInterval(t)
  }, [])
  return freshnessOf({ updatedAt, intervalMs: interval, now, failed })
}
