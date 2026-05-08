import { useEffect, useMemo, useRef, useState } from 'react'
import { Eye, EyeOff, AlertTriangle, Info, Download, ShieldAlert } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type { SecretRevealResponse } from '@/services/api'
import type { ResourceItem } from '@/types/kubernetes'

// SecretRevealModal — UI for the Tier 2 #9 secret-reveal flow.
//
// Two phases in one modal:
//
//   1. CONSENT — operator picks which keys to reveal (default: all)
//      and types a reason (≥10 chars). The reason goes to the audit
//      log and is the audit story's "why this reveal happened."
//      Without it, the audit channel records WHO and WHAT but not
//      WHY, which significantly weakens compliance review value.
//
//   2. REVEALED — values are shown inline with per-key Hide buttons
//      and a single auto-hide timer that re-redacts everything after
//      60 seconds of inactivity. The timer resets on any user
//      interaction (mouse move, keypress, scroll inside the modal).
//      Operators reading the value actively keep it visible; an
//      operator who walks away from the screen has it auto-redacted
//      before someone else sits down.
//
// Critical UX detail: hiding a value LOCALLY does not unlog the
// access. The audit log entry was already written when the operator
// clicked Reveal; subsequent show/hide is just visual control. The
// modal makes this explicit in the help text so operators don't
// expect "I'll just hide it before anyone sees" to retroactively
// undo the audit.
//
// Production-namespace gating happens server-side. If the operator's
// role doesn't satisfy the prod-namespace requirement (Editor in a
// prod namespace), the reveal request returns 403 and we surface
// the message verbatim.

const AUTO_HIDE_MS = 60_000

const PROD_NS_HINT_RE = /^(prod|production|prd)([-_].+)?$/i

interface KeyState {
  // selected: in the consent phase, whether the operator wants this
  // key in the reveal request (defaults to true for all keys).
  selected: boolean
  // visible: in the revealed phase, whether the value is currently
  // shown or locally re-redacted. Initial state is true (just-revealed
  // values default to visible); operator can re-redact each key
  // independently via the Hide button.
  visible: boolean
}

