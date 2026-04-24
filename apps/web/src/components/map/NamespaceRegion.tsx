import { memo } from 'react'
import type { NodeProps } from 'reactflow'

interface NamespaceData {
  namespace: string
  nodeCount: number
  color: { border: string; bg: string; text: string }
  width: number
  height: number
}

function NamespaceRegionComponent({ data }: NodeProps<NamespaceData>) {
  return (
    <div
      style={{
        width: data.width,
        height: data.height,
        background: data.color.bg,
        border: `1px solid ${data.color.border}`,
        borderRadius: 18,
        pointerEvents: 'none',
      }}
    >
      {/* The header is the drag handle. Rest of the region has
          pointer-events: none so edges inside still surface their
          hover tooltips. `nodrag` — actually the opposite — this
          element *is* the drag surface; class name matches the
          `dragHandle` selector set on the region node in
          ClusterMap.tsx so only clicking the label reorders the
          namespace (clicking empty space inside still passes through
          to the edges below). */}
      <div
        className="ns-drag-handle"
        style={{
          position: 'absolute',
          top: 12,
          left: 16,
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          pointerEvents: 'auto',
          cursor: 'grab',
          userSelect: 'none',
        }}
        title="Drag to reorder this namespace"
      >
        <span style={{ fontSize: 13, fontWeight: 600, color: data.color.text }}>
          {data.namespace}
        </span>
        <span
          style={{
            fontSize: 9,
            fontFamily: "'JetBrains Mono', monospace",
            padding: '2px 7px',
            borderRadius: 4,
            background: data.color.bg,
            border: `1px solid ${data.color.border}`,
            color: data.color.text,
            textTransform: 'uppercase',
            letterSpacing: '0.04em',
          }}
        >
          {data.nodeCount} resources
        </span>
      </div>
    </div>
  )
}

export const NamespaceRegion = memo(NamespaceRegionComponent)
