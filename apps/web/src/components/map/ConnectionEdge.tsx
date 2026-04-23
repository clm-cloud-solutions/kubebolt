import { memo } from 'react'
import { getBezierPath, type EdgeProps } from 'reactflow'

interface EdgeStyle {
  stroke: string
  width: number
  dashed: boolean
  particles: number
  particleSize: number
  speed: number
  glow: boolean
}

const EDGE_STYLES: Record<string, EdgeStyle> = {
  // Traffic flow (Service → Pod) — green, fast flowing dots (loud, most prominent)
  selects:    { stroke: '#1DBD7D', width: 2.0, dashed: false, particles: 3, particleSize: 2.5, speed: 2,   glow: true },
  // Traffic routing (Ingress/Gateway → Service) — purple, medium dots
  routes:     { stroke: '#a78bfa', width: 1.8, dashed: false, particles: 2, particleSize: 2,   speed: 2.5, glow: true },
  // Ownership (Deployment → ReplicaSet → Pod) — slow, subtle particle to show liveness
  owns:       { stroke: 'rgba(255,255,255,0.18)', width: 0.8, dashed: false, particles: 1, particleSize: 1.2, speed: 5, glow: false },
  // Config references — amber dashed with a slow pulse
  mounts:     { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 1, particleSize: 1.2, speed: 5, glow: false },
  envFrom:    { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 1, particleSize: 1.2, speed: 5, glow: false },
  imagePull:  { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 0, particleSize: 0,   speed: 0, glow: false },
  // Volume usage — cyan with slow particle
  uses:       { stroke: '#22d3ee', width: 1.0, dashed: false, particles: 1, particleSize: 1.5, speed: 4, glow: false },
  // Storage binding — cyan with slow dot
  bound:      { stroke: '#22d3ee', width: 1.0, dashed: false, particles: 1, particleSize: 1.5, speed: 4, glow: false },
  // Autoscaling — purple dashed with slow particle
  hpa:        { stroke: '#a78bfa', width: 0.8, dashed: true,  particles: 1, particleSize: 1.2, speed: 5, glow: false },
}

const DEFAULT_STYLE: EdgeStyle = {
  stroke: 'rgba(255,255,255,0.08)', width: 1.0, dashed: false,
  particles: 0, particleSize: 0, speed: 0, glow: false,
}

const ERROR_STATUSES = new Set(['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted', 'oomkilled'])

function ConnectionEdgeComponent({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
}: EdgeProps) {
  const edgeType = (data?.edgeType as string) || ''

  // Traffic edges are a special case: width and particle count scale with
  // the observed rate, and color swings on verdict ("forwarded" = emerald,
  // anything else = rose) so drops pop without needing a separate path.
  let cfg: EdgeStyle
  if (edgeType === 'traffic') {
    const rate = Math.max(0, Number(data?.ratePerSec ?? 0))
    const verdict = String(data?.verdict ?? 'forwarded').toLowerCase()
    const isForwarded = verdict === 'forwarded'
    // log10 scaling: 1 rps -> 1.4px, 10 rps -> 2.2px, 100 rps -> 3px,
    // caps around 4px so a hot service doesn't draw a black bar.
    const width = Math.min(4, 1 + Math.log10(rate + 1) * 0.7)
    const particles = Math.min(4, Math.max(1, Math.round(Math.log10(rate + 1) + 1)))
    cfg = {
      stroke: isForwarded ? '#10b981' : '#f43f5e',
      width,
      dashed: false,
      particles,
      particleSize: 2,
      speed: Math.max(1.2, 3 - Math.log10(rate + 1) * 0.6),
      glow: true,
    }
  } else {
    cfg = EDGE_STYLES[edgeType] || DEFAULT_STYLE
  }

  const sourceStatus = ((data?.sourceStatus as string) || '').toLowerCase()
  const targetStatus = ((data?.targetStatus as string) || '').toLowerCase()
  const hasError = ERROR_STATUSES.has(sourceStatus) || ERROR_STATUSES.has(targetStatus)

  // When the user disables animations (or the OS reports prefers-reduced-motion),
  // we render edges without particles or pulse overlays. The data flag is set
  // from ClusterMap based on the animation toggle.
  const animationsEnabled = data?.animationsEnabled !== false
  const particleCount = animationsEnabled ? cfg.particles : 0
  const showErrorPulse = animationsEnabled && hasError && cfg.particles > 0

  const [edgePath] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  return (
    <g>
      {/* Glow effect for traffic edges */}
      {cfg.glow && (
        <path
          d={edgePath}
          fill="none"
          stroke={hasError ? '#ef4056' : cfg.stroke}
          strokeWidth={cfg.width * 3}
          opacity={0.06}
        />
      )}

      {/* Main edge line */}
      <path
        id={id}
        d={edgePath}
        fill="none"
        stroke={hasError && cfg.particles > 0 ? '#ef4056' : cfg.stroke}
        strokeWidth={cfg.width}
        strokeDasharray={cfg.dashed ? '6 4' : undefined}
        opacity={cfg.particles > 0 ? 0.6 : 0.4}
      />

      {/* Error pulse overlay — only when animations are on */}
      {showErrorPulse && (
        <path
          d={edgePath}
          fill="none"
          stroke="#ef4056"
          strokeWidth={cfg.width * 2}
          opacity={0.15}
        >
          <animate attributeName="opacity" values="0.15;0.05;0.15" dur="2s" repeatCount="indefinite" />
        </path>
      )}

      {/* Flowing particles — suppressed entirely when animations are disabled */}
      {particleCount > 0 && Array.from({ length: particleCount }).map((_, i) => (
        <circle
          key={i}
          r={cfg.particleSize}
          fill={hasError ? '#ef4056' : cfg.stroke}
          opacity={0.8}
        >
          <animateMotion
            dur={`${cfg.speed}s`}
            repeatCount="indefinite"
            path={edgePath}
            begin={`${(i / particleCount) * cfg.speed}s`}
          />
        </circle>
      ))}
    </g>
  )
}

export const ConnectionEdge = memo(ConnectionEdgeComponent)
