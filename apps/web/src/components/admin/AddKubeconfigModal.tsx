import { useState, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { FileText, AlertTriangle, Upload } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api } from '@/services/api'

// El alta por kubeconfig. Sale de ClustersPage para que Fleet pueda ofrecer el
// MISMO alta en vez de un enlace que cruza de altitud — ver AddClusterButton,
// que es quien monta los dos caminos.
//
// Se movió tal cual, sin retocar copia ni comportamiento: mezclar una
// extracción con cambios es lo que hace imposible revisar si algo se rompió al
// mover.

export function AddKubeconfigModal({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [kubeconfig, setKubeconfig] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)

  async function handleFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    if (file.size > 1024 * 1024) {
      setError('File too large (max 1MB)')
      return
    }
    const text = await file.text()
    setKubeconfig(text)
    setError(null)
  }

  async function handleSubmit() {
    if (!kubeconfig.trim()) {
      setError('Please paste a kubeconfig or choose a file')
      return
    }
    setUploading(true)
    setError(null)
    try {
      const result = await api.addCluster(kubeconfig)
      queryClient.invalidateQueries({ queryKey: ['clusters'] })
      onClose()
      console.log(`Added ${result.added.length} cluster context(s):`, result.added)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add cluster')
    } finally {
      setUploading(false)
    }
  }

  return (
    <Modal badge="Add cluster" title="Upload kubeconfig" onClose={onClose} size="lg">
        <div className="p-6 space-y-4">
          <p className="text-xs text-kb-text-secondary">
            Paste the content of a kubeconfig file or choose a file to upload. All contexts in the file will be added.
          </p>

          <div className="flex items-center gap-2">
            <button
              onClick={() => fileInputRef.current?.click()}
              className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-kb-elevated hover:bg-kb-card-hover text-xs text-kb-text-primary transition-colors border border-kb-border"
            >
              <FileText className="w-3.5 h-3.5" />
              Choose file...
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".yaml,.yml,.kubeconfig,text/yaml,application/yaml"
              onChange={handleFile}
              className="hidden"
            />
            <span className="text-[10px] font-mono text-kb-text-tertiary">or paste below</span>
          </div>

          <textarea
            value={kubeconfig}
            onChange={(e) => { setKubeconfig(e.target.value); setError(null) }}
            placeholder="apiVersion: v1&#10;kind: Config&#10;clusters:&#10;  - name: my-cluster&#10;    cluster:&#10;      server: https://..."
            className="w-full h-64 px-3 py-2 rounded-lg bg-kb-bg border border-kb-border text-[11px] font-mono text-kb-text-primary placeholder:text-kb-text-tertiary/50 focus:outline-none focus:border-kb-border-active resize-none"
          />

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim">
              <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
              <span className="text-[11px] text-status-error">{error}</span>
            </div>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 px-6 py-4 border-t border-kb-border bg-kb-surface">
          <button
            onClick={onClose}
            className="px-4 py-1.5 rounded-lg text-xs text-kb-text-secondary hover:text-kb-text-primary transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={uploading || !kubeconfig.trim()}
            className="flex items-center gap-1.5 px-4 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-medium hover:bg-kb-accent-bright transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Upload className="w-3 h-3" />
            {uploading ? 'Uploading...' : 'Add cluster'}
          </button>
        </div>
    </Modal>
  )
}
