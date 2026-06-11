// DonutGauge — shared SVG ring gauge. Same visual primitive as the
// Monitor tab donuts (ResourceDetailPage) but parameterized so the
// Overview KPI cards and usage panels can render compact variants.
// `children` is centered inside the ring (value, percent, etc.).
interface DonutGaugeProps {
  percent: number
  color: string
  size?: number
  strokeWidth?: number
  trackColor?: string
  children?: React.ReactNode
  className?: string
}

export function DonutGauge({
  percent,
  color,
  size = 64,
  strokeWidth = 6,
  trackColor = 'var(--kb-border)',
  children,
  className = '',
}: DonutGaugeProps) {
  const clamped = Math.max(0, Math.min(100, percent))
  const r = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * r
  const filled = (clamped / 100) * circumference

  return (
    <div className={`relative shrink-0 ${className}`} style={{ width: size, height: size }}>
      <svg className="-rotate-90" width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={trackColor}
          strokeWidth={strokeWidth}
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeDasharray={`${filled} ${circumference}`}
          strokeLinecap="round"
          className="transition-all duration-700"
        />
      </svg>
      {children && (
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          {children}
        </div>
      )}
    </div>
  )
}
