import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Wrench } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// ResourceActionsMenu — overflow dropdown for resource toolbar actions.
//
// Why this exists: the Tier 1 + Tier 2 build-out grew the resource
// detail toolbar to 8-9 buttons on workloads (Restart, Scale, Set
// image, Set resources, Set env, Pause/Resume rollout, Edit metadata,
// Delete...). The toolbar visually drowns the operator. This menu
// keeps the most-used actions inline and groups the rest under a
// single "Actions ▾" button.
//
// Design choices:
//
//   - PORTAL-RENDERED. The trigger sits in the toolbar (which lives
//     inside an overflow-aware page layout); rendering the popover
//     in document.body avoids ancestor stacking-context bleed.
//   - POSITIONED BY getBoundingClientRect on the trigger. We anchor
//     to the bottom-right of the trigger and grow leftward, since
//     the menu sits at the right edge of the toolbar where there's
//     less room to grow rightward.
//   - CLICK-OUTSIDE + ESC close the popover. Both are wired here so
//     callers don't need to plumb anything.
//   - DISABLED items render dimmed and don't fire onClick. Callers
//     pass a `disabled` flag on the item plus an optional `hint`
//     that becomes a tooltip explaining why.
//   - Item `variant` drives accent color: `default` is neutral
//     (kb-text-secondary), `success` is green (status-ok, used for
//     Resume), `warning` is yellow (status-warn, used for sensitive
//     ops like Reveal), `danger` is red (status-error — but Delete
//     stays out of this menu by deliberate design).
//
// The trigger button mirrors the look of the other toolbar buttons
// (px-3 py-1.5 text-xs, kb-card surface, hover state). When the
// popover is open, the trigger gets the active-style ring so the
// operator can tell at a glance the menu is open.

export type ActionVariant = 'default' | 'success' | 'warning' | 'danger'

export interface ActionItem {
  id: string
  label: string
  icon: LucideIcon
  onClick: () => void
  disabled?: boolean
  // Tooltip on hover. Especially useful for disabled items where the
  // operator wants to know WHY (eg "Editor role required").
  hint?: string
  variant?: ActionVariant
  // When set, renders a thin separator above this item — useful to
  // group destructive or sensitive items at the bottom of the menu.
  separator?: boolean
}

interface Props {
  items: ActionItem[]
  // Optional override for the trigger label. Default "Actions".
  label?: string
  // Disables the trigger entirely (eg. when the operator lacks the
  // role required to perform any of the menu's actions).
  disabled?: boolean
  // Tooltip on the disabled trigger.
  disabledHint?: string
}

export function ResourceActionsMenu({ items, label = 'Actions', disabled, disabledHint }: Props) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const popoverRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null)

  // Position the popover under-and-aligned-right relative to the
  // trigger. useLayoutEffect runs synchronously after layout so the
  // first render has the right coordinates — without this we'd see a
  // single-frame flash at (0,0) before the popover lands.
  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return
    const update = () => {
      const rect = triggerRef.current!.getBoundingClientRect()
      setPos({
        top: rect.bottom + 4,
        right: window.innerWidth - rect.right,
      })
    }
    update()
    // Resize / scroll keep the popover anchored if the user resizes
    // or scrolls an ancestor.
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
    }
  }, [open])

  // Click outside / Esc close. The trigger and popover are both
  // tracked so a click on the trigger doesn't double-toggle (the
  // trigger's onClick already toggles state; the outside-click
  // handler should ignore clicks inside either).
  useEffect(() => {
    if (!open) return
    function onDocClick(e: MouseEvent) {
      const t = e.target as Node
      if (triggerRef.current?.contains(t)) return
      if (popoverRef.current?.contains(t)) return
      setOpen(false)
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (items.length === 0) {
    // Nothing to show — render no trigger at all so the toolbar stays
    // tight. Callers don't need to gate at the call site.
    return null
  }

  const triggerClass = open
    ? 'bg-kb-card-hover border-kb-border-active text-kb-text-primary'
    : 'bg-kb-card border-kb-border text-kb-text-secondary hover:bg-kb-card-hover'

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        onClick={() => !disabled && setOpen((v) => !v)}
        disabled={disabled}
        title={disabled ? disabledHint : undefined}
        className={`px-3 py-1.5 text-xs border rounded-lg transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed ${triggerClass}`}
      >
        {/* Wrench — operational glyph. Reads as "things you can DO
            with this resource", matching KubeBolt's product framing
            (it's an ops tool, not a settings panel). Distinct from
            the kebab/ellipsis "more" pattern, which would read as
            generic overflow rather than action-specific. */}
        <Wrench className="w-3 h-3" />
        {label}
        <ChevronDown className={`w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && pos && createPortal(
        <div
          ref={popoverRef}
          className="fixed z-[9999] min-w-[200px] bg-kb-card border border-kb-border rounded-lg shadow-2xl py-1 overflow-hidden"
          style={{ top: pos.top, right: pos.right }}
          role="menu"
        >
          {items.map((item, i) => (
            <MenuRow
              key={item.id}
              item={item}
              showSeparator={item.separator && i > 0}
              onSelect={() => {
                if (item.disabled) return
                setOpen(false)
                item.onClick()
              }}
            />
          ))}
        </div>,
        document.body,
      )}
    </>
  )
}

function MenuRow({
  item,
  showSeparator,
  onSelect,
}: {
  item: ActionItem
  showSeparator?: boolean
  onSelect: () => void
}) {
  const Icon = item.icon
  const variantClass = variantClassFor(item.variant)
  return (
    <>
      {showSeparator && <div className="my-1 border-t border-kb-border" />}
      <button
        type="button"
        role="menuitem"
        onClick={onSelect}
        disabled={item.disabled}
        title={item.hint}
        className={`w-full flex items-center gap-2 px-3 py-1.5 text-xs text-left transition-colors disabled:opacity-40 disabled:cursor-not-allowed ${variantClass}`}
      >
        <Icon className="w-3.5 h-3.5 shrink-0" />
        <span className="flex-1">{item.label}</span>
      </button>
    </>
  )
}

function variantClassFor(v: ActionVariant | undefined): string {
  switch (v) {
    case 'success':
      return 'text-status-ok hover:bg-status-ok-dim'
    case 'warning':
      return 'text-status-warn hover:bg-status-warn-dim'
    case 'danger':
      return 'text-status-error hover:bg-status-error-dim'
    default:
      return 'text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary'
  }
}

// Re-export ReactNode just so callers don't need a separate import
// for the rare case where they want to compose menu items with
// dynamic JSX. Currently unused but cheap to expose.
export type { ReactNode }
