import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'

// useScrollToHash — a link to "#error-hotspots" brings that panel into view.
// The router changes the URL but never scrolls, and the panel may not exist
// yet when the route renders (it waits for its data), so the target is looked
// for during a couple of seconds. The location key makes a second click on the
// same link scroll again.
export function useScrollToHash() {
  const { hash, pathname, key } = useLocation()
  useEffect(() => {
    const id = decodeURIComponent(hash.replace(/^#/, ''))
    if (!id) return
    const smooth = !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    let tries = 0
    let timer = 0
    const seek = () => {
      const el = document.getElementById(id)
      if (el) {
        el.scrollIntoView({ behavior: smooth ? 'smooth' : 'auto', block: 'start' })
        return
      }
      if (++tries < 25) timer = window.setTimeout(seek, 100)
    }
    seek()
    return () => window.clearTimeout(timer)
  }, [hash, pathname, key])
}
