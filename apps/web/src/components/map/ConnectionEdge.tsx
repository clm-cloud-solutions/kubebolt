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
  // Traffic flow (Service → Pod) — green, fast flowing dots
  selects:    { stroke: '#1DBD7D', width: 2.0, dashed: false, particles: 3, particleSize: 2.5, speed: 2,   glow: true },
  // Traffic routing (Ingress/Gateway → Service) — purple, medium dots
  routes:     { stroke: '#a78bfa', width: 1.8, dashed: false, particles: 2, particleSize: 2,   speed: 2.5, glow: true },
  // Ownership (Deployment → ReplicaSet → Pod) — subtle gray
  owns:       { stroke: 'rgba(255,255,255,0.10)', width: 0.8, dashed: false, particles: 0, particleSize: 0, speed: 0, glow: false },
  // Config references — amber dashed
  mounts:     { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 0, particleSize: 0, speed: 0, glow: false },
  envFrom:    { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 0, particleSize: 0, speed: 0, glow: false },
  imagePull:  { stroke: '#f5a623', width: 0.8, dashed: true,  particles: 0, particleSize: 0, speed: 0, glow: false },
  // Volume usage — cyan solid
  uses:       { stroke: '#22d3ee', width: 1.0, dashed: false, particles: 0, particleSize: 0, speed: 0, glow: false },
  // Storage binding — cyan with slow dot
  bound:      { stroke: '#22d3ee', width: 1.0, dashed: false, particles: 1, particleSize: 1.5, speed: 4, glow: false },
  // Autoscaling — purple dashed
  hpa:        { stroke: '#a78bfa', width: 0.8, dashed: true,  particles: 0, particleSize: 0, speed: 0, glow: false },
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
  const cfg = EDGE_STYLES[edgeType] || DEFAULT_STYLE

  const sourceStatus = ((data?.sourceStatus as string) || '').toLowerCase()
  const targetStatus = ((data?.targetStatus as string) || '').toLowerCase()
  const hasError = ERROR_STATUSES.has(sourceStatus) || ERROR_STATUSES.has(targetStatus)

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

      {/* Error pulse overlay */}
      {hasError && cfg.particles > 0 && (
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

      {/* Flowing particles */}
      {cfg.particles > 0 && Array.from({ length: cfg.particles }).map((_, i) => (
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
            begin={`${(i / cfg.particles) * cfg.speed}s`}
          />
        </circle>
      ))}
    </g>
  )
}

export const ConnectionEdge = memo(ConnectionEdgeComponent)
