import React from 'react'

// YamlViewer is the shared read-only YAML viewer used by the resource detail
// YAML tab and the Helm release manifest tab. Renders line numbers + per-line
// syntax highlighting on the dark code surface, matching the rest of the app.
// (Extracted from ResourceDetailPage so the Helm manifest renders identically
// without duplicating the highlighter.)
export function YamlViewer({
  text,
  heightClass = 'h-[calc(100vh-360px)] min-h-[320px]',
}: {
  text: string
  heightClass?: string
}) {
  const lines = (text ?? '').split('\n')
  return (
    <div
      className={`overflow-auto ${heightClass} rounded-lg p-3`}
      style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}
    >
      <pre className="text-[11px] font-mono leading-5 whitespace-pre-wrap break-all">
        {lines.map((line, i) => (
          <div key={i} className="flex">
            <span className="w-10 text-right pr-3 select-none shrink-0" style={{ color: '#484f58' }}>
              {i + 1}
            </span>
            <span className="flex-1 min-w-0">{highlightYAMLLine(line)}</span>
          </div>
        ))}
      </pre>
    </div>
  )
}

// highlightYAMLLine renders one YAML line with theme-aware syntax classes
// (yaml-comment / yaml-key / yaml-string / yaml-bool / yaml-null / yaml-number
// defined in globals.css). Also exported for callers that render their own
// container (e.g. the detail page's editing-vs-readonly toggle).
export function highlightYAMLLine(line: string): React.ReactNode {
  // Comment lines
  if (/^\s*#/.test(line)) {
    return <span className="yaml-comment">{line}</span>
  }

  // Key: value lines
  const kvMatch = line.match(/^(\s*)([\w.\-/]+)(:)(.*)$/)
  if (kvMatch) {
    const [, indent, key, colon, rest] = kvMatch
    return (
      <>
        <span>{indent}</span>
        <span className="yaml-key">{key}</span>
        <span>{colon}</span>
        {highlightValue(rest)}
      </>
    )
  }

  // List items with key: value
  const listKvMatch = line.match(/^(\s*-\s+)([\w.\-/]+)(:)(.*)$/)
  if (listKvMatch) {
    const [, prefix, key, colon, rest] = listKvMatch
    return (
      <>
        <span>{prefix}</span>
        <span className="yaml-key">{key}</span>
        <span>{colon}</span>
        {highlightValue(rest)}
      </>
    )
  }

  // List items with plain value
  const listMatch = line.match(/^(\s*-\s+)(.*)$/)
  if (listMatch) {
    const [, prefix, val] = listMatch
    return (
      <>
        <span>{prefix}</span>
        {highlightValue(' ' + val)}
      </>
    )
  }

  return <span>{line}</span>
}

function highlightValue(raw: string): React.ReactNode {
  const trimmed = raw.trim()
  if (!trimmed || trimmed === '') return <span>{raw}</span>

  // Quoted strings
  if (/^["'].*["']$/.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-string">{trimmed}</span></>
  }

  // Booleans
  if (/^(true|false)$/i.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-bool">{trimmed}</span></>
  }

  // Null
  if (/^(null|~)$/i.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-null">{trimmed}</span></>
  }

  // Numbers
  if (/^-?\d+(\.\d+)?([eE][+-]?\d+)?$/.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-number">{trimmed}</span></>
  }

  // Plain strings (unquoted)
  if (trimmed.length > 0) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-string">{trimmed}</span></>
  }

  return <span>{raw}</span>
}
