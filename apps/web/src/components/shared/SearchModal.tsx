import { useState, useEffect, useRef, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import {
  Search, X, Box, Server, Layers, Database, BarChart3, Timer, Clock,
  Globe, ArrowRightLeft, HardDrive, Disc, FolderClosed, FileText, Lock,
  Scale, FolderOpen, SearchX,
} from 'lucide-react'
import { api } from '@/services/api'
import { StatusBadge } from '@/components/resources/StatusBadge'

interface SearchResult {
  name: string
  namespace: string
  kind: string
  resourceType: string
  status: string
}

const MIN_CHARS = 3

const kindIcons: Record<string, React.ReactNode> = {
  Pod: <Box className="w-3.5 h-3.5" />,
  Node: <Server className="w-3.5 h-3.5" />,
  Deployment: <Layers className="w-3.5 h-3.5" />,
  StatefulSet: <Database className="w-3.5 h-3.5" />,
  DaemonSet: <BarChart3 className="w-3.5 h-3.5" />,
  Job: <Timer className="w-3.5 h-3.5" />,
  CronJob: <Clock className="w-3.5 h-3.5" />,
  Service: <Globe className="w-3.5 h-3.5" />,
  Ingress: <ArrowRightLeft className="w-3.5 h-3.5" />,
  ConfigMap: <FileText className="w-3.5 h-3.5" />,
  Secret: <Lock className="w-3.5 h-3.5" />,
  PVC: <HardDrive className="w-3.5 h-3.5" />,
  PV: <Disc className="w-3.5 h-3.5" />,
  StorageClass: <FolderClosed className="w-3.5 h-3.5" />,
  HPA: <Scale className="w-3.5 h-3.5" />,
  Namespace: <FolderOpen className="w-3.5 h-3.5" />,
}

export function SearchModal({ onClose }: { onClose: () => void }) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SearchResult[]>([])
  const [loading, setLoading] = useState(false)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const resultsRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()

  useEffect(() => { inputRef.current?.focus() }, [])

  useEffect(() => {
    function handleKey(e: KeyboardEvent) { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [onClose])

  useEffect(() => {
    if (query.trim().length < MIN_CHARS) {
      setResults([])
      setLoading(false)
      return
    }
    // Flip loading BEFORE the debounce, not inside the setTimeout. With
    // the previous version, during the 200ms debounce window there was
    // a frame where loading=false, results=[] (stale), and the query
    // already crossed MIN_CHARS — the empty-state branch matched and
    // flashed "No resources match ..." even for queries that ended up
    // returning hits. The debounce should gate WHEN the API fires, not
    // WHEN the loading indicator appears.
    setLoading(true)
    const timer = setTimeout(async () => {
      try {
        const data = await api.search(query)
        setResults(data ?? [])
        setSelectedIndex(0)
      } catch {}
      setLoading(false)
    }, 200)
    return () => clearTimeout(timer)
  }, [query])

  useEffect(() => {
    if (resultsRef.current) {
      const el = resultsRef.current.querySelector('[data-selected="true"]')
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIndex])

  const navigateTo = useCallback((result: SearchResult) => {
    const ns = result.namespace || '_'
    navigate(`/${result.resourceType}/${ns}/${result.name}`)
    onClose()
  }, [navigate, onClose])

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex(i => Math.min(i + 1, (results ?? []).length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter' && results?.[selectedIndex]) {
      navigateTo(results[selectedIndex])
    }
  }

  const grouped = (results ?? []).reduce<Record<string, SearchResult[]>>((acc, r) => {
    if (!acc[r.kind]) acc[r.kind] = []
    acc[r.kind].push(r)
    return acc
  }, {})

  const totalResults = (results ?? []).length
  let flatIndex = -1

  return createPortal(
    <div className="fixed inset-0 z-[99999] flex items-start justify-center pt-[12vh]" onClick={onClose}>
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" />
      <div
        className="relative w-[90vw] max-w-xl bg-kb-card border border-kb-border rounded-xl shadow-2xl overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        {/* Search input */}
        <div className="flex items-center gap-3 px-4 py-3.5 border-b border-kb-border">
          <Search className="w-4 h-4 text-kb-text-tertiary shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search resources..."
            className="flex-1 bg-transparent text-sm text-kb-text-primary placeholder-kb-text-tertiary outline-none"
          />
          {query && totalResults > 0 && (
            <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">{totalResults} results</span>
          )}
          <button onClick={onClose} className="p-0.5 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors">
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Results */}
        <div ref={resultsRef} className="max-h-[55vh] overflow-y-auto">
          {query.length < MIN_CHARS && (
            <div className="px-4 py-8 text-center text-xs text-kb-text-tertiary">
              Type at least {MIN_CHARS} characters to search or see results below...
            </div>
          )}

          {loading && query.length >= MIN_CHARS && (
            <div className="px-4 py-6 text-center text-xs text-kb-text-tertiary">Searching...</div>
          )}

          {!loading && query.length >= MIN_CHARS && totalResults === 0 && (
            <div className="flex flex-col items-center justify-center px-4 py-10 text-center">
              <SearchX className="w-8 h-8 text-kb-text-tertiary mb-3" />
              <h3 className="text-sm font-medium text-kb-text-secondary mb-1">
                No resources match "{query}"
              </h3>
              <p className="text-xs text-kb-text-tertiary mb-4 max-w-xs">
                The search spans all 16 resource types by name. Try a shorter substring or check the spelling.
              </p>
              <button
                type="button"
                onClick={() => setQuery('')}
                className="px-3 py-1.5 text-xs font-medium bg-kb-elevated text-kb-text-primary rounded-md border border-kb-border hover:border-kb-border-active transition-colors"
              >
                Clear search
              </button>
            </div>
          )}

          {!loading && Object.entries(grouped).map(([kind, items]) => (
            <div key={kind}>
              {/* Group header with icon */}
              <div className="px-4 py-2 flex items-center gap-2 bg-kb-card sticky top-0 border-b border-kb-border">
                <span className="text-kb-text-tertiary">{kindIcons[kind] ?? <Box className="w-3.5 h-3.5" />}</span>
                <span className="text-[11px] font-semibold text-kb-text-secondary">{kind}</span>
                <span className="text-[10px] font-mono text-kb-text-tertiary">{items.length}</span>
              </div>
              {items.map((result) => {
                flatIndex++
                const idx = flatIndex
                const isSelected = idx === selectedIndex
                return (
                  <button
                    key={`${result.resourceType}-${result.namespace}-${result.name}`}
                    data-selected={isSelected}
                    onClick={() => navigateTo(result)}
                    onMouseEnter={() => setSelectedIndex(idx)}
                    className={`w-full text-left px-4 py-2.5 flex items-center gap-3 transition-colors ${
                      isSelected ? 'bg-status-info-dim' : 'hover:bg-kb-card-hover'
                    }`}
                  >
                    <span className="text-kb-text-tertiary shrink-0">{kindIcons[kind] ?? <Box className="w-3.5 h-3.5" />}</span>
                    <div className="flex-1 min-w-0">
                      <div className="text-xs text-kb-text-primary truncate">{result.name}</div>
                      {result.namespace && (
                        <div className="text-[10px] font-mono text-kb-text-tertiary">
                          Namespace: {result.namespace}
                        </div>
                      )}
                    </div>
                    {result.status && <StatusBadge status={result.status} />}
                  </button>
                )
              })}
            </div>
          ))}
        </div>

        {/* Footer hint */}
        {totalResults > 0 && (
          <div className="px-4 py-2 border-t border-kb-border flex items-center gap-4 text-[10px] font-mono text-kb-text-tertiary">
            <span><kbd className="px-1 py-0.5 rounded bg-kb-bg border border-kb-border">↑↓</kbd> navigate</span>
            <span><kbd className="px-1 py-0.5 rounded bg-kb-bg border border-kb-border">↵</kbd> open</span>
            <span><kbd className="px-1 py-0.5 rounded bg-kb-bg border border-kb-border">esc</kbd> close</span>
          </div>
        )}
      </div>
    </div>,
    document.body
  )
}
