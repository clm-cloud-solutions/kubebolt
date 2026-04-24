import { useEffect } from 'react'

// useEscapeClose attaches a window keydown listener that calls the
// given handler when Escape is pressed. Common boilerplate for
// modals — pulled into a hook so every dialog stays honest about
// the "esc to close" affordance every modal in this product
// advertises.
//
// e.stopPropagation() prevents a nested modal's outer handler from
// firing when the inner one captures the keystroke first, which is
// the usual failure mode when several modals are open at once.
export function useEscapeClose(onClose: () => void) {
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])
}
