import { useMemo, useState } from 'react'
import { RotateCcw } from 'lucide-react'
import { diffLines } from 'diff'
import { Modal } from '@/components/shared/Modal'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { highlightYAMLLine } from '@/components/shared/YamlViewer'
import { useRolloutHistory } from '@/hooks/useResources'

// RevisionDiffModal — side-by-side YAML diff of a workload's manifest between
// two revisions, off the K8s-native revision history (no snapshots). Renders
// with the SAME highlighter as the YAML tab (highlightYAMLLine) on the same
// #0d1117 surface, with line wrapping so long values are fully visible, and
// GitHub-style red/green line tints for removed/added.
//
// Diff content is the full workload manifest AS OF each revision (the live
// object with that revision's pod template swapped in, sanitized) — same
// context as the YAML tab. Rollback reuses the parent's gated flow.

interface Props {
  type: 'deployments' | 'statefulsets' | 'daemonsets'
  namespace: string
  name: string
  targetRevision: number
  onClose: () => void
  onRollback?: (revision: number) => void
  canEdit?: boolean
}

type Mode = 'prev-vs-this' | 'this-vs-current'

type RowKind = 'context' | 'removed' | 'added' | 'change'
interface Row {
  leftNo: number | null
  rightNo: number | null
  left: string | null
  right: string | null
  kind: RowKind
}

function toLines(v: string): string[] {
  const lines = v.split('\n')
  if (lines.length && lines[lines.length - 1] === '') lines.pop()
  return lines
}

// buildRows aligns a line diff into side-by-side rows (GitHub split view).
function buildRows(leftYaml: string, rightYaml: string): Row[] {
  const parts = diffLines(leftYaml, rightYaml)
  const rows: Row[] = []
  let leftNo = 1
  let rightNo = 1
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i]
    if (!p.added && !p.removed) {
      for (const l of toLines(p.value)) {
        rows.push({ leftNo: leftNo++, rightNo: rightNo++, left: l, right: l, kind: 'context' })
      }
    } else if (p.removed && i + 1 < parts.length && parts[i + 1].added) {
      // replace block — pair removed (left) with added (right), pad the shorter.
      const rem = toLines(p.value)
      const add = toLines(parts[i + 1].value)
      const n = Math.max(rem.length, add.length)
      for (let k = 0; k < n; k++) {
        rows.push({
          leftNo: k < rem.length ? leftNo++ : null,
          rightNo: k < add.length ? rightNo++ : null,
          left: k < rem.length ? rem[k] : null,
          right: k < add.length ? add[k] : null,
          kind: 'change',
        })
      }
      i++ // consume the paired added part
    } else if (p.removed) {
      for (const l of toLines(p.value)) {
        rows.push({ leftNo: leftNo++, rightNo: null, left: l, right: null, kind: 'removed' })
      }
    } else {
      for (const l of toLines(p.value)) {
        rows.push({ leftNo: null, rightNo: rightNo++, left: null, right: l, kind: 'added' })
      }
    }
  }
  return rows
}

const DEL_BG = 'rgba(248,81,73,0.15)'
const ADD_BG = 'rgba(63,185,80,0.15)'

function DiffCell({ no, line, removed, added }: { no: number | null; line: string | null; removed: boolean; added: boolean }) {
  const bg = removed ? DEL_BG : added ? ADD_BG : 'transparent'
  const sign = removed ? '-' : added ? '+' : ' '
  return (
    <div className="flex-1 min-w-0 flex" style={{ backgroundColor: bg }}>
      <span className="w-10 text-right pr-2 select-none shrink-0" style={{ color: '#484f58' }}>
        {no ?? ''}
      </span>
      <span className="w-3 select-none shrink-0" style={{ color: '#484f58' }}>{line != null ? sign : ''}</span>
      <span className="flex-1 min-w-0 whitespace-pre-wrap break-all">
        {line != null ? highlightYAMLLine(line) : ''}
      </span>
    </div>
  )
}

