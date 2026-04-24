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

// Map an L7 status-class breakdown (request/sec per class) to a single
// edge color. Returns undefined when there's no L7 data to decide on,
// letting the caller fall back to verdict-based coloring.
function l7ColorFor(statusClass?: Record<string, number>): string | undefined {
  if (!statusClass) return undefined
  const total = Object.values(statusClass).reduce((s, v) => s + (v || 0), 0)
  if (total <= 0) return undefined
  if ((statusClass.server_err ?? 0) > 0) return '#f43f5e'   // red
  if ((statusClass.client_err ?? 0) > 0) return '#f59e0b'   // amber
  if ((statusClass.redir ?? 0) > 0 && (statusClass.ok ?? 0) === 0) return '#a78bfa' // violet
  return '#10b981'  // emerald (all ok / info)
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
  // When L7 HTTP data is present on a forwarded edge, status class
  // overrides the green: any 5xx → red, any 4xx-only → amber, 3xx-only
  // → violet. Gives the cluster map an at-a-glance HTTP health read
  // without needing hover.
  let cfg: EdgeStyle
  if (edgeType === 'traffic') {
    const l7 = data?.l7 as
      | { requestsPerSec?: number; statusClass?: Record<string, number>; avgLatencyMs?: number }
      | undefined
    // Drive width / particle count / speed from HTTP req/sec when L7
    // visibility is enabled — it's a much better proxy for actual
    // activity than TCP event rate (one Hubble event per connection
    // covers many HTTP/1.1 keep-alive requests or HTTP/2 multiplexed
    // streams). Falls back to event rate when no L7 data exists so
    // plain TCP / non-HTTP edges still animate.
    const verdict = String(data?.verdict ?? 'forwarded').toLowerCase()
    const isForwarded = verdict === 'forwarded'
    // Visual scale is relative to the busiest edge on the map
    // (ClusterMap injects `relativeRate` in [0, 1]). Using absolute
    // rates broke for high-RPS infra — 50 rps and 5000 rps both
    // maxed out the log curve and looked identical. Relative keeps
    // the busiest edge pinned at peak visual intensity regardless of
    // traffic volume, and everything else reads against it.
    // Fallback to a modest slice of the scale when relativeRate
    // isn't present (e.g., single edge on the map, no peer to
    // compare against).
    const rel = Math.min(1, Math.max(0, Number(data?.relativeRate ?? 0.3)))
    const width = 1 + rel * 3                                 // 1px → 4px
    const particles = rel < 0.1 ? 1 : rel < 0.4 ? 2 : rel < 0.75 ? 3 : 4
    const speed = Math.max(0.4, 2.4 - rel * 2.0)              // 2.4s → 0.4s cycle
    const stroke = isForwarded ? l7ColorFor(l7?.statusClass) ?? '#10b981' : '#f43f5e'
    cfg = {
      stroke,
      width,
      dashed: false,
      particles,
      particleSize: 2,
      speed,
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
      {/* Wide invisible interaction path. ReactFlow attaches pointer
          handlers to this <g>; a thin visible stroke (1-4px) makes
          hover fragile because a pixel of movement leaves the edge
          DOM entirely, which would close the tooltip on every jitter.
          The 20px transparent path gives a reliable hover target
          without changing how the edge looks. */}
      <path
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        className="react-flow__edge-interaction"
      />

      {/* Glow effect for traffic edges */}
      {cfg.glow && (
        <path
          d={edgePath}
          fill="none"
          stroke={hasError ? '#ef4056' : cfg.stroke}
          strokeWidth={cfg.width * 3}
          opacity={0.06}
          pointerEvents="none"
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
        pointerEvents="none"
      />

      {/* Error pulse overlay — only when animations are on */}
      {showErrorPulse && (
        <path
          d={edgePath}
          fill="none"
          stroke="#ef4056"
          strokeWidth={cfg.width * 2}
          opacity={0.15}
          pointerEvents="none"
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
          pointerEvents="none"
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
