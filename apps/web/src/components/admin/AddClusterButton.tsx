import { useState, useRef, useEffect } from 'react'
import { Plus, ChevronDown, FileText, Cable } from 'lucide-react'
import { AddClusterWizard } from '@/components/admin/AddClusterWizard'
import { AddKubeconfigModal } from '@/components/admin/AddKubeconfigModal'

// La puerta de entrada del producto: dar de alta un cluster.
//
// Vivía entera dentro de ClustersPage —desplegable, dos caminos, dos modales— y
// Fleet se limitaba a `navigate('/clusters')`. Eso dejó de valer al partir la
// navegación en dos alturas: Fleet es global y `/clusters` bajó al menú de
// cluster, así que el botón principal de la pantalla de flota **saltaba de
// altitud** y cambiaba el menú entero bajo el cursor. Y el doc lo marca como
// obligatorio en Fleet: sin alta ahí, el producto se queda sin puerta.
//
// Se extrae en vez de copiarse. Son dos rutas de alta con letra pequeña que ya
// costó escribir —cuándo elegir kubeconfig y cuándo agente—, y dos copias de la
// primera pantalla que ve un cliente nuevo se separan a la tercera semana. Con
// esto Fleet y Clusters no se parecen: **son la misma**.
interface Props {
  /** Sin permiso no se renderiza nada. El backend manda igual; esto es cosmético. */
  canManage: boolean
  /** Etiqueta — Fleet dice "Connect cluster" y Clusters "Add cluster". */
  label?: string
  /** Alineación del desplegable, según de qué lado de la cabecera cuelgue. */
  align?: 'left' | 'right'
}

export function AddClusterButton({ canManage, label = 'Add cluster', align = 'right' }: Props) {
  const [menuOpen, setMenuOpen] = useState(false)
  const [modal, setModal] = useState<'kubeconfig' | 'agent' | null>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false)
      }
    }
    if (menuOpen) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [menuOpen])

  if (!canManage) return null

  return (
    <>
      <div className="relative" ref={menuRef}>
        <button
          onClick={() => setMenuOpen((o) => !o)}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors"
        >
          <Plus className="w-3.5 h-3.5" />
          {label}
          <ChevronDown className={`w-3 h-3 transition-transform ${menuOpen ? 'rotate-180' : ''}`} />
        </button>
        {menuOpen && (
          <div
            className={`absolute top-full mt-1 w-72 bg-kb-card border border-kb-border rounded-lg shadow-xl z-50 py-1 overflow-hidden ${
              align === 'right' ? 'right-0' : 'left-0'
            }`}
          >
            {/* La letra pequeña de cada camino es lo que hace útil el
                desplegable: el criterio no es de gusto, es si este backend
                alcanza al apiserver. */}
            <button
              type="button"
              onClick={() => { setMenuOpen(false); setModal('kubeconfig') }}
              className="w-full text-left px-3 py-2.5 hover:bg-kb-card-hover transition-colors"
            >
              <div className="flex items-center gap-2 mb-0.5">
                <FileText className="w-3.5 h-3.5 text-kb-text-secondary" />
                <span className="text-xs font-medium text-kb-text-primary">Import kubeconfig</span>
              </div>
              <div className="text-[10px] text-kb-text-tertiary pl-5 leading-snug">
                KubeBolt dials the apiserver directly. Best when you have a SA token and the cluster is reachable from this backend.
              </div>
            </button>
            <div className="border-t border-kb-border" />
            <button
              type="button"
              onClick={() => { setMenuOpen(false); setModal('agent') }}
              className="w-full text-left px-3 py-2.5 hover:bg-kb-card-hover transition-colors"
            >
              <div className="flex items-center gap-2 mb-0.5">
                <Cable className="w-3.5 h-3.5 text-kb-text-secondary" />
                <span className="text-xs font-medium text-kb-text-primary">Install agent</span>
              </div>
              <div className="text-[10px] text-kb-text-tertiary pl-5 leading-snug">
                Remote agent dials back over gRPC. Best when the cluster's apiserver isn't reachable from this backend.
              </div>
            </button>
          </div>
        )}
      </div>

      {modal === 'kubeconfig' && <AddKubeconfigModal onClose={() => setModal(null)} />}
      {modal === 'agent' && <AddClusterWizard onClose={() => setModal(null)} />}
    </>
  )
}
