import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Folder, FileText, FileCode, Link2, ChevronRight, Download, X, ShieldOff } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import type { ResourceItem } from '@/types/kubernetes'

interface FileEntry {
  name: string
  type: string
  size: string
  modified: string
  permissions: string
}

function formatFileSize(sizeStr: string): string {
  const bytes = parseInt(sizeStr)
  if (isNaN(bytes)) return sizeStr
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const k = 1024
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  const val = bytes / Math.pow(k, i)
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`
}

export function FilesTab({ namespace, name, item }: { namespace: string; name: string; item: ResourceItem }) {
  const containers = Array.isArray(item.containers)
    ? (item.containers as Array<Record<string, unknown>>).map(c => String(c.name ?? ''))
    : []
  const [selectedContainer, setSelectedContainer] = useState(containers[0] ?? '')
  const [currentPath, setCurrentPath] = useState('/')
  const [viewingFile, setViewingFile] = useState<string | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['pod-files', namespace, name, selectedContainer, currentPath],
    queryFn: () => api.listFiles(namespace, name, selectedContainer, currentPath),
    enabled: !!selectedContainer,
  })

  const { data: fileContent, isLoading: contentLoading } = useQuery({
    queryKey: ['pod-file-content', namespace, name, selectedContainer, viewingFile],
    queryFn: () => api.getFileContent(namespace, name, selectedContainer, viewingFile!),
    enabled: !!viewingFile,
  })

  const pathSegments = currentPath.split('/').filter(Boolean)

  function cleanPath(p: string): string {
    // Normalize: remove double slashes, resolve . and ..
    const parts = p.split('/').filter(Boolean)
    const resolved: string[] = []
    for (const part of parts) {
      if (part === '..') {
        resolved.pop()
      } else if (part !== '.') {
        resolved.push(part)
      }
    }
    return '/' + resolved.join('/')
  }

  function navigateTo(entry: FileEntry) {
    if (entry.type === 'dir') {
      const newPath = currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`
      setCurrentPath(cleanPath(newPath))
      setViewingFile(null)
    } else {
      const filePath = currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`
      setViewingFile(cleanPath(filePath))
    }
  }

  function navigateToSegment(index: number) {
    if (index < 0) {
      setCurrentPath('/')
    } else {
      setCurrentPath('/' + pathSegments.slice(0, index + 1).join('/'))
    }
    setViewingFile(null)
  }

  const podStatus = String(item.status ?? '').toLowerCase()
  const canBrowse = podStatus === 'running'

  if (!canBrowse) {
    return <div className="text-center text-xs text-kb-text-tertiary py-12">Pod must be running to browse files</div>
  }

  return (
    <div className="space-y-3">
      {/* Controls */}
      <div className="flex items-center gap-2">
        {containers.length > 1 && (
          <select
            value={selectedContainer}
            onChange={e => { setSelectedContainer(e.target.value); setCurrentPath('/'); setViewingFile(null) }}
            className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
          >
            {containers.map(cn => <option key={cn} value={cn}>{cn}</option>)}
          </select>
        )}
        {containers.length === 1 && (
          <span className="text-xs font-mono text-kb-text-secondary">{containers[0]}</span>
        )}
      </div>

      {/* Breadcrumb */}
      <div className="flex items-center gap-1 text-[11px] font-mono text-kb-text-tertiary flex-wrap">
        <button onClick={() => navigateToSegment(-1)} className="hover:text-kb-text-primary transition-colors">/</button>
        {pathSegments.map((seg, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="w-3 h-3" />
            <button onClick={() => navigateToSegment(i)} className="hover:text-kb-text-primary transition-colors">{seg}</button>
          </span>
        ))}
      </div>

      {/* File content viewer */}
      {viewingFile && (
        <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
          <div className="px-4 py-2.5 border-b border-kb-border flex items-center justify-between">
            <span className="text-[11px] font-mono text-kb-text-secondary">{viewingFile}</span>
            <div className="flex items-center gap-2">
              <a
                href={api.getFileDownloadUrl(namespace, name, selectedContainer, viewingFile)}
                className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
                title="Download"
              >
                <Download className="w-3.5 h-3.5" />
              </a>
              <button
                onClick={() => setViewingFile(null)}
                className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
          <div className="overflow-auto max-h-[400px] p-3" style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}>
            {contentLoading ? (
              <div className="py-8 text-center text-sm text-kb-text-tertiary">Loading...</div>
            ) : (
              <pre className="text-[11px] font-mono leading-5 whitespace-pre-wrap break-all">{fileContent ?? ''}</pre>
            )}
          </div>
        </div>
      )}

      {/* File list */}
      {isLoading && <LoadingSpinner />}
      {error && (
        <div className="flex flex-col items-center justify-center py-12 text-center">
          <div className="w-12 h-12 rounded-2xl bg-status-warn-dim flex items-center justify-center mb-4">
            <ShieldOff className="w-6 h-6 text-status-warn" />
          </div>
          <h3 className="text-sm font-semibold text-kb-text-primary mb-1">Permission denied</h3>
          <p className="text-xs text-kb-text-tertiary max-w-xs">
            Cannot list contents of this directory. The container process does not have read access.
          </p>
        </div>
      )}
      {data && (
        <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-kb-text-tertiary text-left border-b border-kb-border">
                <th className="px-4 py-2 font-normal">Name</th>
                <th className="px-4 py-2 font-normal w-24">Size</th>
                <th className="px-4 py-2 font-normal w-40">Modified</th>
                <th className="px-4 py-2 font-normal w-28">Permissions</th>
                <th className="px-4 py-2 font-normal w-10"></th>
              </tr>
            </thead>
            <tbody className="text-kb-text-secondary">
              {/* Parent directory */}
              {currentPath !== '/' && (
                <tr
                  className="border-t border-kb-border hover:bg-kb-card-hover cursor-pointer transition-colors"
                  onClick={() => navigateToSegment(pathSegments.length - 2)}
                >
                  <td className="px-4 py-2 flex items-center gap-2">
                    <Folder className="w-3.5 h-3.5 text-status-info" />
                    <span className="text-kb-text-tertiary">..</span>
                  </td>
                  <td className="px-4 py-2"></td>
                  <td className="px-4 py-2"></td>
                  <td className="px-4 py-2"></td>
                  <td className="px-4 py-2"></td>
                </tr>
              )}
              {(data.items ?? []).map((entry) => {
                const isDir = entry.type === 'dir'
                const isLink = entry.type === 'link'
                const Icon = isDir ? Folder : isLink ? Link2 : entry.name.match(/\.(ya?ml|json|toml|conf|cfg|ini|sh|py|js|ts|go|rs)$/i) ? FileCode : FileText
                const iconColor = isDir ? 'text-status-info' : isLink ? 'text-status-warn' : 'text-kb-text-tertiary'
                const filePath = currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`

                return (
                  <tr
                    key={entry.name}
                    className="border-t border-kb-border hover:bg-kb-card-hover cursor-pointer transition-colors"
                    onClick={() => navigateTo(entry)}
                  >
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        <Icon className={`w-3.5 h-3.5 ${iconColor} shrink-0`} />
                        <span className={`font-mono ${isDir ? 'text-status-info' : 'text-kb-text-primary'}`}>{entry.name}</span>
                      </div>
                    </td>
                    <td className="px-4 py-2 font-mono text-kb-text-tertiary">{isDir ? '-' : formatFileSize(entry.size)}</td>
                    <td className="px-4 py-2 font-mono text-kb-text-tertiary">{entry.modified || '-'}</td>
                    <td className="px-4 py-2 font-mono text-kb-text-tertiary">{entry.permissions}</td>
                    <td className="px-4 py-2">
                      {!isDir && (
                        <a
                          href={api.getFileDownloadUrl(namespace, name, selectedContainer, filePath)}
                          onClick={e => e.stopPropagation()}
                          className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors inline-block"
                          title="Download"
                        >
                          <Download className="w-3 h-3" />
                        </a>
                      )}
                    </td>
                  </tr>
                )
              })}
              {(data.items ?? []).length === 0 && (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-kb-text-tertiary">Empty directory</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
