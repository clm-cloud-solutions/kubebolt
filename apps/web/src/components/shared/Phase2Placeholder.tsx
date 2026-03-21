import { Lock } from 'lucide-react'

interface Phase2PlaceholderProps {
  title?: string
  description?: string
}

export function Phase2Placeholder({
  title = 'Requires KubeBolt Agent',
  description = 'Advanced metrics collection for deeper insights',
}: Phase2PlaceholderProps) {
  return (
    <div className="border-2 border-dashed border-kb-border rounded-lg p-8 flex flex-col items-center justify-center text-center">
      <Lock className="w-8 h-8 text-[#555770] mb-3" />
      <h3 className="text-sm font-medium text-[#8b8d9a] mb-1">{title}</h3>
      <p className="text-xs text-[#555770] mb-4 max-w-sm">{description}</p>
      <div className="bg-kb-bg rounded-md px-3 py-2 font-mono text-[10px] text-[#8b8d9a] border border-kb-border">
        kubectl apply -f https://kubebolt.dev/install/agent.yaml
      </div>
    </div>
  )
}
