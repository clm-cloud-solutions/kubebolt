import { getStatusBgColor } from '@/utils/colors'

interface StatusBadgeProps {
  status: string
  label?: string
  size?: 'sm' | 'md'
}

export function StatusBadge({ status, label, size = 'sm' }: StatusBadgeProps) {
  const colorClass = getStatusBgColor(status)
  const sizeClass = size === 'sm' ? 'px-2 py-0.5 text-[10px]' : 'px-2.5 py-1 text-xs'

  return (
    <span
      className={`inline-flex items-center font-mono uppercase tracking-[0.06em] rounded-full ${colorClass} ${sizeClass}`}
    >
      {label ?? status}
    </span>
  )
}
