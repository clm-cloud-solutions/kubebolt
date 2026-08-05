import { memo } from 'react'
import type { NodeProps } from 'reactflow'

interface NamespaceData {
  namespace: string
  nodeCount: number
  color: { border: string; bg: string; text: string }
  width: number
  height: number
  // Focus mode (injected by ClusterMap): true when no child of this
  // namespace is in the focused selection's neighbourhood.
  dimmed?: boolean
  // Progressive disclosure: when true the region renders as a compact
  // super-node (name + count + chevron) with no children painted. Clicking
  // it anywhere expands it back to its full contents.
  collapsed?: boolean
}

// A caret that points right when collapsed, down when expanded — the
// universal "click to expand / collapse" affordance.
function Chevron({ collapsed, color }: { collapsed: boolean; color: string }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 10 10"
      style={{
        color,
        transform: collapsed ? 'rotate(0deg)' : 'rotate(90deg)',
        transition: 'transform 0.18s ease',
        flexShrink: 0,
      }}
      aria-hidden
    >
      <path d="M3 1.5 L7 5 L3 8.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

function NamespaceRegionComponent({ data }: NodeProps<NamespaceData>) {
  const collapsed = data.collapsed === true

  const countBadge = (
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
        whiteSpace: 'nowrap',
        flexShrink: 0,
      }}
    >
      {data.nodeCount} {data.nodeCount === 1 ? 'resource' : 'resources'}
    </span>
  )

  // Collapsed: the ENTIRE compact card is the drag/click surface, so a
  // click anywhere expands it (onNodeClick toggles) while dragging still
  // reorders. cursor: pointer signals the click affordance over grab.
  if (collapsed) {
    return (
      <div
        style={{
          width: data.width,
          height: data.height,
          background: data.color.bg,
          border: `1px solid ${data.color.border}`,
          borderRadius: 14,
          opacity: data.dimmed ? 0.18 : 1,
          transition: 'opacity 0.2s ease',
        }}
      >
        <div
          className="ns-drag-handle"
          style={{
            position: 'absolute',
            inset: 0,
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '0 14px',
            pointerEvents: 'auto',
            cursor: 'pointer',
            userSelect: 'none',
          }}
          title="Click to expand · drag to reorder"
        >
          <Chevron collapsed color={data.color.text} />
          <span
            style={{
              flex: 1,
              minWidth: 0,
              fontSize: 13,
              fontWeight: 600,
              color: data.color.text,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {data.namespace}
          </span>
          {countBadge}
        </div>
      </div>
    )
  }

  return (
    <div
      style={{
        width: data.width,
        height: data.height,
        background: data.color.bg,
        border: `1px solid ${data.color.border}`,
        borderRadius: 18,
        pointerEvents: 'none',
        opacity: data.dimmed ? 0.18 : 1,
        transition: 'opacity 0.2s ease',
      }}
    >
      {/* The header is the drag handle. Rest of the region has
          pointer-events: none so edges inside still surface their
          hover tooltips. `nodrag` — actually the opposite — this
          element *is* the drag surface; class name matches the
          `dragHandle` selector set on the region node in
          ClusterMap.tsx so only clicking the label reorders the
          namespace (clicking empty space inside still passes through
          to the edges below). A single click on the header also
          collapses the region (onNodeClick toggles). */}
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
          cursor: 'pointer',
          userSelect: 'none',
        }}
        title="Click to collapse · drag to reorder"
      >
        <Chevron collapsed={false} color={data.color.text} />
        <span style={{ fontSize: 13, fontWeight: 600, color: data.color.text }}>
          {data.namespace}
        </span>
        {countBadge}
      </div>
    </div>
  )
}

export const NamespaceRegion = memo(NamespaceRegionComponent)
