import { describe, it, expect, beforeEach } from 'vitest'
import { useEffect, useRef } from 'react'
import { render, renderHook } from '@testing-library/react'
import { enterKey, enterSkipped, markEnteredAt, resetEnterAnimations, useEnterMode } from './enterOnce'

// A page with one KPI card whose figure has shown a number (CountUp marks the
// page at that moment), inside the <main> Layout renders.
function Page({ mode, page }: { mode: 'play' | 'skip'; page: string }) {
  return (
    <main data-kb-enter={mode} data-kb-enter-page={page}>
      <Card />
    </main>
  )
}
function Card() {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (ref.current) markEnteredAt(ref.current)
  }, [])
  return <div ref={ref} data-testid="card" />
}

// Layout's hook across a sequence of pathnames (and active clusters, on
// cluster pages), as the user navigates.
function navigate(first: string, cluster?: string) {
  const hook = renderHook(({ page, cluster }) => useEnterMode(page, cluster), {
    initialProps: { page: first, cluster } as { page: string; cluster?: string },
  })
  return {
    mode: () => hook.result.current,
    to: (page: string, cluster?: string) => hook.rerender({ page, cluster }),
  }
}

describe('enterOnce', () => {
  beforeEach(() => resetEnterAnimations())

  it('plays the first visit to a page, and skips it after leaving and coming back', () => {
    const nav = navigate('/home')
    expect(nav.mode()).toBe('play')
    render(<Page mode="play" page="/home" />)
    nav.to('/fleet')
    nav.to('/home')
    expect(nav.mode()).toBe('skip')
  })

  it('keeps playing through more navigations to the page you are on (sign-in lands on Home twice)', () => {
    const nav = navigate('/home')
    render(<Page mode="play" page="/home" />) // the first figure marks the page
    nav.to('/home')
    nav.to('/home')
    expect(nav.mode()).toBe('play')
  })

  it('does not mark a page whose visit left before any figure showed', () => {
    const nav = navigate('/home')
    nav.to('/fleet')
    nav.to('/home')
    expect(nav.mode()).toBe('play')
  })

  it('is per page', () => {
    render(<Page mode="play" page="/home" />)
    expect(navigate('/fleet').mode()).toBe('play')
  })

  it('plays every page again after the next sign-in', () => {
    render(<Page mode="play" page="/home" />)
    resetEnterAnimations()
    expect(navigate('/home').mode()).toBe('play')
  })

  it('plays a cluster page again on another cluster, and not on one already seen', () => {
    const nav = navigate('/', 'ctx-a')
    expect(nav.mode()).toBe('play')
    render(<Page mode="play" page={enterKey('/', 'ctx-a')} />)
    nav.to('/', 'ctx-b')
    expect(nav.mode()).toBe('play')
    render(<Page mode="play" page={enterKey('/', 'ctx-b')} />)
    nav.to('/', 'ctx-a')
    expect(nav.mode()).toBe('skip')
  })

  it('does not treat the cluster resolving after a reload as a switch', () => {
    const nav = navigate('/capacity') // the cluster list has not answered yet
    expect(nav.mode()).toBe('play')
    nav.to('/capacity', 'ctx-a')
    expect(nav.mode()).toBe('play')
  })

  it('moves a mark made before the cluster was known onto that cluster', () => {
    const nav = navigate('/capacity')
    render(<Page mode="play" page={enterKey('/capacity')} />) // cards mounted first
    nav.to('/capacity', 'ctx-a')
    expect(navigate('/capacity', 'ctx-a').mode()).toBe('skip')
    expect(navigate('/capacity', 'ctx-b').mode()).toBe('play')
  })

  it('a skipped visit does not mark anything and tells CountUp to stay still', () => {
    const { getByTestId } = render(<Page mode="skip" page="/fleet" />)
    expect(enterSkipped(getByTestId('card'))).toBe(true)
    expect(navigate('/fleet').mode()).toBe('play')
  })
})
