import { useEffect, useRef } from 'react'

// Starfield — a faint twinkling canvas starfield, ported from the site's "3am"
// hero. Stars are biased toward the upper sky, gently twinkle, and a few are
// green-tinted to echo the aurora. Fills its (relatively-positioned) parent.
// Pauses on tab-hide; under prefers-reduced-motion it paints a single static
// frame. Self-cleans on unmount.
export function Starfield({ className }: { className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = ref.current
    const ctx = canvas?.getContext('2d')
    const parent = canvas?.parentElement
    if (!canvas || !ctx || !parent) return

    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)')
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    type Star = { x: number; y: number; r: number; base: number; amp: number; sp: number; ph: number; tint: boolean }
    let stars: Star[] = []
    let w = 0
    let h = 0
    let raf = 0
    let resizeTimer = 0

    // Rare shooting star — a thin fast streak with a fading tail, kept in the
    // upper sky. Spawns every ~9–17s (randomized); never under reduced-motion.
    type Meteor = { x: number; y: number; ang: number; travel: number; len: number; start: number; dur: number }
    let meteor: Meteor | null = null
    let nextMeteorAt = 0
    function spawnMeteor(t: number) {
      const fromLeft = Math.random() < 0.6
      const startX = fromLeft ? Math.random() * w * 0.35 : w * (0.65 + Math.random() * 0.35)
      const startY = Math.random() * h * 0.26
      const deg = 20 + Math.random() * 14 // shallow diagonal descent
      const ang = fromLeft ? (deg * Math.PI) / 180 : Math.PI - (deg * Math.PI) / 180
      meteor = {
        x: startX,
        y: startY,
        ang,
        travel: Math.min(w, 820) * (0.42 + Math.random() * 0.3),
        len: 80 + Math.random() * 70,
        start: t,
        dur: 850 + Math.random() * 500,
      }
    }

    function resize() {
      const rect = parent!.getBoundingClientRect()
      w = rect.width
      h = rect.height
      if (w === 0 || h === 0) return
      canvas!.width = Math.max(1, Math.floor(w * dpr))
      canvas!.height = Math.max(1, Math.floor(h * dpr))
      canvas!.style.width = `${w}px`
      canvas!.style.height = `${h}px`
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0)
      const count = Math.min(150, Math.floor((w * h) / 6500))
      stars = []
      for (let i = 0; i < count; i++) {
        const yb = Math.pow(Math.random(), 1.4) // bias toward the top sky
        stars.push({
          x: Math.random() * w,
          y: yb * h * 0.82,
          r: Math.random() * 1.1 + 0.3,
          base: Math.random() * 0.5 + 0.22,
          amp: Math.random() * 0.4 + 0.12,
          sp: Math.random() * 0.0016 + 0.0005,
          ph: Math.random() * Math.PI * 2,
          tint: Math.random() < 0.16,
        })
      }
    }

    function paint(t: number) {
      ctx!.clearRect(0, 0, w, h)
      for (const s of stars) {
        const tw = reduce.matches ? s.base : s.base + s.amp * (0.5 + 0.5 * Math.sin(t * s.sp + s.ph))
        ctx!.globalAlpha = Math.max(0, Math.min(1, tw))
        ctx!.fillStyle = s.tint ? '#9fffd0' : '#eef0f5'
        ctx!.beginPath()
        ctx!.arc(s.x, s.y, s.r, 0, Math.PI * 2)
        ctx!.fill()
      }
      ctx!.globalAlpha = 1

      // Shooting star (skipped under reduced-motion / static frame).
      if (reduce.matches) return
      if (t >= nextMeteorAt) {
        if (nextMeteorAt === 0) nextMeteorAt = t + 4000 + Math.random() * 5000 // first one, after settle
        else {
          spawnMeteor(t)
          nextMeteorAt = t + 9000 + Math.random() * 8000
        }
      }
      if (meteor) {
        const p = (t - meteor.start) / meteor.dur
        if (p >= 1) {
          meteor = null
        } else {
          const dist = meteor.travel * p
          const hx = meteor.x + Math.cos(meteor.ang) * dist
          const hy = meteor.y + Math.sin(meteor.ang) * dist
          const tx = hx - Math.cos(meteor.ang) * meteor.len
          const ty = hy - Math.sin(meteor.ang) * meteor.len
          const fade = p < 0.18 ? p / 0.18 : 1 - (p - 0.18) / 0.82 // ease in, fade out
          const a = Math.max(0, Math.min(1, fade)) * 0.85
          const grad = ctx!.createLinearGradient(tx, ty, hx, hy)
          grad.addColorStop(0, 'rgba(159,255,208,0)')
          grad.addColorStop(1, `rgba(189,255,218,${a})`)
          ctx!.strokeStyle = grad
          ctx!.lineWidth = 1.5
          ctx!.lineCap = 'round'
          ctx!.beginPath()
          ctx!.moveTo(tx, ty)
          ctx!.lineTo(hx, hy)
          ctx!.stroke()
          ctx!.globalAlpha = a
          ctx!.fillStyle = '#eafff4'
          ctx!.beginPath()
          ctx!.arc(hx, hy, 1.5, 0, Math.PI * 2)
          ctx!.fill()
          ctx!.globalAlpha = 1
        }
      }
    }

    function loop(t: number) {
      paint(t)
      raf = requestAnimationFrame(loop)
    }

    function start() {
      if (reduce.matches || document.hidden) {
        paint(0)
        return
      }
      if (!raf) raf = requestAnimationFrame(loop)
    }
    function stop() {
      if (raf) {
        cancelAnimationFrame(raf)
        raf = 0
      }
    }

    resize()
    start()

    const onResize = () => {
      window.clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(() => {
        resize()
        if (reduce.matches || document.hidden) paint(0)
      }, 160)
    }
    const onVisibility = () => {
      if (document.hidden) stop()
      else start()
    }
    window.addEventListener('resize', onResize, { passive: true })
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      stop()
      window.clearTimeout(resizeTimer)
      window.removeEventListener('resize', onResize)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])

  return <canvas ref={ref} aria-hidden="true" className={className} />
}
