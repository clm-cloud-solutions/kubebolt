import type { ReactNode } from 'react'
import { Modal } from '@/components/shared/Modal'

// ConfirmDialog — generic "Are you sure?" modal used by the Settings
// tabs in place of the native browser confirm(), which doesn't match
// the rest of the app's chrome (and on mobile defaults to a tiny
// system toast).
//
// Variants:
//   - 'default': neutral confirm (Save changes, Re-run wizard) — Confirm
//     button is kb-accent.
//   - 'danger': destructive confirm (Reset to env defaults) — Confirm
//     button is status-error red.
//
// Caller controls open/close via the `open` prop. The dialog itself
// only renders when open=true; mount-only is intentional so the Modal
// portal isn't sitting in the DOM idle.

interface ConfirmDialogProps {
  open: boolean
  badge?: string
  title: string
  description?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  variant?: 'default' | 'danger'
  onConfirm: () => void
  onCancel: () => void
  busy?: boolean
}

export function ConfirmDialog({
  open,
  badge,
  title,
  description,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  variant = 'default',
  onConfirm,
  onCancel,
  busy,
}: ConfirmDialogProps) {
  if (!open) return null

  const confirmClass =
    variant === 'danger'
      ? 'bg-status-error text-white hover:opacity-90'
      : 'bg-kb-accent text-kb-bg hover:opacity-90'

  return (
    <Modal badge={badge ?? 'Confirm'} title={title} onClose={onCancel} size="sm" unbounded>
      {description && (
        <div className="p-5 text-xs text-kb-text-secondary leading-relaxed">{description}</div>
      )}
      <div className="px-5 py-3 border-t border-kb-border flex items-center justify-end gap-2 shrink-0">
        <button
          type="button"
          onClick={onCancel}
          disabled={busy}
          className="px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border disabled:opacity-50"
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={busy}
          className={`px-3 py-1.5 rounded-md text-xs font-medium disabled:opacity-50 ${confirmClass}`}
        >
          {busy ? 'Working…' : confirmLabel}
        </button>
      </div>
    </Modal>
  )
}
