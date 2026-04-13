import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { useDeploymentPods, useStatefulSetPods, useDaemonSetPods, useJobPods } from '@/hooks/useResources'
import { getAccessToken } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import type { ResourceItem } from '@/types/kubernetes'

const SHELLS = [
  { value: '', label: 'Auto (bash → sh)' },
  { value: '/bin/bash', label: '/bin/bash' },
  { value: '/bin/sh', label: '/bin/sh' },
]

function buildExecUrl(namespace: string, name: string, container: string, shell: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const params = new URLSearchParams()
  if (container) params.set('container', container)
  if (shell) params.set('shell', shell)
  // Attach auth token for WebSocket authentication
  const token = getAccessToken()
  if (token) params.set('token', token)
  return `${proto}//${window.location.host}/ws/exec/${namespace}/${name}?${params}`
}

const TERM_THEME = {
  background: '#0d1117',
  foreground: '#c9d1d9',
  cursor: '#c9d1d9',
  selectionBackground: '#264f78',
  black: '#484f58',
  red: '#ff7b72',
  green: '#3fb950',
  yellow: '#d29922',
  blue: '#58a6ff',
  magenta: '#bc8cff',
  cyan: '#39d353',
  white: '#b1bac4',
  brightBlack: '#6e7681',
  brightRed: '#ffa198',
  brightGreen: '#56d364',
  brightYellow: '#e3b341',
  brightBlue: '#79c0ff',
  brightMagenta: '#d2a8ff',
  brightCyan: '#56d364',
  brightWhite: '#f0f6fc',
}

interface TerminalTabProps {
  namespace: string
  name: string
  item: ResourceItem
}

export function TerminalTab({ namespace, name, item }: TerminalTabProps) {
  const containers = Array.isArray(item.containers)
    ? (item.containers as Array<Record<string, unknown>>).map(c => String(c.name ?? ''))
    : []
  const [selectedContainer, setSelectedContainer] = useState(containers[0] ?? '')
  const [selectedShell, setSelectedShell] = useState('')
  const [sessionActive, setSessionActive] = useState(false)
  const [sessionKey, setSessionKey] = useState(0)

  const { hasRole } = useAuth()
  const podStatus = String(item.status ?? '').toLowerCase()
  const canExec = podStatus === 'running' && hasRole('editor')

  function handleConnect() {
    setSessionKey(k => k + 1)
    setSessionActive(true)
  }

  function handleDisconnect() {
    setSessionActive(false)
  }

  return (
    <div className="space-y-3">
      {/* Controls */}
      <div className="flex items-center gap-2 flex-wrap">
        {containers.length > 1 && (
          <select
            value={selectedContainer}
            onChange={(e) => { setSelectedContainer(e.target.value); handleDisconnect() }}
            disabled={sessionActive}
            className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary disabled:opacity-50"
          >
            {containers.map(cn => (
              <option key={cn} value={cn}>{cn}</option>
            ))}
          </select>
        )}
        {containers.length === 1 && (
          <span className="text-xs font-mono text-kb-text-secondary">{containers[0]}</span>
        )}

        <select
          value={selectedShell}
          onChange={(e) => { setSelectedShell(e.target.value); handleDisconnect() }}
          disabled={sessionActive}
          className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary disabled:opacity-50"
        >
          {SHELLS.map(sh => (
            <option key={sh.value} value={sh.value}>{sh.label}</option>
          ))}
        </select>

        {!sessionActive ? (
          <button
            onClick={handleConnect}
            disabled={!canExec}
            className="px-3 py-1.5 text-xs font-mono bg-status-ok-dim text-status-ok border border-status-ok/20 rounded-lg hover:bg-status-ok/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Connect
          </button>
        ) : (
          <button
            onClick={handleDisconnect}
            className="px-3 py-1.5 text-xs font-mono bg-status-error-dim text-status-error border border-status-error/20 rounded-lg hover:bg-status-error/20 transition-colors"
          >
            Disconnect
          </button>
        )}

        {!canExec && (
          <span className="text-[10px] font-mono text-status-warn">
            {!hasRole('editor') ? 'Editor role required for terminal access' : 'Pod is not running'}
          </span>
        )}

        {sessionActive && (
          <div className="flex items-center gap-1.5 ml-auto">
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-status-ok opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-status-ok" />
            </span>
            <span className="text-[10px] text-kb-text-tertiary">Connected</span>
          </div>
        )}
      </div>

      {/* Terminal session — stays mounted to preserve error messages */}
      {sessionActive ? (
        <TerminalSession
          key={sessionKey}
          namespace={namespace}
          name={name}
          container={selectedContainer}
          shell={selectedShell}
          onDisconnect={handleDisconnect}
        />
      ) : (
        <div
          className="rounded-[10px] border border-kb-border flex items-center justify-center text-kb-text-tertiary text-xs font-mono"
          style={{ backgroundColor: '#0d1117', minHeight: '400px' }}
        >
          {canExec ? 'Click Connect to start a terminal session' : !hasRole('editor') ? 'Editor role required for terminal access' : 'Pod must be running to use the terminal'}
        </div>
      )}
    </div>
  )
}

