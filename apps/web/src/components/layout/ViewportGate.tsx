import { useEffect, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'

// KubeBolt's console is a dense, desktop-first Kubernetes UI (see the responsive
// survey — tables horizontal-scroll, the cluster map jams, detail grids are
// desktop-locked). Rather than silently break on small screens, we gate them:
//   < 768px (phones)      → a full replacement notice; the shell is unusable here.
//   768–1024px (tablets)  → render the app, but a dismissible banner sets
//                           expectations that map/tables are cramped.
//   ≥ 1024px (desktop)    → nothing; normal experience.
const PHONE_MAX = 768
const TABLET_MAX = 1024
const TABLET_DISMISS_KEY = 'kb-viewport-tablet-dismissed'

function useViewportWidth(): number {
  const [width, setWidth] = useState(() =>
    typeof window !== 'undefined' ? window.innerWidth : 1280,
  )
  useEffect(() => {
    const onResize = () => setWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return width
}

function PhoneNotice() {
  return (
    <div className="fixed inset-0 z-[100] flex flex-col items-center justify-center bg-kb-bg px-8 text-center">
      <div className="w-12 h-12 rounded-2xl bg-kb-accent-light flex items-center justify-center mb-6">
        <KubeBoltLogo className="w-6 h-6 text-kb-accent" />
      </div>
      <h1 className="text-lg font-semibold text-kb-text-primary mb-2">
        Best viewed on a larger screen
      </h1>
      <p className="text-sm text-kb-text-secondary max-w-sm leading-relaxed">
        KubeBolt is a dense Kubernetes console built for desktop. It needs a screen at least
        1024px wide. Please resize your window or open it on a larger device.
      </p>
    </div>
  )
}

function TabletBanner({ onDismiss }: { onDismiss: () => void }) {
  return (
    <div className="fixed bottom-0 inset-x-0 z-[90] flex items-center gap-2 px-4 py-2 bg-kb-card border-t border-kb-border text-xs text-kb-text-secondary">
      <span className="flex-1">
        Some views (cluster map, resource tables) are optimized for wider screens.
      </span>
      <button
        onClick={onDismiss}
        className="text-kb-text-tertiary hover:text-kb-text-primary p-0.5 rounded hover:bg-kb-card-hover transition-colors shrink-0"
        title="Dismiss"
        aria-label="Dismiss small-screen notice"
      >
        <X className="w-3.5 h-3.5" />
      </button>
    </div>
  )
}

// Wraps the authenticated app shell. Auth pages render outside this (they're
// already responsive), so a phone can still sign in — it just can't use the
// dashboard until it's on a wider viewport.
export function ViewportGate({ children }: { children: ReactNode }) {
  const width = useViewportWidth()
  const [tabletDismissed, setTabletDismissed] = useState(() => {
    try {
      return sessionStorage.getItem(TABLET_DISMISS_KEY) === '1'
    } catch {
      return false
    }
  })

  const dismissTablet = () => {
    setTabletDismissed(true)
    try {
      sessionStorage.setItem(TABLET_DISMISS_KEY, '1')
    } catch {
      /* sessionStorage unavailable — the banner just re-shows next mount */
    }
  }

  if (width < PHONE_MAX) return <PhoneNotice />

  return (
    <>
      {children}
      {width < TABLET_MAX && !tabletDismissed && <TabletBanner onDismiss={dismissTablet} />}
    </>
  )
}