export function SecretRevealModal({
  namespace,
  name,
  resource,
  onClose,
}: {
  namespace: string
  name: string
  resource: ResourceItem | undefined
  onClose: () => void
}) {
  // Available keys come from the resource detail's `keys` field
  // (comma-separated string) — same source the InfoField in the
  // detail page uses.
  const availableKeys = useMemo(() => {
    const raw = (resource as unknown as { keys?: string })?.keys
    if (!raw) return []
    return String(raw)
      .split(',')
      .map((k) => k.trim())
      .filter(Boolean)
  }, [resource])

  const [keys, setKeys] = useState<Record<string, KeyState>>(() => {
    const init: Record<string, KeyState> = {}
    for (const k of availableKeys) {
      init[k] = { selected: true, visible: true }
    }
    return init
  })

  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [revealed, setRevealed] = useState<SecretRevealResponse | null>(null)

  // Re-init key state if availableKeys changes after mount (rare —
  // happens if the underlying Secret is rotated mid-modal). Operator
  // sees the new key set without losing the modal context.
  useEffect(() => {
    setKeys((prev) => {
      const next: Record<string, KeyState> = {}
      for (const k of availableKeys) {
        next[k] = prev[k] ?? { selected: true, visible: true }
      }
      return next
    })
  }, [availableKeys])

  const selectedKeys = useMemo(
    () => Object.entries(keys).filter(([, v]) => v.selected).map(([k]) => k),
    [keys],
  )
  const allSelected = availableKeys.length > 0 && selectedKeys.length === availableKeys.length

  const reasonValid = reason.trim().length >= 10
  const isProdHint = PROD_NS_HINT_RE.test(namespace)

  // Auto-hide timers — per-key, fixed 60s window since each key
  // became visible. Initial in-vivo testing surfaced that gating the
  // global timer on "user activity" (mousemove / keydown / scroll)
  // made the auto-hide unpredictable: even casual mouse movement
  // over the modal kept resetting the timer indefinitely. The fixed
  // window is the safer semantic for shoulder-surfing protection —
  // 60 seconds, no exceptions, regardless of whether the operator
  // is interacting with the modal. If they need more time, click
  // "show" again and a fresh 60s starts for that specific key.
  const perKeyTimers = useRef<Map<string, number>>(new Map())

  const clearKeyTimer = (k: string) => {
    const t = perKeyTimers.current.get(k)
    if (t !== undefined) {
      window.clearTimeout(t)
      perKeyTimers.current.delete(k)
    }
  }
  const scheduleKeyHide = (k: string) => {
    clearKeyTimer(k)
    const t = window.setTimeout(() => {
      setKeys((prev) => ({ ...prev, [k]: { ...prev[k], visible: false } }))
      perKeyTimers.current.delete(k)
    }, AUTO_HIDE_MS)
    perKeyTimers.current.set(k, t)
  }

  // On reveal: schedule per-key timers for every text value. Binary
  // values are never "visible" in the readable sense (they show the
  // sha256 placeholder), so they don't need an auto-hide.
  useEffect(() => {
    if (!revealed) return
    for (const v of revealed.values) {
      if (v.kind === 'text') {
        scheduleKeyHide(v.key)
      }
    }
    return () => {
      for (const t of perKeyTimers.current.values()) {
        window.clearTimeout(t)
      }
      perKeyTimers.current.clear()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealed])

  function toggleKeySelected(k: string) {
    setKeys((prev) => ({
      ...prev,
      [k]: { ...prev[k], selected: !prev[k].selected },
    }))
  }

  function selectAll() {
    setKeys((prev) => {
      const next: Record<string, KeyState> = {}
      for (const k of Object.keys(prev)) next[k] = { ...prev[k], selected: true }
      return next
    })
  }
  function selectNone() {
    setKeys((prev) => {
      const next: Record<string, KeyState> = {}
      for (const k of Object.keys(prev)) next[k] = { ...prev[k], selected: false }
      return next
    })
  }

  async function submit() {
    if (!reasonValid) {
      setError('Reason must be at least 10 characters')
      return
    }
    if (selectedKeys.length === 0) {
      setError('Pick at least one key to reveal')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const body = {
        keys: allSelected ? undefined : selectedKeys,
        reason: reason.trim(),
      }
      const res = await api.revealSecret(namespace, name, body)
      // Reset visibility — every revealed key starts visible. Subsequent
      // operator action toggles each independently.
      setKeys((prev) => {
        const next = { ...prev }
        for (const v of res.values) {
          next[v.key] = { selected: true, visible: true }
        }
        return next
      })
      setRevealed(res)
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message
      setError(msg)
    } finally {
      setBusy(false)
    }
  }

  function toggleVisible(k: string) {
    const wasVisible = keys[k]?.visible ?? false
    setKeys((prev) => ({ ...prev, [k]: { ...prev[k], visible: !wasVisible } }))
    // Show after auto-hide → start a fresh 60s window for this key.
    // Hide via click → cancel the pending auto-hide (no point firing
    // a "hide" timer on a key the operator already hid).
    if (!wasVisible) {
      scheduleKeyHide(k)
    } else {
      clearKeyTimer(k)
    }
  }

  // Tracks which key just got copied, so the button can show
  // "copied!" feedback for ~1.5s before reverting. Without it the
  // copy click is a silent no-op visually — operators can't tell
  // whether the clipboard write actually happened.
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const copiedTimerRef = useRef<number | null>(null)
  function copy(k: string, text: string) {
    navigator.clipboard.writeText(text).catch(() => {
      // Clipboard write can fail on insecure contexts; surface a
      // friendly error rather than a silent no-op.
      setError('Could not copy to clipboard')
      return
    })
    setCopiedKey(k)
    if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
    copiedTimerRef.current = window.setTimeout(() => {
      setCopiedKey(null)
      copiedTimerRef.current = null
    }, 1500)
  }
  // Cleanup on unmount: cancel any in-flight "copied!" timeout so a
  // fast unmount + remount doesn't leave a dangling timer.
  useEffect(() => {
    return () => {
      if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
    }
  }, [])

  function downloadBinary(key: string) {
    // For binary values we don't have the bytes locally — only the
    // hash + length. Wire a future "download binary" endpoint here;
    // for v1 we just inform the operator. Documented as v2 follow-up
    // in the spec.
    setError(`Binary download for "${key}" is not yet implemented in v1. Use kubectl get secret to retrieve binary values.`)
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 text-status-warn">
          <Eye className="w-3 h-3" /> reveal secret
        </span>
      }
      title={`Reveal · ${name}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        {/* Production namespace banner — pure UI hint; the actual
            authorization check is server-side. The hint helps the
            operator anticipate a denial before submitting. */}
        {isProdHint && (
          <div className="flex items-start gap-2 text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-2.5">
            <ShieldAlert className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold">Production namespace</div>
              Reveals here require Admin role and emit a high-priority audit entry. Editor accounts are denied at the server.
            </div>
          </div>
        )}

        {!revealed ? (
          <>
            {/* CONSENT phase */}
            <p className="text-xs text-kb-text-tertiary">
              Pick the keys to reveal and provide a reason. The keys, your identity, and the reason go to the audit log. The decoded values do <strong>not</strong>: they flow only over the TLS connection to your browser.
            </p>

            <div>
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
                  Keys ({selectedKeys.length}/{availableKeys.length})
                </span>
                <div className="flex items-center gap-2 text-[11px]">
                  <button onClick={selectAll} className="text-status-info hover:underline">
                    All
                  </button>
                  <span className="text-kb-text-tertiary">·</span>
                  <button onClick={selectNone} className="text-kb-text-tertiary hover:underline">
                    None
                  </button>
                </div>
              </div>
              {availableKeys.length === 0 ? (
                <div className="text-[11px] text-kb-text-tertiary border border-kb-border rounded-lg px-3 py-3 text-center">
                  This Secret has no data keys to reveal.
                </div>
              ) : (
                <div className="border border-kb-border rounded-lg overflow-hidden">
                  {availableKeys.map((k) => (
                    <label
                      key={k}
                      className="flex items-center gap-2 px-3 py-2 border-b border-kb-border last:border-b-0 cursor-pointer hover:bg-kb-elevated"
                    >
                      <input
                        type="checkbox"
                        checked={keys[k]?.selected ?? false}
                        onChange={() => toggleKeySelected(k)}
                        className="accent-status-info"
                      />
                      <span className="font-mono text-[11px] text-kb-text-primary break-all">{k}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>

            <div>
              <label className="block">
                <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
                  Reason for revealing (required, ≥10 chars)
                </span>
                <textarea
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  rows={2}
                  maxLength={500}
                  placeholder="e.g. Validating rotation succeeded, Debugging connection refused error in payments-api"
                  className={`mt-1 w-full text-[11px] font-mono bg-kb-bg border rounded px-2 py-1.5 text-kb-text-primary outline-none focus:border-kb-border-active resize-none ${
                    reason.length === 0 ? 'border-kb-border' : reasonValid ? 'border-kb-border' : 'border-status-error/50'
                  }`}
                />
                <div className="mt-1 flex justify-between text-[10px] text-kb-text-tertiary">
                  <span>
                    {reason.length < 10
                      ? `${10 - reason.length} more characters needed`
                      : 'looks good'}
                  </span>
                  <span>{reason.length}/500</span>
                </div>
              </label>
            </div>
          </>
        ) : (
          <>
            {/* REVEALED phase */}
            {/* Status callout — two-line hierarchy. Wrapping the text
                in a single child element is intentional: the parent's
                `flex items-start gap-2` would otherwise treat each
                text segment around <strong> as a separate flex item,
                producing a side-by-side column layout instead of
                inline text. */}
            <div className="text-[11px] text-kb-text-tertiary border border-kb-border bg-kb-elevated rounded-md p-2.5 flex items-start gap-2">
              <Info className="w-3.5 h-3.5 mt-0.5 shrink-0" />
              <div className="space-y-0.5 leading-relaxed">
                <div className="text-kb-text-secondary">
                  Revealed at <span className="font-mono text-kb-text-primary">{new Date(revealed.revealedAt).toLocaleTimeString()}</span>. Each value auto-hides 60 seconds after becoming visible.
                </div>
                <div>
                  Hiding locally doesn't unlog the access — the audit entry was written when you clicked Reveal.
                </div>
              </div>
            </div>

            {revealed.missing.length > 0 && (
              <div className="text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-2.5">
                <strong>Missing keys:</strong>{' '}
                <span className="font-mono">{revealed.missing.join(', ')}</span> — not present on this Secret.
              </div>
            )}

            <div className="border border-kb-border rounded-lg overflow-hidden">
              <table className="w-full text-xs">
                <thead className="bg-kb-elevated border-b border-kb-border">
                  <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                    <th className="px-3 py-2 font-medium w-40">Key</th>
                    <th className="px-3 py-2 font-medium">Value</th>
                    <th className="px-3 py-2 font-medium w-24"></th>
                  </tr>
                </thead>
                <tbody>
                  {revealed.values.map((v) => {
                    const visible = keys[v.key]?.visible ?? false
                    return (
                      <tr key={v.key} className="border-b border-kb-border last:border-b-0">
                        <td className="px-3 py-2 font-mono text-[11px] text-kb-text-primary break-all">
                          {v.key}
                        </td>
                        <td className="px-3 py-2 font-mono text-[11px] text-kb-text-secondary break-all">
                          {v.kind === 'binary' ? (
                            <span className="text-kb-text-tertiary">
                              binary · {v.bytes} bytes · sha256: <span className="font-mono text-[10px]">{v.sha256?.slice(0, 16)}…</span>
                            </span>
                          ) : visible ? (
                            <span className="whitespace-pre-wrap">{v.value}</span>
                          ) : (
                            <span className="text-kb-text-tertiary">•••••</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right">
                          <div className="flex justify-end gap-1.5">
                            {v.kind === 'binary' ? (
                              <button
                                type="button"
                                onClick={() => downloadBinary(v.key)}
                                className="text-[10px] text-status-info hover:underline flex items-center gap-1"
                              >
                                <Download className="w-3 h-3" />
                                save
                              </button>
                            ) : (
                              <>
                                <button
                                  type="button"
                                  onClick={() => toggleVisible(v.key)}
                                  className="text-[10px] text-kb-text-secondary hover:text-kb-text-primary flex items-center gap-1"
                                  title={visible ? 'Hide locally (audit was already logged)' : 'Show again locally'}
                                >
                                  {visible ? <EyeOff className="w-3 h-3" /> : <Eye className="w-3 h-3" />}
                                  {visible ? 'hide' : 'show'}
                                </button>
                                {visible && v.value !== undefined && (
                                  <button
                                    type="button"
                                    onClick={() => copy(v.key, v.value!)}
                                    className={`text-[10px] transition-colors ${
                                      copiedKey === v.key
                                        ? 'text-status-ok cursor-default'
                                        : 'text-status-info hover:underline'
                                    }`}
                                    title={copiedKey === v.key ? 'Copied to clipboard' : 'Copy value to clipboard'}
                                  >
                                    {copiedKey === v.key ? 'copied!' : 'copy'}
                                  </button>
                                )}
                              </>
                            )}
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </>
        )}

        {error && (
          <div className="flex items-center gap-2 text-xs text-status-error">
            <AlertTriangle className="w-3 h-3" />
            <span className="break-words">{error}</span>
          </div>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        <button
          onClick={onClose}
          disabled={busy}
          className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
        >
          {revealed ? 'Close' : 'Cancel'}
        </button>
        {!revealed && (
          <button
            onClick={submit}
            disabled={busy || !reasonValid || selectedKeys.length === 0}
            className="px-3 py-1.5 text-xs rounded bg-status-warn-dim text-status-warn hover:bg-status-warn hover:text-kb-bg border border-status-warn disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            <Eye className="w-3 h-3" />
            {busy ? 'Revealing…' : 'Reveal'}
          </button>
        )}
      </div>
    </Modal>
  )
}
