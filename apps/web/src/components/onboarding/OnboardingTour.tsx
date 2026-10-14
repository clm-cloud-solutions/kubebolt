import { useEffect, useRef } from 'react'

// OnboardingTour — the 18 s product tour shown while no cluster is registered
// (the setup wizard's Agent step and the empty Home). Silent, looped, plays on
// its own; with reduced motion it shows the poster and a play button instead.
//
// Source: kubebolt-onboarding-video (storyboard.oss.en.json, render.py
// --product --height 720), built from the README screenshots of this edition.
// Served from /media/ — never /onboarding/: a public folder named like an app
// route makes nginx's `try_files $uri $uri/` answer a reload of that route
// with the directory instead of the SPA.
const SRC = '/media/onboarding-tour-en.mp4'
const POSTER = '/media/onboarding-tour-en-poster.jpg'

export function OnboardingTour({ className = '' }: { className?: string }) {
  const ref = useRef<HTMLVideoElement>(null)
  const reduceMotion =
    typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

  useEffect(() => {
    const v = ref.current
    if (!v) return
    // React sets `muted` as a property, not as the HTML attribute, and Safari
    // only lets a video start by itself when it is muted at play time.
    v.muted = true
    if (!reduceMotion) v.play().catch(() => {})
  }, [reduceMotion])

  return (
    <video
      ref={ref}
      className={`w-full rounded-lg border border-kb-border bg-kb-bg ${className}`}
      src={SRC}
      poster={POSTER}
      autoPlay={!reduceMotion}
      controls={reduceMotion}
      muted
      loop
      playsInline
      preload="metadata"
      aria-label="KubeBolt preview: a cluster overview, Kobi drafting a fix, security findings, the cluster map and cost"
    />
  )
}
