import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { MarkdownRenderer } from './MarkdownRenderer'

// Regression pins for the lightweight yaml/bash highlighter. Field-driven:
// trailing comments (`timeoutSeconds: 3  # era 1`) shipped undimmed in the
// first pass because only full-line comments were handled.

const yamlFence = (body: string) => '```yaml\n' + body + '\n```'
const bashFence = (body: string) => '```bash\n' + body + '\n```'

const COMMENT_HEX = '6b6864'
const VALUE_HEX = 'f0ede6'

function spans(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll('code span'))
}

describe('MarkdownRenderer code highlighting', () => {
  it('accents YAML keys and emphasizes unit-bearing quantities', () => {
    const { container } = render(
      <MarkdownRenderer content={yamlFence('resources:\n  limits: { memory: 512Mi }')} />,
    )
    const key = spans(container).find((s) => s.textContent === 'resources')
    expect(key?.className).toContain('text-kobi-accent')
    const qty = spans(container).find((s) => s.textContent === '512Mi')
    expect(qty?.className).toContain(VALUE_HEX)
  })

  it('dims a full-line YAML comment', () => {
    const { container } = render(
      <MarkdownRenderer content={yamlFence('# failureThreshold: 3 — puede quedarse')} />,
    )
    const comment = spans(container).find((s) =>
      s.textContent?.startsWith('# failureThreshold'),
    )
    expect(comment?.className).toContain(COMMENT_HEX)
  })

  it('dims a trailing YAML comment while keeping the key accented', () => {
    const { container } = render(
      <MarkdownRenderer content={yamlFence('timeoutSeconds: 3   # era 1')} />,
    )
    const key = spans(container).find((s) => s.textContent === 'timeoutSeconds')
    expect(key?.className).toContain('text-kobi-accent')
    const comment = spans(container).find((s) => s.textContent === '# era 1')
    expect(comment?.className).toContain(COMMENT_HEX)
  })

  it('dims trailing bash comments but never URL fragments or =# values', () => {
    const { container } = render(
      <MarkdownRenderer
        content={bashFence(
          'kubectl get pods # lista\ncurl https://x.io/a#frag\nkubectl get events --sort-by=#weird',
        )}
      />,
    )
    const all = spans(container)
    const comment = all.find((s) => s.textContent === '# lista')
    expect(comment?.className).toContain(COMMENT_HEX)
    // Neither the URL fragment line nor the =# flag line produce a comment span.
    const dimmed = all.filter((s) => s.className.includes(COMMENT_HEX))
    expect(dimmed).toHaveLength(1)
  })

  it('leaves non-yaml/bash languages untouched', () => {
    const { container } = render(<MarkdownRenderer content={'```json\n{ "a": 1 }\n```'} />)
    expect(spans(container).filter((s) => s.className.includes(COMMENT_HEX))).toHaveLength(0)
    expect(container.textContent).toContain('{ "a": 1 }')
  })
})

// Field report: Kobi's insight table rendered its headers one character per
// line — "Severidad" as "Seve / rida / d", "crítico" as "críti / co". Two
// causes compounding: the renderer sets overflow-wrap:anywhere on the whole
// message (so a pod name cannot blow the panel open) and it INHERITS into
// cells; and the table was w-full, pinned to the panel, so it could never
// overflow and the wrapper's overflow-x-auto had nothing to scroll.
describe('MarkdownRenderer tables', () => {
  const table = [
    '| Insight | Severidad | Desde | Detalle |',
    '| --- | --- | --- | --- |',
    '| Zero Available Replicas | crítico | 10:04 | 0 de 2 réplicas disponibles |',
  ].join('\n')

  it('lets the table outgrow the panel so the wrapper can scroll it', () => {
    const { container } = render(<MarkdownRenderer content={table} />)
    const el = container.querySelector('table')!
    expect(el.className).toContain('min-w-full')
    // w-full pins it to the panel width: it can never overflow, so the columns
    // squeeze instead of scrolling. That is the bug.
    expect(el.className.split(/\s+/)).not.toContain('w-full')
    expect(container.querySelector('div.overflow-x-auto')).not.toBeNull()
  })

  it('never breaks a header mid-word', () => {
    const { container } = render(<MarkdownRenderer content={table} />)
    for (const th of Array.from(container.querySelectorAll('th'))) {
      expect(th.className).toContain('whitespace-nowrap')
      expect(th.className).toContain('[overflow-wrap:normal]')
    }
  })

  it('breaks a cell only when a single token truly does not fit', () => {
    const { container } = render(<MarkdownRenderer content={table} />)
    for (const td of Array.from(container.querySelectorAll('td'))) {
      // break-word, not anywhere: a long pod name still breaks so the column
      // cannot run away, but "crítico" stays one word.
      expect(td.className).toContain('[overflow-wrap:break-word]')
      expect(td.className).not.toContain('anywhere')
    }
  })
})
