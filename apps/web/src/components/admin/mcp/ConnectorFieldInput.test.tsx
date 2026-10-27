import { describe, it, expect, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { McpFieldSchema } from '@/services/api'
import { ConnectorFieldInput, type SecretState } from './ConnectorFieldInput'

// user-event fires its events through its own copy of @testing-library/dom
// (10.x), which React Testing Library 14 — built on 9.x — never hooks up to
// act(): the updates an event causes land outside act and React warns.
// Going through act settles each interaction before the next assertion.
const user = {
  click: (el: Element) => act(() => userEvent.click(el)),
  type: (el: Element, text: string) => act(() => userEvent.type(el, text)),
}

// One generated form row. What these guard: a multiline field (CONTRACT §5
// FieldSchema.multiline — a pasted credential JSON) gets a textarea without
// ever showing a secret unmasked by default, and a stored secret does not
// announce itself as a field that still has to be filled.

function renderField(field: McpFieldSchema, opts: { value?: string; secretState?: SecretState } = {}) {
  const onChange = vi.fn()
  render(
    <ConnectorFieldInput
      field={field}
      idPrefix="t"
      value={opts.value ?? ''}
      onChange={onChange}
      dirty={false}
      secretState={opts.secretState}
    />,
  )
  return { onChange }
}

describe('ConnectorFieldInput', () => {
  it('renders a multiline setting as a textarea', () => {
    renderField({ name: 'notes', label: 'Notes', secret: false, required: false, multiline: true })
    expect(screen.getByLabelText('Notes').tagName).toBe('TEXTAREA')
  })

  it('keeps a multiline secret masked until revealed, then opens it as a textarea', async () => {
    renderField(
      { name: 'credentialJSON', label: 'Credential JSON', secret: true, required: true, multiline: true },
      { value: '{\n  "type": "service_account"\n}' },
    )
    const masked = screen.getByLabelText('Credential JSON')
    expect(masked.tagName).toBe('INPUT')
    expect(masked).toHaveAttribute('type', 'password')
    // A key file is offered for a multiline secret, whatever its name.
    expect(screen.getByRole('button', { name: /Load file/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Show Credential JSON' }))
    const open = screen.getByLabelText('Credential JSON')
    expect(open.tagName).toBe('TEXTAREA')
    expect((open as HTMLTextAreaElement).value).toBe('{\n  "type": "service_account"\n}')

    await user.click(screen.getByRole('button', { name: 'Hide Credential JSON' }))
    expect(screen.getByLabelText('Credential JSON')).toHaveAttribute('type', 'password')
  })

  it('keeps a single-line setting an input', () => {
    renderField({ name: 'roleArn', label: 'Role ARN', secret: false, required: true })
    const input = screen.getByLabelText('Role ARN')
    expect(input.tagName).toBe('INPUT')
    expect(input).toHaveAttribute('aria-required', 'true')
  })

  // The GCP key is the one secret a connector stores today (the AWS external
  // ID is generated, below).
  const credential: McpFieldSchema = { name: 'credentialJSON', label: 'Credential JSON', secret: true, required: true, multiline: true }

  it('does not mark a stored secret as still required — blank keeps it', () => {
    renderField(credential, { secretState: 'set' })
    expect(screen.getByLabelText('Credential JSON')).not.toHaveAttribute('aria-required')
    expect(screen.getByText(/Already set — leave blank to keep it/)).toBeInTheDocument()
  })

  it('still marks a secret required when the stored one cannot be read', () => {
    renderField(credential, { secretState: 'unreadable' })
    expect(screen.getByLabelText('Credential JSON')).toHaveAttribute('aria-required', 'true')
    // Warn text in the per-theme amber: the single-valued status amber reads
    // at about 2:1 on the light theme's white.
    const helper = screen.getByText(/The stored value can't be read anymore/)
    expect(helper).toHaveClass('text-kobi-st-warn')
    expect(helper).not.toHaveClass('text-status-warn')
  })
})

// SEC-3: a value KubeBolt generated (the AWS external ID) is shown and copied,
// never typed — the API refuses a save or a test that carries it.
describe('ConnectorFieldInput — generated value', () => {
  it('is read-only, with a copy button that names it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const { onChange } = renderField(
      { name: 'externalId', label: 'External ID', secret: false, required: true, generated: true },
      { value: 'a1b2c3d4' },
    )
    const input = screen.getByLabelText('External ID')
    expect(input).toHaveAttribute('readonly')
    expect(input).toHaveValue('a1b2c3d4')
    // Filled by KubeBolt, so not announced as a field the admin must fill.
    expect(input).not.toHaveAttribute('aria-required')
    await user.type(input, 'x')
    expect(onChange).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Copy External ID' }))
    expect(writeText).toHaveBeenCalledWith('a1b2c3d4')
  })
})

// The error border must survive the focus: after a failed Save or Test the
// focus jumps to the first invalid field, and a green focus border next to a
// red message read as "this one is fine".
describe('ConnectorFieldInput — invalid', () => {
  const projectId: McpFieldSchema = { name: 'projectId', label: 'Project ID', secret: false, required: true }
  it('keeps the error colour when focused', () => {
    render(<ConnectorFieldInput field={projectId} idPrefix="t" value="" onChange={vi.fn()} dirty={false} error="Project ID is required." />)
    const input = screen.getByLabelText(/Project ID/)
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveClass('border-status-error', 'focus:border-status-error')
    expect(input).not.toHaveClass('focus:border-kb-accent')
  })
  it('uses the accent focus border when valid', () => {
    render(<ConnectorFieldInput field={projectId} idPrefix="t" value="p" onChange={vi.fn()} dirty={false} />)
    const input = screen.getByLabelText(/Project ID/)
    expect(input).toHaveClass('border-kb-border', 'focus:border-kb-accent')
  })
})
