import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PlanExportModal } from './PlanExportModal'

const { previewPlanExport, exportPlan, role } = vi.hoisted(() => ({
  previewPlanExport: vi.fn(),
  exportPlan: vi.fn(),
  role: { value: 'editor' },
}))
vi.mock('@/services/api', () => ({
  api: {
    previewPlanExport: (...a: unknown[]) => previewPlanExport(...a),
    exportPlan: (...a: unknown[]) => exportPlan(...a),
  },
}))
vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({ hasRole: (r: string) => r === 'viewer' || (r === 'editor' && role.value !== 'viewer') }),
}))

const exported = (format: string, text: string) => ({
  export: { format, filename: `kobi-plan-pln_1-${format}.sh`, text, notes: ['1 step has no identified owner: check it.'] },
})

function renderModal() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PlanExportModal planId="pln_1" title="Right-sizing kyverno" onClose={() => undefined} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  role.value = 'editor'
  previewPlanExport.mockImplementation((_id: string, format: string) =>
    Promise.resolve(exported(format, format === 'diff' ? 'spec:\n  template: {}\n' : 'kubectl -n kyverno set resources deployment/x\n')),
  )
  exportPlan.mockResolvedValue({ ...exported('commands', 'x'), plan: { id: 'pln_1', effectiveStatus: 'exported' } })
})

describe('PlanExportModal', () => {
  it('shows the text and its notes, and changes nothing by looking', async () => {
    renderModal()
    expect(await screen.findByText(/kubectl -n kyverno set resources deployment\/x/)).toBeTruthy()
    expect(screen.getByText('1 step has no identified owner: check it.')).toBeTruthy()
    expect(previewPlanExport).toHaveBeenCalledWith('pln_1', 'commands')
    fireEvent.click(screen.getByRole('tab', { name: 'Diff' }))
    expect(await screen.findByText(/template/)).toBeTruthy()
    expect(previewPlanExport).toHaveBeenCalledWith('pln_1', 'diff')
    expect(exportPlan).not.toHaveBeenCalled()
  })

  // Copy writes the text on screen first (the browser allows it only right
  // after the click), then records the export.
  it('copies the text and records the export', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    renderModal()
    await screen.findByText(/kubectl -n kyverno/)
    fireEvent.click(screen.getByRole('button', { name: /Copy/ }))
    await waitFor(() => expect(exportPlan).toHaveBeenCalledWith('pln_1', 'commands'))
    expect(writeText).toHaveBeenCalledWith('kubectl -n kyverno set resources deployment/x\n')
    expect(await screen.findByText(/the plan is exported/)).toBeTruthy()
  })

  it('lets a viewer read the text but not export it', async () => {
    role.value = 'viewer'
    renderModal()
    await screen.findByText(/kubectl -n kyverno/)
    expect((screen.getByRole('button', { name: /Copy/ }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: /Download/ }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('Editors can export a plan.')).toBeTruthy()
  })
})
