import { useReactFlow } from 'reactflow'
import { ZoomIn, ZoomOut, Maximize2 } from 'lucide-react'

export function MapControls() {
  const { zoomIn, zoomOut, fitView, getZoom } = useReactFlow()
  const zoom = Math.round(getZoom() * 100)

  return (
    <div className="absolute bottom-4 left-4 flex items-center gap-1 bg-kb-card border border-kb-border rounded-lg p-1 z-10">
      <button
        onClick={() => zoomOut()}
        className="p-1.5 rounded hover:bg-kb-elevated transition-colors text-[#8b8d9a] hover:text-[#e8e9ed]"
        title="Zoom out"
      >
        <ZoomOut className="w-4 h-4" />
      </button>
      <span className="text-[10px] font-mono text-[#555770] w-10 text-center">{zoom}%</span>
      <button
        onClick={() => zoomIn()}
        className="p-1.5 rounded hover:bg-kb-elevated transition-colors text-[#8b8d9a] hover:text-[#e8e9ed]"
        title="Zoom in"
      >
        <ZoomIn className="w-4 h-4" />
      </button>
      <div className="w-px h-4 bg-kb-border mx-0.5" />
      <button
        onClick={() => fitView({ padding: 0.2 })}
        className="p-1.5 rounded hover:bg-kb-elevated transition-colors text-[#8b8d9a] hover:text-[#e8e9ed]"
        title="Fit view"
      >
        <Maximize2 className="w-4 h-4" />
      </button>
    </div>
  )
}
