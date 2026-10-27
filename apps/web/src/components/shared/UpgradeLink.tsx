interface UpgradeLinkProps {
  /** The install's contact target — a mailto: or https: address. */
  url: string
  /**
   * The plan to move to. Only the multi-org edition sells plans; this one
   * never names a plan to buy, so the link always falls back to `url`.
   */
  plan?: string | null
  className?: string
  label?: string
}

// UpgradeLink — the CTA on a plan gate: the install's contact address, opened
// in a new tab when it is a web page. Renders nothing without one, so callers
// can pass it unconditionally.
export function UpgradeLink({ url, className, label }: UpgradeLinkProps) {
  const cls = className ?? 'font-medium underline hover:no-underline'
  if (!url) return null
  const external = /^https?:\/\//i.test(url)
  return (
    <a
      href={url}
      target={external ? '_blank' : undefined}
      rel={external ? 'noopener noreferrer' : undefined}
      className={cls}
    >
      {label ?? 'Contact us to upgrade'}
    </a>
  )
}
