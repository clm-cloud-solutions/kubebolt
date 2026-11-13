import type { MouseEvent } from 'react'
import { Link, type LinkProps } from 'react-router-dom'
import { useOpenInCluster } from '@/hooks/useOpenInCluster'
import { parseClusterDisplayName } from '@/utils/cluster'

// ClusterLink — a link from a global page into one cluster's page (finding
// #81). When the cluster it names is not the active one, a plain click switches
// to it first, then opens the page. It stays a real <a>: a modified click
// (new tab, new window) and a link to the active cluster behave as any link.
export function ClusterLink({
  cluster,
  to,
  title,
  ...rest
}: { cluster?: string; to: string } & Omit<LinkProps, 'to' | 'onClick'>) {
  const { targetFor, open } = useOpenInCluster()
  const target = targetFor(cluster)
  const onClick = (e: MouseEvent<HTMLAnchorElement>) => {
    if (!target || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    void open(cluster, to)
  }
  return (
    <Link
      to={to}
      onClick={onClick}
      title={target ? `Switches to ${parseClusterDisplayName(target)}` : title}
      {...rest}
    />
  )
}
