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
