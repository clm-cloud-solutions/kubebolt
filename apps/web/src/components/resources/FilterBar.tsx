import { Search } from 'lucide-react'

interface FilterBarProps {
  namespaces: string[]
  selectedNamespace: string
  onNamespaceChange: (ns: string) => void
  search: string
  onSearchChange: (search: string) => void
  total: number
  resourceName: string
}

export function FilterBar({
  namespaces,
  selectedNamespace,
  onNamespaceChange,
  search,
  onSearchChange,
  total,
  resourceName,
}: FilterBarProps) {
  return (
    <div className="flex items-center justify-between gap-4 mb-4">
      <div className="flex items-center gap-2 flex-wrap">
        <button
          onClick={() => onNamespaceChange('')}
          className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
            selectedNamespace === ''
              ? 'bg-status-info-dim text-status-info border-status-info/20'
              : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
          }`}
        >
          All
        </button>
        {namespaces.map((ns) => (
          <button
            key={ns}
            onClick={() => onNamespaceChange(ns)}
            className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
              selectedNamespace === ns
                ? 'bg-status-info-dim text-status-info border-status-info/20'
                : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
            }`}
          >
            {ns}
          </button>
        ))}
      </div>
      <div className="flex items-center gap-3">
        <span className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">
          {total} {resourceName}
        </span>
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-kb-text-tertiary" />
          <input
            type="text"
            placeholder="Filter..."
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
            className="w-48 pl-8 pr-3 py-1.5 bg-kb-card border border-kb-border rounded-md text-xs text-kb-text-primary placeholder-kb-text-tertiary outline-none focus:border-kb-border-active transition-colors"
          />
        </div>
      </div>
    </div>
  )
}
