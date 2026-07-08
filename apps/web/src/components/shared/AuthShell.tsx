import type { ReactNode } from 'react'
import { Check } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import { Starfield } from '@/components/shared/Starfield'
import { VERSION } from '@/version'

// AuthShell — the shared split-panel chrome for the pre-login pages (Login,
// SignUp). Left: a cinematic dark branding panel ported from the site's "3am"
// hero — a layered night scene (sky → breathing aurora → horizon glow →
// vignette) under a green-glowing headline. Right: the form. The whole shell is
// forced to the DARK theme (`.dark` wrapper) so the kb-* tokens resolve to the
// site-matching dark palette regardless of the user's in-app theme — these pages
// own their look. Uses ONLY existing tokens/fonts; the accent #1DBD7D ≈
// rgb(29,189,125) drives every glow.

const FEATURES = [
  'Kobi, your AI copilot for Kubernetes',
  'Investigate incidents & apply fixes',
  'Metrics, logs & topology across clusters',
]

export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string
  subtitle: string
  children: ReactNode
}) {
  return (
    <div className="dark min-h-screen flex bg-kb-bg text-kb-text-primary">
      {/* Branding panel — desktop only */}
      <div className="hidden lg:flex lg:w-[46%] relative overflow-hidden flex-col justify-between p-12 xl:p-16 2xl:p-24 isolate">
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
              'radial-gradient(60% 100% at 50% 100%, rgba(29,189,125,0.28), rgba(29,189,125,0.12) 38%, transparent 70%)',
            filter: 'blur(40px)',
            mixBlendMode: 'screen',
          }}
        />
        {/* horizon glow */}
        <div
          className="absolute inset-x-0 bottom-0 z-[1] pointer-events-none"
          style={{
            height: '34%',
            background: 'linear-gradient(180deg, transparent, rgba(29,189,125,0.06) 60%, rgba(29,189,125,0.11))',
            mixBlendMode: 'screen',
          }}
        />
        {/* twinkling starfield */}
        <Starfield className="absolute inset-0 z-[2] pointer-events-none" />
        {/* vignette */}
        <div
          className="absolute inset-0 z-[3] pointer-events-none"
          style={{
            background:
              'radial-gradient(115% 95% at 50% 38%, transparent 40%, rgba(0,0,0,0.55) 82%, rgba(0,0,0,0.85) 100%)',
          }}
        />

        <div className="relative z-[5] flex items-center gap-2.5">
          <div className="w-9 h-9 2xl:w-11 2xl:h-11 rounded-lg bg-kb-accent-light flex items-center justify-center">
            <KubeBoltLogo className="w-5 h-5 2xl:w-6 2xl:h-6 text-kb-accent" />
          </div>
          <span className="text-lg 2xl:text-xl font-semibold tracking-tight">KubeBolt</span>
        </div>

        <div className="relative z-[5] max-w-md xl:max-w-xl 2xl:max-w-2xl">
          <p className="inline-flex items-center gap-2 text-[11px] 2xl:text-xs font-mono uppercase tracking-[0.22em] text-kb-text-tertiary mb-5 2xl:mb-6">
            <span className="relative flex w-1.5 h-1.5">
              <span className="absolute inline-flex w-full h-full rounded-full bg-kb-accent opacity-60 animate-ping" />
              <span className="relative inline-flex w-1.5 h-1.5 rounded-full bg-kb-accent" />
            </span>
            AI operations platform for Kubernetes
          </p>
          <h2 className="text-[clamp(2.6rem,3.6vw,4.4rem)] font-semibold leading-[1.05] tracking-tight">
            Root cause in{' '}
            <span className="text-kb-accent [text-shadow:0_0_34px_rgba(29,189,125,0.4)]">seconds, not hours.</span>
          </h2>
          <p className="text-sm xl:text-base 2xl:text-lg text-kb-text-secondary mt-4 xl:mt-5 leading-relaxed max-w-sm xl:max-w-md">
            Kobi, your AI copilot, investigates incidents and proposes the fix you approve. Metrics, logs, and topology across clusters.
          </p>
          <ul className="mt-7 xl:mt-9 space-y-3 xl:space-y-4">
            {FEATURES.map((f) => (
              <li key={f} className="flex items-center gap-2.5 xl:gap-3 text-sm xl:text-base text-kb-text-secondary">
                <span className="w-4 h-4 xl:w-5 xl:h-5 rounded-full bg-kb-accent-light flex items-center justify-center shrink-0">
                  <Check className="w-2.5 h-2.5 xl:w-3 xl:h-3 text-kb-accent" />
                </span>
                {f}
              </li>
            ))}
          </ul>
        </div>

        <div className="relative z-[5] text-[10px] 2xl:text-xs font-mono text-kb-text-tertiary">KubeBolt v{VERSION}</div>
      </div>

      {/* Form panel */}
      <div className="flex-1 flex items-center justify-center p-6 relative overflow-hidden">
        {/* faint glow behind the form on small screens (branding panel hidden below lg) */}
        <div
          className="absolute inset-0 pointer-events-none lg:hidden"
          style={{ background: 'radial-gradient(80% 50% at 50% 0%, rgba(29,189,125,0.08), transparent 60%)' }}
        />
        <div className="relative w-full max-w-sm">
          {/* mobile logo */}
          <div className="lg:hidden flex flex-col items-center mb-8">
            <div className="w-12 h-12 rounded-xl bg-kb-accent-light flex items-center justify-center mb-3">
              <KubeBoltLogo className="w-7 h-7 text-kb-accent" />
            </div>
            <span className="text-lg font-semibold">KubeBolt</span>
          </div>
          <div className="mb-6">
            <h1 className="text-2xl font-semibold tracking-tight text-kb-text-primary">{title}</h1>
            <p className="text-sm text-kb-text-tertiary mt-1">{subtitle}</p>
          </div>
          {children}
        </div>
      </div>
    </div>
  )
}
