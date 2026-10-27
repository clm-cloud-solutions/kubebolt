import { afterEach, describe, it, expect, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { SetupGuide } from './SetupGuide'

// The connector's manual, as the drawer shows it. What these guard: an
// alternative path ("Or use this machine's credentials") read as step 5 of
// the path it replaces (UI-4), backticks showed up as characters (UI-5), and
// every code block's button was named just "Copy" (UI-9).

describe('SetupGuide', () => {
  it('numbers only the steps that follow each other; an alternative gets "Or"', () => {
    render(
      <SetupGuide
        steps={[
          { title: 'Create the permissions policy', body: '' },
          { title: 'Create the trust policy', body: '' },
          { title: 'Connect KubeBolt', body: '' },
          { title: "Or use this machine's AWS credentials", body: '', alternative: true },
        ]}
      />,
    )
    const markers = screen.getAllByRole('listitem').map((li) => li.firstElementChild?.textContent)
    expect(markers).toEqual(['1', '2', '3', 'Or'])
  })

  it('renders backticked spans in a step body as inline code', () => {
    render(
      <SetupGuide
        steps={[{ title: 'Create the trust policy', body: 'Run `aws sts get-caller-identity` where KubeBolt runs.' }]}
      />,
    )
    const code = screen.getByText('aws sts get-caller-identity')
    expect(code.tagName).toBe('CODE')
    expect(screen.queryByText(/`/)).not.toBeInTheDocument()
  })

  it('keeps an unmatched backtick as a character', () => {
    render(<SetupGuide steps={[{ title: 'T', body: 'a `b` c `d' }]} />)
    expect(screen.getByText('b').tagName).toBe('CODE')
    expect(screen.getByText((_, el) => el?.tagName === 'P' && el.textContent === 'a b c `d')).toBeInTheDocument()
  })

  it('names each copy button after what it copies', () => {
    render(
      <SetupGuide
        steps={[
          { title: 'Create the permissions policy', body: '', code: '{}', language: 'json' },
          { title: 'Create the role', body: '', code: 'aws iam create-role', language: 'shell' },
        ]}
      />,
    )
    expect(screen.getByRole('button', { name: 'Copy the JSON for “Create the permissions policy”' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy the commands for “Create the role”' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy' })).not.toBeInTheDocument()
  })
})

// A code block used to cut long lines at the right edge behind a scrollbar
// the OS hides, and cut its last visible line in half at the height cap,
// with nothing to say there was more.
describe('SetupGuide — code blocks', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })
  const policy = Array.from({ length: 40 }, (_, i) => `  "line-${i}": "arn:aws:iam::111122223333:role/a-very-long-role-name-that-would-not-fit-${i}"`).join('\n')

  it('wraps long lines instead of cutting them', () => {
    render(<SetupGuide steps={[{ title: 'Create the role', body: '', code: 'aws iam create-role --role-name x', language: 'shell' }]} />)
    const pre = screen.getByText('aws iam create-role --role-name x')
    expect(pre.tagName).toBe('PRE')
    expect(pre).toHaveClass('whitespace-pre-wrap')
    expect(pre).not.toHaveClass('whitespace-pre')
  })

  it('shows no fold for a block that fits', () => {
    render(<SetupGuide steps={[{ title: 'T', body: '', code: '{}', language: 'json' }]} />)
    expect(screen.queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument()
  })

  it('folds a tall block behind "Show all N lines" and opens it', async () => {
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(256)
    render(<SetupGuide steps={[{ title: 'Create the trust policy', body: '', code: policy, language: 'json' }]} />)
    const more = screen.getByRole('button', { name: 'Show all 40 lines' })
    expect(more).toHaveAttribute('aria-expanded', 'false')
    const pre = screen.getByText(/line-0/)
    expect(pre).toHaveClass('max-h-64', 'overflow-hidden')
    await act(async () => more.click())
    const less = screen.getByRole('button', { name: 'Show less' })
    expect(less).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/line-0/)).not.toHaveClass('max-h-64')
  })
})
