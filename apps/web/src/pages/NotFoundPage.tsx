import { Link, useLocation, useNavigate } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'
import { KubeBoltLogo } from '@/components/shared/KubeBoltLogo'

// NotFoundPage — what an address that leads nowhere shows. Without it React
// Router matched no route and the screen stayed blank, chrome included.
//
// Mounted as the last route of the authenticated layout (path="*"), so the
// sidebar and the topbar stay and offer a way out, and by the resource detail
// for a resource type that does not exist. Its altitude is global
// (routeScope in utils/scope.ts): it reads nothing from the cluster, so a dead
// cluster never replaces it with "Cluster unreachable".
//
// The kubebolt.io 404 in the product's voice: the code, one sentence, the path
// that was tried (without the query string) and the way back.
export function NotFoundPage() {
  const { pathname, key } = useLocation()
  const navigate = useNavigate()
  // "default" is the entry this tab opened the app on: going back from there
  // would leave it.
  const canGoBack = key !== 'default'

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden px-4 py-16">
      {/* The Split Bolt as a watermark, as on the site: flat fill, no tilt;
          the element's opacity makes it a watermark. */}
      <KubeBoltLogo className="pointer-events-none absolute -bottom-16 -right-16 h-[26rem] w-[26rem] text-kb-accent opacity-[0.05]" />

      <div className="relative w-full max-w-lg text-center">
        <p
          aria-hidden
          className="select-none font-mono text-[5.5rem] font-semibold leading-none tracking-[-0.04em] text-kb-border-active sm:text-[7rem]"
        >
          404
        </p>
        <h1 className="mt-4 text-xl font-semibold text-kb-text-primary">This page doesn't exist</h1>
        <p className="mt-2 text-sm text-kb-text-secondary">The address may have a typo, or the page has moved.</p>

        <div className="mt-5 inline-flex max-w-full items-center gap-2 rounded-md border border-kb-border bg-kb-elevated px-3 py-1.5">
          <span className="shrink-0 font-mono text-[10px] uppercase tracking-[0.06em] text-kb-text-tertiary">You tried</span>
          <code translate="no" className="truncate font-mono text-xs text-kb-text-primary">
            {pathname}
          </code>
        </div>

        <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
          <Link
            to="/home"
            className="inline-flex items-center gap-1.5 rounded-md bg-kb-accent px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-kb-accent-bright"
          >
            Go to Home
            <ArrowRight className="h-3.5 w-3.5" />
          </Link>
          {canGoBack && (
            <button
              type="button"
              onClick={() => navigate(-1)}
              className="rounded-md border border-kb-border bg-kb-elevated px-3 py-1.5 text-xs font-medium text-kb-text-primary transition-colors hover:border-kb-border-active"
            >
              Go back
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
