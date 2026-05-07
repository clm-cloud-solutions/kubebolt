import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Lock, Unlock, Loader2 } from 'lucide-react'
import { useNodeSchedulability } from '@/hooks/useNodeSchedulability'
import { MutationErrorToast } from '@/components/shared/MutationErrorToast'
import type { ResourceItem } from '@/types/kubernetes'

// NodeSchedulabilityToolbarButton — toolbar variant of the
// cordon/uncordon action. Twin of NodeActionMenu (the three-dot
// popover on the Nodes list cards): same mutation logic via the
// shared useNodeSchedulability hook, different visual chrome to
// fit the resource detail page's toolbar.
//
// Click flow: button → portalled confirmation popover anchored
// below the trigger → confirm → mutation. Uses a portal because
// the detail page's toolbar lives inside flex containers that
// would otherwise clip the popover.

interface Props {
  node: ResourceItem
  // Whether the operator has Editor+ role. Disabled with tooltip
  // explaining why if false. The detail page already computes
  // this; we accept it as a prop rather than re-deriving via
  // useAuth so the same role check is shared with sibling toolbar
  // buttons (Restart / Delete / etc.).
  canEdit: boolean
}

export function NodeSchedulabilityToolbarButton({ node, canEdit }: Props) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const popoverRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  const schedulability = useNodeSchedulability(node)

  const unschedulable = (node as unknown as { unschedulable?: boolean }).unschedulable === true
  const target = !unschedulable // clicking when active → cordon; when cordoned → uncordon
  const action = target ? 'Cordon' : 'Uncordon'
  const Icon = target ? Lock : Unlock

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return
    const update = () => {
      const r = triggerRef.current!.getBoundingClientRect()
      // Anchor the popover BELOW-RIGHT of the button. If it would
      // overflow the right edge of the viewport, flip to the left.
      const popoverWidth = 280
      const overflowsRight = r.right + popoverWidth > window.innerWidth - 8
      const left = overflowsRight ? r.right - popoverWidth : r.left
      setPos({ top: r.bottom + 4, left })
    }
    update()
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      const target = e.target as Node
      if (triggerRef.current?.contains(target)) return
      if (popoverRef.current?.contains(target)) return
      setOpen(false)
    }
    function handleKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('mousedown', handleClick)
      document.removeEventListener('keydown', handleKey)
    }
  }, [open])

  async function confirm() {
    await schedulability.run(target)
    setOpen(false)
  }

  return (
    <>
      <button
        ref={triggerRef}
        onClick={() => setOpen((v) => !v)}
        disabled={!canEdit}
        title={
          !canEdit
            ? 'Editor role required'
            : target
            ? 'Mark unschedulable — no new pods will be placed here'
            : 'Mark schedulable — scheduler resumes placing pods here'
        }
        className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <Icon className={`w-3 h-3 ${schedulability.busy ? 'animate-pulse' : ''}`} />
        {action}
      </button>
      {open && pos &&
        createPortal(
          <div
            ref={popoverRef}
            className="fixed bg-kb-card border border-kb-border rounded-lg shadow-xl overflow-hidden"
            style={{ zIndex: 100000, top: pos.top, left: pos.left, width: 280 }}
          >
            {schedulability.busy ? (
              <div className="px-3 py-4 flex items-center gap-2 text-[12px] text-kb-text-secondary">
                <Loader2 className="w-3.5 h-3.5 animate-spin text-status-info" />
                Submitting…
              </div>
            ) : (
              <div className="text-[12px]">
                <div className="px-3 py-3 space-y-2">
                  <div className="text-[12px] font-semibold text-kb-text-primary">
                    {target ? 'Cordon node?' : 'Uncordon node?'}
                  </div>
                  <div className="text-[11px] leading-relaxed">
                    <span className="font-mono text-kb-text-primary">{node.name}</span>
                    <span className="text-kb-text-secondary">
                      {target
                        ? ' will be marked unschedulable. New pods will not be placed here, but existing pods continue running.'
                        : ' will be marked schedulable. The scheduler will resume placing new pods here.'}
                    </span>
                  </div>
                </div>
                <div className="px-3 pb-3 flex justify-end gap-2">
                  <button
                    onClick={() => setOpen(false)}
                    className="px-2.5 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={confirm}
                    className={`px-2.5 py-1 text-[11px] rounded border inline-flex items-center gap-1 font-medium hover:opacity-90 ${
                      target
                        ? 'bg-status-warn text-kb-bg border-status-warn'
                        : 'bg-status-ok text-kb-bg border-status-ok'
                    }`}
                  >
                    <Icon className="w-3 h-3" />
                    {action}
                  </button>
                </div>
              </div>
            )}
          </div>,
          document.body,
        )}
      {schedulability.error && (
        <MutationErrorToast
          error={schedulability.error.err}
          action={schedulability.error.action}
          onDismiss={schedulability.clearError}
        />
      )}
    </>
  )
}