export function RevisionDiffModal({
  type,
  namespace,
  name,
  targetRevision,
  onClose,
  onRollback,
  canEdit,
}: Props) {
  const { data, isLoading, error } = useRolloutHistory(type, namespace, name)
  const [mode, setMode] = useState<Mode>('prev-vs-this')

  const revisions = useMemo(() => data?.revisions ?? [], [data])
  const target = revisions.find((r) => r.revision === targetRevision)
  const previous = useMemo(
    () => revisions.filter((r) => r.revision < targetRevision).sort((a, b) => b.revision - a.revision)[0],
    [revisions, targetRevision],
  )
  const current = useMemo(
    () => revisions.find((r) => r.revision === (data?.currentRevision ?? 0)) ?? revisions.find((r) => r.active),
    [revisions, data],
  )

  const { left, right } = useMemo(() => {
    if (mode === 'this-vs-current') return { left: target, right: current }
    return { left: previous, right: target }
  }, [mode, target, previous, current])

  const leftYaml = left?.manifestYaml ?? ''
  const rightYaml = right?.manifestYaml ?? ''
  const bothPresent = !!leftYaml && !!rightYaml && !!left && !!right
  const identical = bothPresent && leftYaml === rightYaml

  const rows = useMemo(() => (bothPresent && !identical ? buildRows(leftYaml, rightYaml) : []), [leftYaml, rightYaml, bothPresent, identical])

  function rollback(rev?: number) {
    if (rev == null || !onRollback) return
    onRollback(rev)
    onClose()
  }

  return (
    <Modal badge="YAML Diff" title={name} onClose={onClose} size="2xl">
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as Mode)}
            className="bg-kb-elevated border border-kb-border rounded px-2 py-1 text-[11px] font-mono text-kb-text-secondary"
          >
            <option value="prev-vs-this">
              Previous{previous ? ` (rev ${previous.revision})` : ''} vs This (rev {targetRevision})
            </option>
            <option value="this-vs-current">
              This (rev {targetRevision}) vs Current{current ? ` (rev ${current.revision})` : ''}
            </option>
          </select>
          {onRollback && (
            <div className="flex items-center gap-2">
              {left && !left.active && (
                <button
                  onClick={() => rollback(left.revision)}
                  disabled={!canEdit}
                  title={!canEdit ? 'Editor role required' : `Rollback to revision ${left.revision}`}
                  className="px-2 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors inline-flex items-center gap-1 disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  <RotateCcw className="w-3 h-3" /> Rollback to {left.revision}
                </button>
              )}
              {right && !right.active && (
                <button
                  onClick={() => rollback(right.revision)}
                  disabled={!canEdit}
                  title={!canEdit ? 'Editor role required' : `Rollback to revision ${right.revision}`}
                  className="px-2 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors inline-flex items-center gap-1 disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  <RotateCcw className="w-3 h-3" /> Rollback to {right.revision}
                </button>
              )}
            </div>
          )}
        </div>

        {isLoading ? (
          <LoadingSpinner />
        ) : error ? (
          <ErrorState message={error.message} />
        ) : !target ? (
          <div className="text-sm text-kb-text-tertiary text-center py-12">Revision {targetRevision} not found in history.</div>
        ) : !bothPresent ? (
          <div className="text-sm text-kb-text-tertiary text-center py-12">
            {mode === 'prev-vs-this' && !previous
              ? 'This is the earliest available revision — nothing to compare against. Try "This vs Current".'
              : 'Manifest unavailable for one of the revisions (older revisions may have been pruned).'}
          </div>
        ) : identical ? (
          <div className="text-sm text-kb-text-tertiary text-center py-12">No changes between these revisions.</div>
        ) : (
          <div
            className="overflow-auto max-h-[70vh] rounded-lg border border-kb-border p-2"
            style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}
          >
            <div className="text-[11px] font-mono leading-5">
              {rows.map((r, i) => (
                <div key={i} className="flex">
                  <DiffCell
                    no={r.leftNo}
                    line={r.left}
                    removed={(r.kind === 'removed' || r.kind === 'change') && r.left != null}
                    added={false}
                  />
                  <div className="w-px bg-kb-border shrink-0 mx-1" />
                  <DiffCell
                    no={r.rightNo}
                    line={r.right}
                    removed={false}
                    added={(r.kind === 'added' || r.kind === 'change') && r.right != null}
                  />
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}
