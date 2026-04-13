import { Construction } from 'lucide-react'

interface AdminPlaceholderPageProps {
  title: string
  description: string
}

export function AdminPlaceholderPage({ title, description }: AdminPlaceholderPageProps) {
  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="flex flex-col items-center justify-center py-20">
        <div className="w-12 h-12 rounded-xl bg-kb-elevated flex items-center justify-center mb-4">
          <Construction className="w-6 h-6 text-kb-text-tertiary" />
        </div>
        <h2 className="text-lg font-semibold text-kb-text-primary mb-1">{title}</h2>
        <p className="text-xs text-kb-text-tertiary text-center max-w-sm mb-4">{description}</p>
        <span className="px-3 py-1 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-status-info-dim text-status-info">
          Coming soon
        </span>
      </div>
    </div>
  )
}