// ─── Terminal Session (mounts/unmounts cleanly) ──────────────

function TerminalSession({
  namespace, name, container, shell, onDisconnect,
}: {
  namespace: string
  name: string
  container: string
  shell: string
  onDisconnect: () => void
}) {
  const termRef = useRef<HTMLDivElement>(null)
  const onDisconnectRef = useRef(onDisconnect)
  onDisconnectRef.current = onDisconnect

  useEffect(() => {
    if (!termRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
      theme: TERM_THEME,
      allowProposedApi: true,
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())
    term.open(termRef.current)
    fitAddon.fit()
    term.focus()

    term.writeln('\x1b[1;34m●\x1b[0m Connecting to \x1b[1m' + name + '\x1b[0m / \x1b[33m' + container + '\x1b[0m ...')
    term.writeln('')

    const url = buildExecUrl(namespace, name, container, shell)
    let ws: WebSocket | null = null
    let disposed = false
    let serverClosed = false
    let firstOutput = true
    let initialFlush: { buffered: string[]; timer: ReturnType<typeof setTimeout> } | null = null

    const connectTimer = setTimeout(() => {
      if (disposed) return
      ws = new WebSocket(url)

      ws.onopen = () => {
        // Send resize before any output arrives so the shell knows the terminal size
        const dims = fitAddon.proposeDimensions()
        if (dims) {
          ws!.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }))
        }
      }

      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data)
          switch (msg.type) {
            case 'stdout':
            case 'stderr':
              if (firstOutput) {
                firstOutput = false
                // Buffer initial output briefly to absorb the duplicate prompt
                // caused by the shell printing prompt before and after resize SIGWINCH
                const buffered = [msg.data]
                const flushTimer = setTimeout(() => {
                  term.reset()
                  term.writeln('\x1b[1;32m●\x1b[0m Connected to \x1b[1m' + name + '\x1b[0m / \x1b[33m' + container + '\x1b[0m')
                  term.writeln('')
                  // Write only the last prompt (skip duplicate)
                  const all = buffered.join('')
                  // Find the last occurrence of the prompt pattern and write from there
                  const lines = all.split(/\r?\n/)
                  const lastPrompt = lines.length > 1 ? lines[lines.length - 1] : all
                  term.write(lastPrompt)
                  initialFlush = null
                }, 150)
                initialFlush = { buffered, timer: flushTimer }
                break
              }
              if (initialFlush) {
                initialFlush.buffered.push(msg.data)
                break
              }
              term.write(msg.data)
              break
            case 'exit':
              serverClosed = true
              if (initialFlush) {
                clearTimeout(initialFlush.timer)
                initialFlush = null
              }
              term.writeln('')
              term.writeln(`\x1b[1;${msg.code === 0 ? '32' : '31'}m●\x1b[0m Session ended`)
              // Auto-disconnect after a moment so the message is visible
              setTimeout(() => { if (!disposed) onDisconnectRef.current() }, 1500)
              break
            case 'error':
              serverClosed = true
              if (initialFlush) {
                clearTimeout(initialFlush.timer)
                initialFlush = null
              }
              term.reset()
              term.writeln('\x1b[1;31m●\x1b[0m ' + msg.message)
              term.writeln('')
              // Countdown before auto-disconnect
              let countdown = 3
              term.write(`\x1b[90mDisconnecting in ${countdown}s...\x1b[0m`)
              const countdownTimer = setInterval(() => {
                countdown--
                if (countdown > 0) {
                  term.write(`\r\x1b[90mDisconnecting in ${countdown}s...\x1b[0m`)
                } else {
                  clearInterval(countdownTimer)
                  term.write(`\r\x1b[90mDisconnected.            \x1b[0m\r\n`)
                  if (!disposed) onDisconnectRef.current()
                }
              }, 1000)
              break
          }
        } catch {
          term.write(event.data)
        }
      }

      ws.onclose = () => {
        // Never call onDisconnect from onclose — it fires during cleanup/navigation
        // and causes state updates on unmounted components. The parent handles
        // disconnect via the Disconnect button or server-initiated exit/error only.
        if (!disposed && !serverClosed) {
          term.writeln('')
          term.writeln('\x1b[1;33m●\x1b[0m Connection closed')
        }
      }

      ws.onerror = () => {
        if (!disposed && !serverClosed) {
          term.writeln('')
          term.writeln('\x1b[1;31m●\x1b[0m Connection error')
        }
      }
    }, 50)

    // Forward terminal input
    const dataDisposable = term.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'stdin', data }))
      }
    })

    // Handle resize
    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit()
      const dims = fitAddon.proposeDimensions()
      if (dims && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }))
      }
    })
    resizeObserver.observe(termRef.current)

    // Cleanup on unmount
    return () => {
      disposed = true
      clearTimeout(connectTimer)
      if (initialFlush) clearTimeout(initialFlush.timer)
      try { resizeObserver.disconnect() } catch {}
      try { dataDisposable.dispose() } catch {}
      try {
        if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
          ws.close()
        }
      } catch {}
      try { term.dispose() } catch {}
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namespace, name, container, shell])

  return (
    <div
      ref={termRef}
      className="rounded-[10px] border border-kb-border overflow-hidden"
      style={{ backgroundColor: '#0d1117', minHeight: '400px' }}
    />
  )
}

