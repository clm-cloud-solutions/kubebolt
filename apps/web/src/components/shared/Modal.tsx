import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import { useEscapeClose } from '@/hooks/useEscapeClose'

// Modal is the shared chrome every dialog in KubeBolt sits inside.
// The template was set by the kubectl describe / delete-resource
// modals (see apps/web/src/components/resources/ResourceDetailPage.tsx),
// and this component brings the rest of the app in line:
//
//   - Portal to document.body — no stacking-context or DOM-ancestor
//     bleed, clicks inside the dialog never escape to a parent
//     panel's backdrop.
//   - Dark, blurred backdrop at z-[99999].
//   - Card: bg-kb-card with border, rounded-xl, shadow-2xl.
//   - Header: small uppercase pill badge on the left, title next
//     to it, X icon on the right — no text shortcuts like "esc",
//     the escape key works via useEscapeClose.
//   - Click-outside-to-close and Esc-to-close are both wired up;
//     callers only ever think about onClose.
//
// The body of the modal is raw children. Most callers follow one
// of two shapes:
//
//   <Modal ...>
//     <div className="p-5 space-y-3">{body}</div>                    <!-- short modal -->
//   </Modal>
//
//   <Modal ...>
//     <div className="flex-1 overflow-y-auto p-5">{body}</div>
//     <div className="px-5 py-3 border-t border-kb-border           <!-- scrollable with footer -->
//                     flex justify-end gap-2 shrink-0">{footer}</div>
//   </Modal>

export type ModalSize = 'sm' | 'md' | 'lg' | 'xl' | '2xl'

const sizeToMax: Record<ModalSize, string> = {
  sm: 'max-w-md',   // Short forms: reset password, delete confirmation
  md: 'max-w-lg',   // Standard edit forms
  lg: 'max-w-2xl',  // Detail views with a few fields
  xl: 'max-w-3xl',  // Multi-section forms (install wizard, configure)
  '2xl': 'max-w-5xl', // Long output (describe, logs)
}

export interface ModalProps {
  // Small uppercase pill shown to the left of the title. Omit when
  // the title alone is self-explanatory.
  badge?: ReactNode
  // Main title — shown next to the badge in the header.
  title: ReactNode
  onClose: () => void
  size?: ModalSize
  children: ReactNode
  // When set, the card grows to fit its content instead of being
  // capped at 85vh with internal scrolling. Useful for small
  // dialogs where the content already fits.
  unbounded?: boolean
}

export function Modal({ badge, title, onClose, size = 'md', children, unbounded }: ModalProps) {
  useEscapeClose(onClose)

  return createPortal(
    <div
      className="fixed inset-0 z-[99999] flex items-center justify-center"
      onClick={onClose}
    >
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" />
      <div
        className={`relative w-[90vw] ${sizeToMax[size]} ${unbounded ? '' : 'max-h-[85vh]'} bg-kb-card border border-kb-border rounded-xl shadow-2xl flex flex-col overflow-hidden`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-5 py-3 border-b border-kb-border flex items-center justify-between shrink-0">
          <div className="flex items-center gap-3 min-w-0">
            {badge && (
              <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary bg-kb-elevated px-2 py-0.5 rounded shrink-0">
                {badge}
              </span>
            )}
            <span className="text-sm text-kb-text-primary font-medium truncate">{title}</span>
          </div>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors shrink-0"
            aria-label="Close"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
        {children}
      </div>
    </div>,
    document.body,
  )
}
