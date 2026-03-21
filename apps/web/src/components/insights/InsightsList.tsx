import { useState } from 'react'
import { useInsights } from '@/hooks/useInsights'
import { InsightCard } from './InsightCard'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { EmptyState } from '@/components/shared/EmptyState'
import { Lightbulb } from 'lucide-react'

type SeverityFilter = '' | 'critical' | 'warning' | 'info'

export function InsightsList() {
  const [severity, setSeverity] = useState<SeverityFilter>('')
  const { data, isLoading, error, refetch } = useInsights(severity ? { severity } : undefined)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const insights = data?.items || []
  const filters: { label: string; value: SeverityFilter }[] = [
    { label: 'All', value: '' },
    { label: 'Critical', value: 'critical' },
    { label: 'Warning', value: 'warning' },
    { label: 'Info', value: 'info' },
  ]

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-lg font-semibold text-[#e8e9ed]">Insights</h1>
        <div className="flex gap-1">
          {filters.map((f) => (
            <button
              key={f.value}
              onClick={() => setSeverity(f.value)}
              className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
                severity === f.value
                  ? 'bg-status-info-dim text-status-info border-status-info/20'
                  : 'bg-kb-card text-[#8b8d9a] border-kb-border hover:border-kb-border-active'
              }`}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      {insights.length === 0 ? (
        <EmptyState
          icon={<Lightbulb className="w-10 h-10" />}
          title="No insights"
          message="Everything looks healthy"
        />
      ) : (
        <div className="space-y-3">
          {insights.map((insight) => (
            <InsightCard key={insight.id} insight={insight} />
          ))}
        </div>
      )}
    </div>
  )
}
