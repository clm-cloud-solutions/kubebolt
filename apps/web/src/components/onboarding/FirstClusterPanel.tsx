import { Link } from 'react-router-dom'
import { Check, Laptop, Server } from 'lucide-react'
import { OnboardingTour } from '@/components/onboarding/OnboardingTour'

// FirstClusterPanel — what Home shows while no cluster is registered.
//
// Home is where every visit lands. With no cluster it used to say
// "0 clusters · all reporting" over empty panels, and the only way forward was
// a pill to Fleet and a menu from there. Now the next step is the page.
export function FirstClusterPanel({ onConnect }: { onConnect: (where: 'own' | 'local') => void }) {
  return (
    <section className="kb-panel p-5 grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)] items-start">
      <div className="space-y-5 min-w-0">
        <ol className="grid grid-cols-3 gap-2" aria-label="Getting started">
          <Progress state="done" label="KubeBolt running" />
          <Progress state="now" label="Install the agent" />
          <Progress state="next" label="First data" />
        </ol>
        <div className="space-y-2">
          <h2 className="text-[20px] font-semibold text-kb-text-primary tracking-[-0.01em]">Connect your first cluster</h2>
          <p className="text-[13px] text-kb-text-secondary leading-relaxed max-w-[52ch]">
            One block to paste, about two minutes. Health, incidents, the map and cost show up here as soon as the agent
            reports. The agent is read-only.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => onConnect('own')}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-kb-accent text-white text-[13px] font-medium hover:bg-kb-accent/90 transition-colors"
          >
            <Server className="w-4 h-4" /> Connect my cluster
          </button>
          <button
            type="button"
            onClick={() => onConnect('local')}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-lg border border-kb-border text-[13px] text-kb-text-primary hover:bg-kb-card-hover transition-colors"
          >
            <Laptop className="w-4 h-4" /> Try it on my laptop
          </button>
        </div>
        <p className="text-[12px] text-kb-text-tertiary">
          Someone else installs things in your clusters?{' '}
          <Link to="/admin/access" className="text-kb-accent underline underline-offset-2">
            Add them
          </Link>{' '}
          as an admin and they can connect it.
        </p>
      </div>
      <figure className="space-y-2 min-w-0">
        <OnboardingTour />
        <figcaption className="text-[11px] font-mono uppercase tracking-[0.12em] text-kb-text-tertiary">
          What you'll see once data arrives
        </figcaption>
      </figure>
    </section>
  )
}

function Progress({ state, label }: { state: 'done' | 'now' | 'next'; label: string }) {
  return (
    <li
      className={`rounded-lg border px-3 py-2 space-y-0.5 ${
        state === 'done'
          ? 'border-status-ok/40 bg-status-ok-dim'
          : state === 'now'
            ? 'border-kb-accent ring-2 ring-kb-accent/20'
            : 'border-kb-border'
      }`}
    >
      <div
        className={`text-[10px] font-mono uppercase tracking-[0.1em] flex items-center gap-1 ${
          state === 'done' ? 'text-status-ok' : state === 'now' ? 'text-kb-accent' : 'text-kb-text-tertiary'
        }`}
      >
        {state === 'done' && <Check className="w-3 h-3" />}
        {state === 'done' ? 'Done' : state === 'now' ? 'Now' : 'Then'}
      </div>
      <div className="text-[12px] text-kb-text-primary">{label}</div>
    </li>
  )
}
