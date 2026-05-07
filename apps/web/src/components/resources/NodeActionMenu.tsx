import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { MoreVertical, Lock, Unlock, AlertCircle, ChevronLeft, Loader2 } from 'lucide-react'
import { useAuth } from '@/contexts/AuthContext'
import { useNodeSchedulability } from '@/hooks/useNodeSchedulability'
import { MutationErrorToast } from '@/components/shared/MutationErrorToast'
import type { ResourceItem } from '@/types/kubernetes'

// NodeActionMenu — three-dot popover anchored to a NodeCard. Lives
// inside Link-wrapped cards, so every interactive element here MUST
// stop event propagation to keep the card's navigation from firing.
// The popover itself is portalled to document.body for the same
// reason the SetImage suggestion dropdown is: the card has rounded
// borders + an ancestor with overflow effects, and the popover
// would clip if rendered as a child.
//
// Two-step interaction:
//   1. Click the three-dot trigger → menu of available actions.
//   2. Click an action (Cordon / Uncordon) → confirmation pane
//      INSIDE the same popover. [Cancel] returns to step 1;
//      [Confirm] executes.
//
// We chose an inline confirmation rather than a modal because it
// keeps the operator on the Nodes grid (no context switch), and
// because the actions are reversible (uncordon flips the state
// back). Drain stays in a separate full modal because it's
// destructive, takes minutes, and needs configuration.
//
// Feedback model:
//   - Success: optimistic flip of `unschedulable` in the resources
//     cache fires before the request, AND we explicitly refetch
//     the active list query to make sure the badge appears
//     immediately even if the optimistic prefix-match missed.
//   - Error: rolls back via refetchQueries and renders a
//     MutationErrorToast (auto-dismisses 6s).
//   - "Already in target state": silent — the optimistic flip was
//     a no-op against the existing state.

interface Props {
  node: ResourceItem
  onDrain?: (node: ResourceItem) => void
}

type PopoverPane = 'menu' | 'confirm-cordon' | 'confirm-uncordon' | 'busy'