// ─── Workload Terminal Wrappers ────────────────────────────────

function WorkloadTerminalTab({ pods, isLoading, error }: { pods: ResourceItem[]; isLoading: boolean; error: Error | null }) {
  const [selectedPod, setSelectedPod] = useState('')

  useEffect(() => {
    if (pods.length > 0 && !selectedPod) {
      setSelectedPod(pods[0].name)
    }
  }, [pods, selectedPod])

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />
  if (pods.length === 0) return <div className="text-center text-xs text-kb-text-tertiary py-8">No pods found</div>

  const pod = pods.find(p => p.name === selectedPod) ?? pods[0]

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <select
          value={selectedPod}
          onChange={(e) => setSelectedPod(e.target.value)}
          className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
        >
          {pods.map(p => (
            <option key={p.name} value={p.name}>{p.name}</option>
          ))}
        </select>
      </div>
      <TerminalTab namespace={pod.namespace} name={pod.name} item={pod} />
    </div>
  )
}

export function DeploymentTerminalTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDeploymentPods(namespace, name)
  return <WorkloadTerminalTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

export function StatefulSetTerminalTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useStatefulSetPods(namespace, name)
  return <WorkloadTerminalTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

export function DaemonSetTerminalTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDaemonSetPods(namespace, name)
  return <WorkloadTerminalTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}
