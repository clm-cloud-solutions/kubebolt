import { useEffect, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import { Starfield } from '@/components/shared/Starfield'
import { VERSION } from '@/version'
import { authLicense, authPillars, authReleaseNotesUrl, authSiteUrl, authTagline } from '@/ee/authCopy'

// AuthShell — the shared split-panel chrome for the pre-login pages (Login,
// SignUp). Left: a cinematic dark branding panel ported from the site's "3am"
// hero — a layered night scene (sky → breathing aurora → horizon glow →
// vignette) under a green-glowing headline. Right: the form. The whole shell is
// forced to the DARK theme (`.dark` wrapper) so the kb-* tokens resolve to the
// site-matching dark palette regardless of the user's in-app theme — these pages
// own their look. Uses ONLY existing tokens/fonts; the brand accent #00e07a ≈
// rgb(0,224,122) drives every glow.

// The panel says a different thing at each moment of the funnel. It used to
// be one pitch for every page, so someone recovering a password — already a
// customer, mid-task — got the sales argument. Three panels now:
//
//   pitch  — login, signup: the only moment someone may not know the product.
//            The site's spine (See. Understand. Operate.), the pillars, the
//            proof line.
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
    <div className="dark h-full overflow-y-auto overscroll-contain bg-[var(--kobi-bg)] text-kb-text-primary">
    <div className="min-h-full flex">
      {/* Branding panel — desktop only */}
      <div className="hidden lg:flex lg:w-[52%] xl:w-[56%] relative overflow-hidden flex-col justify-between p-12 xl:p-16 2xl:p-24 isolate">
        {/* sky */}
        <div
          className="absolute inset-0 z-0 pointer-events-none"
          style={{ background: 'radial-gradient(120% 80% at 50% -20%, #0d0f0e 0%, #090a09 45%, #060706 100%)' }}
        />
        {/* breathing aurora */}
        <div
          className="absolute left-1/2 z-[1] pointer-events-none auth-aurora-anim"
          style={{
            bottom: '-14%',
            width: '160%',
            height: '62%',
            transform: 'translateX(-50%)',
            background:
              'radial-gradient(60% 100% at 50% 100%, rgba(0,224,122,0.28), rgba(0,224,122,0.12) 38%, transparent 70%)',
            filter: 'blur(40px)',
            mixBlendMode: 'screen',
          }}
        />
        {/* horizon glow */}
        <div
          className="absolute inset-x-0 bottom-0 z-[1] pointer-events-none"
          style={{
            height: '34%',
            background: 'linear-gradient(180deg, transparent, rgba(0,224,122,0.06) 60%, rgba(0,224,122,0.11))',
            mixBlendMode: 'screen',
          }}
        />
        {/* twinkling starfield */}
        <Starfield className="absolute inset-0 z-[2] pointer-events-none" />
        {/* vignette — centred toward the seam, so it darkens the screen's
            outer edges and NOT the edge this panel shares with the form.
            Centred, it pushed that edge to near-black against the form's
            lit half and the page split into two pages glued together. */}
        <div
          className="absolute inset-0 z-[3] pointer-events-none"
          style={{
            background:
              'radial-gradient(130% 100% at 78% 40%, transparent 45%, rgba(0,0,0,0.5) 82%, rgba(0,0,0,0.8) 100%)',
          }}
        />

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

      {/* Form panel */}
      <div className="flex-1 flex items-center justify-center p-6 relative overflow-hidden">
        {/* faint glow behind the form on small screens (branding panel hidden below lg) */}
        <div
          className="absolute inset-0 pointer-events-none lg:hidden"
          style={{ background: 'radial-gradient(80% 50% at 50% 0%, rgba(0,224,122,0.08), transparent 60%)' }}
        />
        {/* Desktop: the horizon of the left panel, continued across the seam.
            Without it this half is FLAT BLACK next to a half carrying aurora,
            starfield, horizon and vignette — and that contrast, not the card's
            width, is what made the form read as floating in a void. Far fainter
            than the branding side (0.05 vs 0.11) so it reads as the same scene
            seen from further away, never as a second light source. */}
        <div
          className="auth-seam-fade absolute inset-0 pointer-events-none hidden lg:block"
          style={{
            background: 'radial-gradient(75% 55% at 50% 100%, rgba(0,224,122,0.05), transparent 68%)',
            mixBlendMode: 'screen',
          }}
        />
        {/* The charco — the site's warm light with sand grain, shapeless on
            purpose (see .auth-charco in globals.css). Desktop only. */}
        {/* Everything that lights this half fades out toward the seam, so the
            two halves meet without a vertical cut. */}
        <div className="auth-seam-fade absolute inset-0 pointer-events-none hidden lg:block" aria-hidden="true">
          <div className="auth-charco absolute pointer-events-none" />
          <div className="auth-charco-grain absolute inset-0 pointer-events-none" />
        </div>
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
        <Eyebrow text="Kubernetes operations platform" />
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
      <Eyebrow text="Kubernetes operations platform" />
      <h2 className="text-[clamp(2.6rem,3.6vw,4.4rem)] font-semibold leading-[1.05] tracking-tight">
        See. Understand.{' '}
        <span className="text-kb-accent [text-shadow:0_0_34px_rgba(0,224,122,0.4)]">Operate.</span>
      </h2>
      <p className="text-sm xl:text-base 2xl:text-lg text-kb-text-secondary mt-4 xl:mt-5 leading-relaxed max-w-md xl:max-w-lg">
        {authTagline}
      </p>
      <Pillars />
      <p className="mt-7 xl:mt-9 pt-5 border-t border-kb-border text-xs xl:text-sm text-kb-text-tertiary max-w-md xl:max-w-lg">
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
// it is: a small console of the product, with its cluster named and one line
// narrating it. Cropped and rotated in the panel's corner it read as
// decoration; here one node goes amber, the line says why, and a few seconds
// later both say it healed: seen, understood, operated, in one cell. The line
// and the node run on the same 10 s clock (globals.css). Hidden on short
// screens, where it would push the copy; held still, in the incident state,
// under prefers-reduced-motion.
function ClusterConsole() {
  const cells = Array.from({ length: 16 }, (_, i) => i)
  return (
    <div
      aria-hidden="true"
      className="hidden xl:block [@media(max-height:760px)]:!hidden w-[17rem] 2xl:w-[19rem] shrink-0 rounded-xl border border-kb-border auth-console p-3"
    >
      <div className="flex items-center justify-between mb-2.5 font-mono text-[10px] text-kb-text-tertiary">
        <span className="inline-flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-kb-accent shadow-[0_0_6px_#00e07a]" />
          prod-eu-1
        </span>
        <span>16 nodes</span>
      </div>
      <div className="grid grid-cols-8 gap-1.5">
        {cells.map((i) => (
          <div key={i} className={`h-4 rounded-[4px] border ${i === 5 ? 'auth-node-heal' : 'auth-node'}`} />
        ))}
      </div>
      <div className="relative h-4 mt-2.5 font-mono text-[10px]">
        <span className="absolute inset-0 truncate text-status-warn auth-line-incident">node-06 · memory pressure</span>
        <span className="absolute inset-0 truncate text-kb-accent auth-line-healed">node-06 · healthy again</span>
      </div>
    </div>
  )
}
