import { memo } from 'react'
import { getBezierPath, type EdgeProps } from 'reactflow'

const EDGE_STYLES: Record<string, { stroke: string; width: number; dashed: boolean; animated: boolean }> = {
  'svc-pod':     { stroke: '#4c9aff', width: 1.6, dashed: false, animated: true },
  'svc-deploy':  { stroke: '#4c9aff', width: 1.6, dashed: false, animated: true },
  'ing-svc':     { stroke: '#a78bfa', width: 1.4, dashed: false, animated: true },
  config:        { stroke: '#f5a623', width: 0.9, dashed: true,  animated: false },
  configmap:     { stroke: '#f5a623', width: 0.9, dashed: true,  animated: false },
  secret:        { stroke: '#f5a623', width: 0.9, dashed: true,  animated: false },
  hpa:           { stroke: '#22d68a', width: 0.9, dashed: true,  animated: false },
  pvc:           { stroke: '#22d3ee', width: 1.1, dashed: false, animated: false },
  storage:       { stroke: '#22d3ee', width: 1.1, dashed: false, animated: false },
  owner:         { stroke: 'rgba(255,255,255,0.12)', width: 1, dashed: false, animated: false },
}

const DEFAULT_STYLE = { stroke: 'rgba(255,255,255,0.08)', width: 1.2, dashed: false, animated: false }

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
      <path
        id={id}
        d={edgePath}
        fill="none"
        stroke={cfg.stroke}
        strokeWidth={cfg.width}
        strokeDasharray={cfg.dashed ? '6 4' : undefined}
        opacity={0.5}
      />
      {(cfg.animated || data?.animated) && (
        <circle r="2" fill={cfg.stroke}>
          <animateMotion dur="2.5s" repeatCount="indefinite" path={edgePath} />
        </circle>
      )}
    </g>
  )
}

export const ConnectionEdge = memo(ConnectionEdgeComponent)
