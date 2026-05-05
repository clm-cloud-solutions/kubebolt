import { Github, Globe, Linkedin, Heart, ExternalLink } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'
import { Modal } from '@/components/shared/Modal'
import { VERSION } from '@/version'

// About modal — project identity + creator credits. Opened from
// the sidebar's "About" link. Kept lightweight on purpose: the
// sponsor attribution is prominent but the rest (description,
// license, repo) is meta-info for users curious about provenance.
interface Props {
  onClose: () => void
}

export function AboutModal({ onClose }: Props) {
  return (
    <Modal badge="About" title={`KubeBolt v${VERSION}`} onClose={onClose} size="sm" unbounded>
      <div className="p-6 space-y-5">
        {/* Hero */}
        <div className="flex items-start gap-3">
          <div className="w-12 h-12 rounded-xl bg-kb-accent-light flex items-center justify-center shrink-0">
            <KubeBoltLogo className="w-6 h-6 text-kb-accent" />
          </div>
          <div className="min-w-0">
            <div className="text-base font-semibold text-kb-text-primary">KubeBolt</div>
            <div className="text-[11px] text-kb-text-secondary leading-relaxed mt-1">
              Instant Kubernetes monitoring — full cluster visibility in
              under 2 minutes, with zero configuration and Kobi, the
              built-in agent.
            </div>
          </div>
        </div>

        {/* Creator attribution */}
        <div className="pt-4 border-t border-kb-border">
          <div className="flex items-center gap-1.5 text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-2">
            Built with
            <Heart className="w-3 h-3 text-status-error fill-status-error" />
            by
          </div>

          <div className="rounded-lg border border-kb-border bg-kb-elevated p-4 space-y-3">
            <div>
              <a
                href="https://clmcloudsolutions.es/"
                target="_blank"
                rel="noreferrer"
                className="text-sm font-semibold text-kb-text-primary hover:text-kb-accent transition-colors inline-flex items-center gap-1.5"
              >
                CLM Cloud Solutions
                <ExternalLink className="w-3 h-3" />
              </a>
              <div className="text-[11px] text-kb-text-secondary mt-1 leading-relaxed">
                Intelligent monitoring and DevSecOps platform powered by AI —
                serving LATAM and Spain.
              </div>
            </div>

            <div className="flex items-center gap-2 pt-1">
              <LinkPill href="https://clmcloudsolutions.es/" icon={<Globe className="w-3 h-3" />} label="Website" />
              <LinkPill href="https://github.com/clm-cloud-solutions" icon={<Github className="w-3 h-3" />} label="GitHub" />
              <LinkPill href="https://linkedin.com/company/clm-cloud-solutions" icon={<Linkedin className="w-3 h-3" />} label="LinkedIn" />
            </div>
          </div>
        </div>

        {/* Project links */}
        <div className="pt-4 border-t border-kb-border space-y-2">
          <ProjectLink
            label="Source code"
            href="https://github.com/clm-cloud-solutions/kubebolt"
            value="github.com/clm-cloud-solutions/kubebolt"
          />
          <ProjectLink
            label="Issues & feedback"
            href="https://github.com/clm-cloud-solutions/kubebolt/issues"
            value="Open an issue on GitHub"
          />
          <div className="flex items-start justify-between gap-3 text-[11px]">
            <span className="text-kb-text-tertiary font-mono uppercase tracking-[0.08em]">License</span>
            <span className="text-kb-text-primary">Apache 2.0</span>
          </div>
        </div>

        <div className="text-[10px] text-kb-text-tertiary text-center font-mono pt-2">
          © 2026 CLM Cloud Solutions S.L.
        </div>
      </div>
    </Modal>
  )
}

function LinkPill({ href, icon, label }: { href: string; icon: React.ReactNode; label: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-kb-card border border-kb-border text-[11px] text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors"
    >
      {icon}
      {label}
    </a>
  )
}

function ProjectLink({ label, href, value }: { label: string; href: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-3 text-[11px]">
      <span className="text-kb-text-tertiary font-mono uppercase tracking-[0.08em] shrink-0">{label}</span>
      <a
        href={href}
        target="_blank"
        rel="noreferrer"
        className="text-kb-accent hover:underline inline-flex items-center gap-1 text-right min-w-0"
      >
        <span className="truncate">{value}</span>
        <ExternalLink className="w-3 h-3 shrink-0" />
      </a>
    </div>
  )
}
