import type { ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useWorkloadPath } from '@/hooks/useWorkloadPath'

// The links a lit KPI card carries to its problem: the same quiet underline
// everywhere, so a name you can click reads as one.
const LINK = 'underline decoration-dotted underline-offset-2 hover:text-kb-text-primary transition-colors'

// AnchorLink — to a panel further down the same page (useScrollToHash scrolls).
export function AnchorLink({ id, children }: { id: string; children: ReactNode }) {
  const { pathname, search } = useLocation()
  return (
    <Link to={{ pathname, search, hash: id }} className={LINK}>
      {children}
    </Link>
  )
}

// WorkloadLink — to a workload known by name and namespace (useWorkloadPath).
export function WorkloadLink({ namespace, name, children }: { namespace: string; name: string; children?: ReactNode }) {
  const to = useWorkloadPath(namespace, name)
  return (
    <Link to={to} className={LINK} title={`${namespace}/${name}`}>
      {children ?? name}
    </Link>
  )
}