export function NodeActionMenu({ node, onDrain }: Props) {
  const [open, setOpen] = useState(false)
  const [pane, setPane] = useState<PopoverPane>('menu')
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const popoverRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  // Mutation logic is shared with NodeSchedulabilityToolbarButton
  // (the detail-page-toolbar twin of this menu) via
  // useNodeSchedulability. Both components hand the same
  // optimistic-flip + cache-sync + rollback dance to the hook so
  // the cordon/uncordon UX stays consistent across surfaces.
  const schedulability = useNodeSchedulability(node)
  const { hasRole } = useAuth()
  const canEdit = hasRole('editor')
  const canAdmin = hasRole('admin')

  const unschedulable = (node as unknown as { unschedulable?: boolean }).unschedulable === true
  const nodeName = node.name

  // Reset pane to menu whenever the popover closes; otherwise re-
  // opening would land on the last seen pane (confirm/busy) which
  // would feel buggy.
  useEffect(() => {
    if (!open) setPane('menu')
  }, [open])

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return
    const update = () => {
      const r = triggerRef.current!.getBoundingClientRect()
      // Anchor the popover BELOW-RIGHT of the trigger. If it would
      // overflow the right edge of the viewport, flip to the left.
      const popoverWidth = 260
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

  // setSchedulability runs the actual mutation. The pane state
  // gates the call: by the time we get here, the operator already
  // confirmed in the second pane.
  async function setSchedulability(targetUnschedulable: boolean) {
    setPane('busy')
    await schedulability.run(targetUnschedulable)
    setOpen(false)
  }

  return (
    <>
      <button
        ref={triggerRef}
        onClick={(e) => {
          e.preventDefault()
          e.stopPropagation()
          setOpen((v) => !v)
        }}
        disabled={!canEdit}
        title={canEdit ? 'Node actions' : 'Editor role required'}
        className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <MoreVertical className="w-3.5 h-3.5" />
      </button>
      {open && pos &&
        createPortal(
          <div
            ref={popoverRef}
            className="fixed bg-kb-card border border-kb-border rounded-lg shadow-xl overflow-hidden"
            style={{ zIndex: 100000, top: pos.top, left: pos.left, width: 260 }}
            onClick={(e) => e.stopPropagation()}
            onMouseDown={(e) => e.stopPropagation()}
          >
            {pane === 'menu' && (
              <div className="py-1">
                {!unschedulable && (
                  <MenuItem
                    icon={<Lock className="w-3.5 h-3.5" />}
                    onClick={() => setPane('confirm-cordon')}
                  >
                    Cordon
                  </MenuItem>
                )}
                {unschedulable && (
                  <MenuItem
                    icon={<Unlock className="w-3.5 h-3.5" />}
                    onClick={() => setPane('confirm-uncordon')}
                  >
                    Uncordon
                  </MenuItem>
                )}
                <MenuItem
                  icon={<AlertCircle className="w-3.5 h-3.5" />}
                  onClick={() => {
                    setOpen(false)
                    if (onDrain) onDrain(node)
                  }}
                  disabled={!canAdmin || !onDrain}
                  disabledTitle={!canAdmin ? 'Admin role required' : 'Drain not yet wired (Cut 5)'}
                >
                  Drain…
                </MenuItem>
              </div>
            )}
            {pane === 'confirm-cordon' && (
              <ConfirmPane
                title="Cordon node?"
                body={
                  <>
                    <span className="font-mono text-kb-text-primary">{nodeName}</span>
                    <span className="text-kb-text-secondary">
                      {' '}will be marked unschedulable. New pods won't be placed here, but existing pods continue running.
                    </span>
                  </>
                }
                confirmLabel="Cordon"
                confirmIcon={<Lock className="w-3.5 h-3.5" />}
                accent="warn"
                onCancel={() => setPane('menu')}
                onConfirm={() => setSchedulability(true)}
              />
            )}
            {pane === 'confirm-uncordon' && (
              <ConfirmPane
                title="Uncordon node?"
                body={
                  <>
                    <span className="font-mono text-kb-text-primary">{nodeName}</span>
                    <span className="text-kb-text-secondary">
                      {' '}will be marked schedulable. The scheduler will resume placing new pods here.
                    </span>
                  </>
                }
                confirmLabel="Uncordon"
                confirmIcon={<Unlock className="w-3.5 h-3.5" />}
                accent="ok"
                onCancel={() => setPane('menu')}
                onConfirm={() => setSchedulability(false)}
              />
            )}
            {pane === 'busy' && (
              <div className="px-3 py-4 flex items-center gap-2 text-[12px] text-kb-text-secondary">
                <Loader2 className="w-3.5 h-3.5 animate-spin text-status-info" />
                Submitting…
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

// ConfirmPane is the inline-confirmation step that replaces the
// menu pane after the operator picks Cordon/Uncordon. Same width,
// same chrome — feels like a sub-page of the popover, not a modal.
function ConfirmPane({
  title,
  body,
  confirmLabel,
  confirmIcon,
  accent,
  onCancel,
  onConfirm,
}: {
  title: string
  body: React.ReactNode
  confirmLabel: string
  confirmIcon: React.ReactNode
  accent: 'warn' | 'ok'
  onCancel: () => void
  onConfirm: () => void
}) {
  const accentClasses =
    accent === 'warn'
      ? 'bg-status-warn text-kb-bg border-status-warn'
      : 'bg-status-ok text-kb-bg border-status-ok'
  return (
    <div className="text-[12px]">
      <button
        onClick={onCancel}
        className="w-full px-3 py-1.5 text-[10px] uppercase tracking-wider text-kb-text-tertiary hover:text-kb-text-primary border-b border-kb-border flex items-center gap-1"
      >
        <ChevronLeft className="w-3 h-3" />
        Back
      </button>
      <div className="px-3 py-3 space-y-2">
        <div className="text-[12px] font-semibold text-kb-text-primary">{title}</div>
        <div className="text-[11px] leading-relaxed">{body}</div>
      </div>
      <div className="px-3 pb-3 flex justify-end gap-2">
        <button
          onClick={onCancel}
          className="px-2.5 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
        >
          Cancel
        </button>
        <button
          onClick={onConfirm}
          className={`px-2.5 py-1 text-[11px] rounded border inline-flex items-center gap-1 font-medium hover:opacity-90 ${accentClasses}`}
        >
          {confirmIcon}
          {confirmLabel}
        </button>
      </div>
    </div>
  )
}

function MenuItem({
  icon,
  onClick,
  disabled,
  disabledTitle,
  children,
}: {
  icon: React.ReactNode
  onClick: () => void
  disabled?: boolean
  disabledTitle?: string
  children: React.ReactNode
}) {
  return (
    <button
      onClick={(e) => {
        e.stopPropagation()
        if (!disabled) onClick()
      }}
      disabled={disabled}
      title={disabled ? disabledTitle : undefined}
      className="w-full text-left px-3 py-1.5 text-[12px] text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors flex items-center gap-2 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-transparent"
    >
      <span className="text-kb-text-tertiary shrink-0">{icon}</span>
      {children}
    </button>
  )
}
