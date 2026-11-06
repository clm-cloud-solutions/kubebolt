import { useEffect, useState, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import { Starfield } from '@/components/shared/Starfield'
import { KobiFace } from '@/components/kobi'
import { VERSION } from '@/version'
import {
  authConsole,
  authEyebrow,
  authLicense,
  authPillars,
  authReleaseNotesUrl,
  authSiteUrl,
  authTagline,
} from '@/ee/authCopy'

// AuthShell — the shared chrome for the pre-login pages (Login, SignUp, the
// password and invite flows). ONE night scene ported from the site's "3am"
// hero, at the hero's measured values — sky → two breathing auroras →
// starfield → vignette — runs edge to edge behind two columns: the branding
// copy on the left (desktop only) and the form on the right, on the site's
// opaque card. The whole shell is forced
// to the DARK theme (`.dark` wrapper) so the kb-* tokens resolve to the
// site-matching dark palette regardless of the user's in-app theme — these
// pages own their look. Uses ONLY existing tokens/fonts; the brand accent
// #00e07a ≈ rgb(0,224,122) drives every glow.
//
// It used to be two backgrounds: the scene behind the copy and a near-black
// half behind the form, with three layers (a seam fade, the horizon carried
// across, a warm light) to hide where they met. The halves still read as two
// pages glued together. One scene has no seam to hide.

// The panel says a different thing at each moment of the funnel. It used to
// be one pitch for every page, so someone recovering a password — already a
// customer, mid-task — got the sales argument. Three panels now:
//
//   pitch  — login, signup: the only moment someone may not know the product.
//            The site home's headline (From alert to root cause), the
//            pillars, the proof line, and Kobi working a node in the console.
//   return — forgot / reset password: no pitch. They want back in.
//   invite — accept-invite: they do not know the product, and they are not
//            the buyer either. What it is, without the plan.
//
// Pillars, tagline and licence come from ee/authCopy (per edition), so the
// panel never promises a feature — or a licence — the build does not have.
export type AuthPanel = 'pitch' | 'return' | 'invite'

// The proof line: the one sentence that is cheap to say and hard to imitate.
const PROOF_LINE = 'Running in our own production: incidents, capacity and cost, operated from one place.'

// The public site the auth pages link back out to, per edition (ee/authCopy):
// the commercial build links its homepage; a self-hosted open-source install
// has no site of its own to send people to, so it renders no link at all.
function SiteLink({ className, children }: { className: string; children: ReactNode }) {
  if (!authSiteUrl) return <div className={className}>{children}</div>
  return (
    <a href={authSiteUrl} aria-label="Go to kubebolt.io" className={`${className} hover:opacity-90 transition-opacity`}>
      {children}
    </a>
  )
}

export function AuthShell({
  title,
  subtitle,
  panel = 'pitch',
  children,
}: {
  title: string
  // Which story the branding panel tells — see AuthPanel above.
  panel?: AuthPanel
  // Optional, and Login passes nothing. It read "Sign in to your account."
  // under a heading that says "Welcome back", above a form labelled Email and
  // Password — the same fact stated three times. A subtitle earns its place only
  // when it says something the title and the form do not, which is the case on
  // signup ("Connect your first cluster in a few minutes") and on the OAuth hand-off
  // ("Name your organization to finish signing up"), and is not the case here.
  subtitle?: string
  children: ReactNode
}) {
  // Wide monitors: the page scales with the screen, like the site does from
  // 1920 and 2560 up. Everything here is sized in rem, so raising the root
  // size while an auth page is mounted scales the form, the copy, the spacing
  // and the grid together — instead of a 384px form alone in half of a 2560
  // screen. Removed on unmount, so the app itself is never touched.
  // The form column is keyed by route, so moving between sign in, sign up and
  // password recovery replays the card's entrance (see .auth-form in
  // globals.css) instead of swapping the fields in place.
  const { pathname } = useLocation()

  useEffect(() => {
    document.documentElement.classList.add('auth-scale')
    return () => document.documentElement.classList.remove('auth-scale')
  }, [])

  return (
    // Its own scroll container: html, body and #root are overflow:hidden
    // (the app scrolls inside its panes), so a min-h-screen page taller than
    // the phone — signup, or login with the keyboard up — was clipped with no
    // way to reach the rest of the form. The inner min-h-full row keeps both
    // columns as tall as the page, however long the form runs.
    <div className="auth-scene dark h-full overflow-y-auto overscroll-contain bg-[var(--kobi-bg)] text-kb-text-primary">
    <div className="relative min-h-full flex isolate">
      {/* The scene — one sky behind both columns, on every viewport. It is
          sized by this row, so it grows with a long form instead of ending
          at the first screen. */}
      <div className="absolute inset-0 z-0 overflow-hidden pointer-events-none" aria-hidden="true">
        {/* The night is the site hero's (HeroCluster), values included. It
            used to run a stronger green — an aurora at 0.34 plus a horizon
            glow at 0.11 — and the lower third read as a green fill; on the
            site green is an accent, never a fill, and the hero's aurora was
            measured to 0.20. */}
        {/* sky — lit from above, a step off black so the page has a floor */}
        <div
          className="absolute inset-0"
          style={{ background: 'radial-gradient(120% 80% at 50% -20%, #0d0f0e 0%, #090a09 45%, #060706 100%)' }}
        />
        {/* breathing aurora — the whole horizon, not one column's */}
        <div
          className="absolute left-1/2 z-[1] auth-aurora-anim"
          style={{
            bottom: '-14%',
            width: '160%',
            height: '62%',
            transform: 'translateX(-50%)',
            background:
              'radial-gradient(60% 100% at 50% 100%, rgba(0,224,122,0.2), rgba(0,184,100,0.1) 38%, transparent 70%)',
            filter: 'blur(34px)',
            mixBlendMode: 'screen',
          }}
        />
        {/* a second, slower aurora, so the light never breathes in one piece */}
        <div
          className="absolute left-1/2 z-[1] auth-aurora-anim"
          style={{
            bottom: '-10%',
            width: '120%',
            height: '50%',
            transform: 'translateX(-50%)',
            background: 'radial-gradient(55% 100% at 42% 100%, rgba(0,224,122,0.14), transparent 65%)',
            filter: 'blur(48px)',
            opacity: 0.85,
            mixBlendMode: 'screen',
            animationDuration: '19s',
            animationDirection: 'alternate-reverse',
          }}
        />
        {/* twinkling starfield */}
        <Starfield className="absolute inset-0 z-[2]" />
        {/* vignette — centred on the page now that there is no seam to keep
            lit; soft enough that the corners stay sky, not black */}
        <div
          className="absolute inset-0 z-[3]"
          style={{
            background:
              'radial-gradient(140% 110% at 50% 45%, transparent 55%, rgba(0,0,0,0.3) 85%, rgba(0,0,0,0.5) 100%)',
          }}
        />
      </div>

      {/* Branding column — desktop only */}
      {/* The panel is sized to the screen's height as well as its width:
          the vertical padding follows the height (5vh, within bounds) and so
          does the headline (h2 below), so the three-line headline, the
          pillars and Kobi's console fit from a 13" laptop to a 27" monitor
          without scrolling. gap-6 keeps logo, copy and console from ever
          touching. */}
      <div className="hidden lg:flex lg:w-[52%] xl:w-[56%] relative z-[1] flex-col justify-between gap-6 px-12 py-[clamp(1.5rem,5vh,3rem)] xl:px-16 xl:py-[clamp(1.5rem,5vh,4rem)] 2xl:px-24">
        <SiteLink className="relative z-[5] flex items-center gap-2.5 w-fit">
          <div className="w-9 h-9 2xl:w-11 2xl:h-11 rounded-lg bg-kb-accent-light flex items-center justify-center">
            <KubeBoltLogo className="w-6 h-6 2xl:w-7 2xl:h-7 text-kb-accent" />
          </div>
          <span className="text-lg 2xl:text-xl font-semibold tracking-tight">KubeBolt</span>
        </SiteLink>

        <div className="relative z-[5] max-w-md xl:max-w-xl 2xl:max-w-2xl">
          {/* Keyed by panel so the story re-enters when it changes (sign in →
              forgot password swaps the pitch for the way back in). */}
          <div key={panel} className="auth-copy-in">
            <BrandingCopy panel={panel} />
          </div>
        </div>

        {/* The console starts on the copy's margin, above the version, so it
            reads as part of the panel rather than a widget adrift in the
            corner. */}
        <div className="relative z-[5] flex flex-col items-start gap-5">
          <ClusterConsole />
          <VersionPill />
        </div>
      </div>

      {/* Form column — on the same scene; the card carries its own surface
          (glass, see .auth-form .bg-kb-card in globals.css). */}
      <div className="flex-1 flex items-center justify-center p-6 relative z-[1]">
        <div key={pathname} className="relative w-full max-w-sm auth-form">
          {/* mobile logo */}
          <SiteLink className="lg:hidden flex flex-col items-center mb-8">
            <div className="w-12 h-12 rounded-xl bg-kb-accent-light flex items-center justify-center mb-3">
              <KubeBoltLogo className="w-7 h-7 text-kb-accent" />
            </div>
            <span className="text-lg font-semibold">KubeBolt</span>
          </SiteLink>
          {/* The title sits on the ground and the page's own card below it —
              ONE card. The shell used to wrap the pages' card in a second
              surface, and a card inside a card is what made the form read as
              heavy. The card, the fields and the buttons take the site's
              vocabulary from the .auth-form rules in globals.css, so the six
              pages keep their markup. */}
          <div className="mb-6 auth-rise">
            <h1 className="text-2xl font-semibold tracking-tight text-kb-text-primary">{title}</h1>
            {subtitle && <p className="text-sm text-kb-text-tertiary mt-1">{subtitle}</p>}
          </div>
          {children}
          {/* Explicit way back to the marketing site — visible on every
              viewport (the branding panel with the clickable logo is hidden
              below lg). */}
          {authSiteUrl && (
            <p className="mt-6 text-center">
              <a
                href={authSiteUrl}
                className="font-mono text-[11px] text-kb-text-tertiary hover:text-kb-accent transition-colors"
              >
                ← Back to kubebolt.io
              </a>
            </p>
          )}
        </div>
      </div>
    </div>
    </div>
  )
}

// BrandingCopy is the panel's story for the moment the page belongs to.
function BrandingCopy({ panel }: { panel: AuthPanel }) {
  if (panel === 'return') {
    return (
      <>
        <Eyebrow text={authEyebrow} />
        <h2 className="text-[clamp(2.2rem,3vw,3.6rem)] font-semibold leading-[1.08] tracking-tight">
          Your clusters are{' '}
          <span className="text-kb-accent [text-shadow:0_0_34px_rgba(0,224,122,0.4)]">right where you left them.</span>
        </h2>
        <p className="text-sm xl:text-base 2xl:text-lg text-kb-text-secondary mt-4 xl:mt-5 leading-relaxed max-w-md xl:max-w-lg">
          Set a new password and you are back in, on the same clusters, dashboards and history.
        </p>
      </>
    )
  }
  if (panel === 'invite') {
    return (
      <>
        <Eyebrow text="You've been invited" />
        <h2 className="text-[clamp(2.2rem,3vw,3.6rem)] font-semibold leading-[1.08] tracking-tight">
          One place to see, understand and{' '}
          <span className="text-kb-accent [text-shadow:0_0_34px_rgba(0,224,122,0.4)]">operate.</span>
        </h2>
        <p className="text-sm xl:text-base 2xl:text-lg text-kb-text-secondary mt-4 xl:mt-5 leading-relaxed max-w-md xl:max-w-lg">
          {authTagline}
        </p>
        <Pillars />
      </>
    )
  }
  return (
    <>
      <Eyebrow text={authEyebrow} />
      {/* The site home's headline, so whoever clicks «Start free» there
          lands on the same promise. Three lines, broken where the site
          breaks them; sized by the screen's width or its height, whichever
          is tighter (6vh), so three lines fit on a short screen. */}
      <h2 className="text-[clamp(2.6rem,min(3.6vw,6vh),4.4rem)] font-semibold leading-[1.05] tracking-tight">
        <span className="block">From alert</span>
        <span className="block">
          to <span className="text-kb-accent [text-shadow:0_0_34px_rgba(0,224,122,0.4)]">root cause</span>,
        </span>
        <span className="block">no kubectl needed.</span>
      </h2>
      <p className="text-sm xl:text-base 2xl:text-lg text-kb-text-secondary mt-4 xl:mt-5 leading-relaxed max-w-md xl:max-w-lg">
        {authTagline}
      </p>
      <Pillars />
      {/* Steps aside below 1000px of height (laptops and most desktop
          browsers): with the three-line headline, the panel and Kobi's
          console do not fit together, and the console says the same thing by
          showing it. Measured with it: 1728×990 ran 24px past the screen. */}
      <p className="mt-7 xl:mt-9 pt-5 border-t border-kb-border text-xs xl:text-sm text-kb-text-tertiary max-w-md xl:max-w-lg [@media(max-height:1000px)]:hidden">
        {PROOF_LINE}
      </p>
    </>
  )
}

function Eyebrow({ text }: { text: string }) {
  return (
    <p className="inline-flex items-center gap-2 text-[11px] 2xl:text-xs font-mono uppercase tracking-[0.22em] text-kb-text-tertiary mb-5 2xl:mb-6">
      <span className="relative flex w-1.5 h-1.5">
        <span className="absolute inline-flex w-full h-full rounded-full bg-kb-accent opacity-60 animate-ping motion-reduce:animate-none" />
        <span className="relative inline-flex w-1.5 h-1.5 rounded-full bg-kb-accent" />
      </span>
      {text}
    </p>
  )
}

function Pillars() {
  return (
    <ul className="mt-7 xl:mt-9 space-y-3 xl:space-y-4">
      {authPillars.map((p) => (
        <li key={p.tag} className="flex items-baseline gap-3 text-sm xl:text-base text-kb-text-secondary">
          <span className="w-24 shrink-0 font-mono text-[10px] xl:text-[11px] uppercase tracking-[0.14em] text-kb-accent">
            {p.tag}
          </span>
          {p.text}
        </li>
      ))}
    </ul>
  )
}

// VersionPill: the version, the licence when the edition has an open one, and
// a link to the release notes when they are public.
function VersionPill() {
  const label = authLicense ? `v${VERSION} · ${authLicense}` : `KubeBolt v${VERSION}`
  const cls =
    'text-[10px] 2xl:text-xs font-mono text-kb-text-tertiary border border-kb-border rounded-full px-3 py-1'
  if (!authReleaseNotesUrl) return <span className={cls}>{label}</span>
  return (
    <a
      href={authReleaseNotesUrl}
      target="_blank"
      rel="noreferrer"
      className={`${cls} hover:text-kb-accent hover:border-kb-accent transition-colors`}
    >
      {label}
    </a>
  )
}

// ClusterConsole is the site's signature — a grid of nodes — framed as what
// it is: a small console of the product, and Kobi working it. One node goes
// amber; under the grid Kobi's face and the line beside it tell what it does
// about it, beat by beat, until the node is green again: seen, understood,
// operated, in one cell. The beats are the edition's (ee/authCopy
// authConsole): OSS stops for your approval, the commercial build runs it in
// Autopilot. The face takes the states the panel header shows, with the
// state written beside it, as the site's product views do.
//
// The cluster is "your-cluster", not a production name: the console is an
// example, and the proof line above it is about our production, not this.
//
// Hidden below xl and on short screens, where it would push the copy; held
// still on one beat under prefers-reduced-motion.
function ClusterConsole() {
  const { who, autopilot, loop, hold, beats } = authConsole
  const [still] = useState(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches)
  const [at, setAt] = useState(() => (still ? hold : 0))

  useEffect(() => {
    if (still) return
    let i = 0
    let timer = 0
    const next = () => {
      const n = (i + 1) % beats.length
      const wait = (n === 0 ? loop : beats[n].at) - beats[i].at
      timer = window.setTimeout(() => {
        i = n
        setAt(n)
        next()
      }, wait * 1000)
    }
    next()
    return () => window.clearTimeout(timer)
  }, [still, beats, loop])

  const beat = beats[at]
  const cells = Array.from({ length: 16 }, (_, i) => i)
  return (
    <div
      aria-hidden="true"
      className="hidden xl:block [@media(max-height:780px)]:!hidden w-[19rem] 2xl:w-[21rem] shrink-0 rounded-xl border border-kb-border auth-console p-3"
    >
      <div className="flex items-center justify-between mb-2.5 font-mono text-[10px] text-kb-text-tertiary">
        <span className="inline-flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-kb-accent shadow-[0_0_6px_#00e07a]" />
          your-cluster
        </span>
        <span>16 nodes</span>
      </div>
      <div className="grid grid-cols-8 gap-1.5">
        {cells.map((i) => (
          <div
            key={i}
            className="h-4 rounded-[4px] border auth-node"
            data-warn={i === 5 && beat.node === 'warn' ? '' : undefined}
          />
        ))}
      </div>
      <div className="flex items-center gap-2.5 mt-3 pt-3 border-t border-[rgba(245,242,236,0.07)]">
        <KobiFace size={32} state={beat.state} autopilot={autopilot} />
        {/* Keyed by beat, so each line fades in (.auth-console-say). */}
        <div key={at} className="min-w-0 grid gap-1 auth-console-say">
          <span className="font-mono text-[10px] leading-none uppercase tracking-[0.12em] text-kb-text-tertiary whitespace-nowrap">
            {who} ·{' '}
            <span className="auth-console-state" data-state={beat.state}>
              {beat.label}
            </span>
          </span>
          <span className="font-mono text-[11px] leading-tight text-[var(--kobi-code-text)] truncate">{beat.detail}</span>
        </div>
      </div>
    </div>
  )
}
