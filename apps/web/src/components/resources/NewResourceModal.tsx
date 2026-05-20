import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Plus, AlertTriangle, FileCode, RefreshCw } from 'lucide-react'
import { EditorView, lineNumbers } from '@codemirror/view'
import { yaml as yamlLang } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type { ResourceList } from '@/types/kubernetes'

// NewResourceModal — UI for the Tier 2 #10 apply-new-manifest flow.
//
// Three controls + an editor:
//
//   1. Kind picker — common kinds pinned at top, the rest in
//      alphabetical order. Cluster-scoped kinds (Namespace,
//      StorageClass, ClusterRole, ClusterRoleBinding) hide the
//      namespace picker.
//
//   2. Namespace picker — populated from the namespaces API. Only
//      visible for namespaced kinds.
//
//   3. Starter template chips — kind-aware curated manifests baked
//      into the frontend. Picking a chip replaces the editor's
//      content. Default chip on kind change is "Blank" (minimal
//      apiVersion/kind/metadata.name shell).
//
//   4. CodeMirror YAML editor with the same theme/font as the YAML
//      tab on resource detail pages. The editor's content is the
//      raw manifest body shipped to POST /resources/:type/:ns.
//
// The backend's URL/body consistency check means a typo in apiVersion
// or kind here surfaces as a clean "URL says X, body says Y" 400
// before the apiserver round-trip — the operator can correct without
// guessing what went wrong.

// Cluster-scoped kinds — hide the namespace picker for these. Mirrors
// cluster/connector.go's isClusterScoped; if a future PR adds a new
// cluster-scoped kind to that list, this set must be updated too or
// the modal will incorrectly require a namespace selection.
const CLUSTER_SCOPED_TYPES = new Set([
  'namespaces',
  'nodes',
  'persistentvolumes',
  'pvs',
  'storageclasses',
  'clusterroles',
  'clusterrolebindings',
])

// Common kinds shown at the top of the picker. Order is the same as
// the operator's typical mental model for "what do I create most?"
const COMMON_KINDS: { type: string; label: string }[] = [
  { type: 'pods', label: 'Pod' },
  { type: 'deployments', label: 'Deployment' },
  { type: 'services', label: 'Service' },
  { type: 'configmaps', label: 'ConfigMap' },
  { type: 'secrets', label: 'Secret' },
  { type: 'jobs', label: 'Job' },
  { type: 'cronjobs', label: 'CronJob' },
  { type: 'ingresses', label: 'Ingress' },
]

const OTHER_KINDS: { type: string; label: string }[] = [
  { type: 'statefulsets', label: 'StatefulSet' },
  { type: 'daemonsets', label: 'DaemonSet' },
  { type: 'persistentvolumeclaims', label: 'PersistentVolumeClaim' },
  { type: 'horizontalpodautoscalers', label: 'HorizontalPodAutoscaler' },
  { type: 'networkpolicies', label: 'NetworkPolicy' },
  { type: 'namespaces', label: 'Namespace' },
  { type: 'storageclasses', label: 'StorageClass' },
  { type: 'roles', label: 'Role' },
  { type: 'rolebindings', label: 'RoleBinding' },
  { type: 'clusterroles', label: 'ClusterRole' },
  { type: 'clusterrolebindings', label: 'ClusterRoleBinding' },
]

interface StarterTemplate {
  id: string
  label: string
  manifest: string
}

// Hand-curated starter templates per kind. Each one is a working,
// minimally-complete manifest the operator can apply as-is or edit.
// Names are illustrative ("netshoot", "nginx") — the operator can
// rename in the editor.
//
// Keep these tight: starter templates are operational shortcuts, not
// a manifest catalog. If a real catalog is needed, that's a v2
// surface (Enterprise candidate per the spec — org-required templates
// per kind).
const STARTERS: Record<string, StarterTemplate[]> = {
  pods: [
    {
      id: 'pods-blank',
      label: 'Blank',
      manifest: `apiVersion: v1
kind: Pod
metadata:
  name: my-pod
spec:
  containers:
    - name: app
      image: busybox:1.36
      command: ["sh", "-c", "sleep infinity"]
`,
    },
    {
      id: 'pods-netshoot',
      label: 'Debug pod (netshoot)',
      manifest: `apiVersion: v1
kind: Pod
metadata:
  name: netshoot
spec:
  containers:
    - name: netshoot
      image: nicolaka/netshoot
      command: ["sleep", "infinity"]
  restartPolicy: Never
`,
    },
  ],
  deployments: [
    {
      id: 'deployments-blank',
      label: 'Blank',
      manifest: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-deployment
  template:
    metadata:
      labels:
        app: my-deployment
    spec:
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "sleep infinity"]
`,
    },
    {
      id: 'deployments-nginx',
      label: 'nginx',
      manifest: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports:
            - containerPort: 80
`,
    },
  ],
  services: [
    {
      id: 'services-clusterip',
      label: 'ClusterIP',
      manifest: `apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  type: ClusterIP
  selector:
    app: my-app
  ports:
    - port: 80
      targetPort: 8080
`,
    },
  ],
  configmaps: [
    {
      id: 'configmaps-blank',
      label: 'Blank',
      manifest: `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
data:
  KEY: value
`,
    },
  ],
  secrets: [
    {
      id: 'secrets-opaque',
      label: 'Opaque (stringData)',
      manifest: `apiVersion: v1
kind: Secret
metadata:
  name: my-secret
type: Opaque
stringData:
  password: changeme
`,
    },
  ],
  jobs: [
    {
      id: 'jobs-blank',
      label: 'Blank',
      manifest: `apiVersion: batch/v1
kind: Job
metadata:
  name: my-job
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: job
          image: busybox:1.36
          command: ["sh", "-c", "echo hello && sleep 3"]
  backoffLimit: 1
`,
    },
  ],
  cronjobs: [
    {
      id: 'cronjobs-blank',
      label: 'Blank (every 5m)',
      manifest: `apiVersion: batch/v1
kind: CronJob
metadata:
  name: my-cronjob
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: job
              image: busybox:1.36
              command: ["sh", "-c", "date"]
`,
    },
  ],
  ingresses: [
    {
      id: 'ingresses-blank',
      label: 'Basic (nginx class)',
      manifest: `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-ingress
spec:
  ingressClassName: nginx
  rules:
    - host: example.local
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-service
                port:
                  number: 80
`,
    },
  ],
  // NetworkPolicy starters — three patterns ordered by how often
  // an operator types them. "Default deny" is THE first NP most
  // teams ship (regulated-industry default posture). "Allow same
  // namespace" is the natural relaxation once deny is in place.
  // "Allow from specific app" is the working illustration of the
  // podSelector + ingress.from.podSelector pattern most newcomers
  // get wrong on the first try.
  networkpolicies: [
    {
      id: 'np-default-deny',
      label: 'Default deny all',
      manifest: `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
spec:
  # Empty podSelector = applies to ALL pods in the namespace.
  podSelector: {}
  policyTypes:
    - Ingress
    - Egress
`,
    },
    {
      id: 'np-allow-same-ns',
      label: 'Allow same namespace',
      manifest: `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-same-namespace
spec:
  podSelector: {}
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector: {}
`,
    },
    {
      id: 'np-allow-from-app',
      label: 'Allow from specific app',
      manifest: `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-from-frontend
spec:
  # Pods this policy protects (the "destination" side).
  podSelector:
    matchLabels:
      app: backend
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: frontend
      ports:
        - protocol: TCP
          port: 8080
`,
    },
  ],
  statefulsets: [
    {
      id: 'sts-blank',
      label: 'Blank',
      manifest: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-sts
spec:
  serviceName: my-sts
  replicas: 1
  selector:
    matchLabels:
      app: my-sts
  template:
    metadata:
      labels:
        app: my-sts
    spec:
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "sleep infinity"]
`,
    },
  ],
  daemonsets: [
    {
      id: 'ds-blank',
      label: 'Blank',
      manifest: `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: my-ds
spec:
  selector:
    matchLabels:
      app: my-ds
  template:
    metadata:
      labels:
        app: my-ds
    spec:
      tolerations:
        - operator: Exists
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "sleep infinity"]
`,
    },
  ],
  namespaces: [
    {
      id: 'ns-blank',
      label: 'Blank',
      manifest: `apiVersion: v1
kind: Namespace
metadata:
  name: my-namespace
`,
    },
  ],
  persistentvolumeclaims: [
    {
      id: 'pvc-blank',
      label: 'Blank (1Gi RWO)',
      manifest: `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
`,
    },
  ],
  horizontalpodautoscalers: [
    {
      id: 'hpa-blank',
      label: 'Blank (CPU 80%)',
      manifest: `apiVersion: autoscaling/v1
kind: HorizontalPodAutoscaler
metadata:
  name: my-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-deployment
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80
`,
    },
  ],
  storageclasses: [
    {
      id: 'sc-blank',
      label: 'Blank',
      manifest: `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: my-storageclass
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: WaitForFirstConsumer
`,
    },
  ],
  roles: [
    {
      id: 'role-blank',
      label: 'Blank',
      manifest: `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: my-role
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
`,
    },
  ],
  rolebindings: [
    {
      id: 'rb-blank',
      label: 'Blank',
      manifest: `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: my-rolebinding
subjects:
  - kind: User
    name: someone@example.com
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: Role
  name: my-role
  apiGroup: rbac.authorization.k8s.io
`,
    },
  ],
  clusterroles: [
    {
      id: 'cr-blank',
      label: 'Blank',
      manifest: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: my-clusterrole
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list"]
`,
    },
  ],
  clusterrolebindings: [
    {
      id: 'crb-blank',
      label: 'Blank',
      manifest: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: my-clusterrolebinding
subjects:
  - kind: ServiceAccount
    name: default
    namespace: default
roleRef:
  kind: ClusterRole
  name: my-clusterrole
  apiGroup: rbac.authorization.k8s.io
`,
    },
  ],
}

export function NewResourceModal({
  defaultType,
  defaultNamespace,
  onClose,
}: {
  defaultType?: string
  defaultNamespace?: string
  onClose: () => void
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [type, setType] = useState<string>(defaultType ?? 'pods')
  const [namespace, setNamespace] = useState<string>(defaultNamespace ?? 'default')
  const [manifest, setManifest] = useState<string>(() => STARTERS[defaultType ?? 'pods']?.[0]?.manifest ?? '')
  const [activeTemplateId, setActiveTemplateId] = useState<string>(
    STARTERS[defaultType ?? 'pods']?.[0]?.id ?? '',
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const isClusterScoped = CLUSTER_SCOPED_TYPES.has(type)
  const templates = STARTERS[type] ?? []

  // Namespace dropdown — fetch namespaces lazily when the modal is
  // open. Cluster-scoped kinds skip this.
  const { data: nsList } = useQuery<ResourceList>({
    queryKey: ['resources', 'namespaces'],
    queryFn: () => api.getResources('namespaces'),
    enabled: !isClusterScoped,
    staleTime: 60_000,
  })
  const namespaceOptions = useMemo(() => {
    const names = (nsList?.items ?? []).map((n) => n.name).sort()
    if (!names.includes(namespace) && namespace) {
      // Show the operator's current selection even if the API list
      // hasn't loaded yet — avoids a flash of "default" while the
      // dropdown populates.
      return [namespace, ...names.filter((n) => n !== namespace)]
    }
    return names
  }, [nsList, namespace])

  function pickType(newType: string) {
    setType(newType)
    setError(null)
    const next = STARTERS[newType]?.[0]
    if (next) {
      setManifest(next.manifest)
      setActiveTemplateId(next.id)
    } else {
      // No starter for this kind — reset to a minimal shell.
      setManifest('')
      setActiveTemplateId('')
    }
  }

  function pickTemplate(t: StarterTemplate) {
    setManifest(t.manifest)
    setActiveTemplateId(t.id)
  }

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      const urlNamespace = isClusterScoped ? '_' : namespace
      const res = await api.createResource(type, urlNamespace, manifest, 'ui')
      // Navigate to the newly created resource. The detail page reads
      // the namespace + name from the URL, with `_` as the placeholder
      // for cluster-scoped kinds — same convention the rest of the
      // app uses.
      const detailNS = res.namespace || '_'
      // Seed the detail-query cache with the post-create snapshot the
      // backend already polled for us — same pattern restart/scale/set-*
      // use. Without this, the detail page mounts, fires its first
      // /resources/.../{ns}/{name} fetch, the informer cache hasn't
      // observed the apiserver Create yet, and the user sees "Resource
      // not found" before the next refetch tick reconciles. Resource
      // may be null when the backend's own retry window expired —
      // skip the seed in that case and let the page's normal fetch
      // (which retries on its own) handle it.
      if (res.resource) {
        queryClient.setQueryData(['resource-detail', type, detailNS, res.name], res.resource)
      }
      // Pre-invalidate the list query so the new resource shows up
      // when the user navigates back.
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      navigate(`/${type}/${detailNS}/${res.name}`)
      onClose()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message
      setError(msg)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 text-status-info">
          <Plus className="w-3 h-3" /> new resource
        </span>
      }
      title="Create new resource"
      onClose={onClose}
      size="2xl"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        {/* Kind + namespace pickers */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
              Kind
            </span>
            <select
              value={type}
              onChange={(e) => pickType(e.target.value)}
              className="text-xs font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary focus:border-kb-border-active outline-none"
            >
              <optgroup label="Common">
                {COMMON_KINDS.map((k) => (
                  <option key={k.type} value={k.type}>{k.label}</option>
                ))}
              </optgroup>
              <optgroup label="Other">
                {OTHER_KINDS.map((k) => (
                  <option key={k.type} value={k.type}>{k.label}</option>
                ))}
              </optgroup>
            </select>
          </div>

          {!isClusterScoped && (
            <div className="flex items-center gap-2">
              <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
                Namespace
              </span>
              <select
                value={namespace}
                onChange={(e) => setNamespace(e.target.value)}
                className="text-xs font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary focus:border-kb-border-active outline-none"
              >
                {namespaceOptions.length === 0 ? (
                  <option value={namespace}>{namespace}</option>
                ) : (
                  namespaceOptions.map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))
                )}
              </select>
            </div>
          )}

          {isClusterScoped && (
            <span className="text-[11px] text-kb-text-tertiary italic">
              cluster-scoped (no namespace)
            </span>
          )}
        </div>

        {/* Starter templates */}
        {templates.length > 0 && (
          <div className="flex items-center gap-2 flex-wrap">
            <FileCode className="w-3.5 h-3.5 text-kb-text-tertiary" />
            <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
              Starter
            </span>
            {templates.map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => pickTemplate(t)}
                className={`text-[11px] px-2 py-0.5 rounded border transition-colors ${
                  activeTemplateId === t.id
                    ? 'bg-status-info-dim border-status-info/40 text-status-info'
                    : 'bg-kb-card border-kb-border text-kb-text-secondary hover:bg-kb-card-hover'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        )}

        {/* YAML editor */}
        <YAMLEditor value={manifest} onChange={setManifest} />

        <p className="text-[11px] text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl create -f manifest.yaml
          </code>
          . Fails on conflict (use the YAML editor to update an existing resource). Single-document manifests only.
        </p>

        {error && (
          <div className="flex items-start gap-2 text-xs text-status-error border border-status-error/30 bg-status-error-dim rounded p-2.5">
            <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <span className="break-words whitespace-pre-wrap">{error}</span>
          </div>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        <button
          onClick={onClose}
          disabled={busy}
          className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={busy || !manifest.trim()}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
        >
          {busy && <RefreshCw className="w-3 h-3 animate-spin" />}
          {busy ? 'Creating…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

// Self-contained CodeMirror YAML editor. Same theme + font as the
// detail-page YAMLTab editor; deliberately duplicated rather than
// imported to avoid coupling the modal to the detail page's
// internal helpers. If a third use surfaces, extract to a shared
// component.
function YAMLEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const editorRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)

  // The editor is mounted ONCE on first render and re-fed `value`
  // through a separate effect. This prevents a destroy/recreate
  // cycle on every parent re-render — without it, picking a starter
  // template would reset the cursor and lose focus.
  useEffect(() => {
    if (!editorRef.current) return
    const view = new EditorView({
      doc: value,
      extensions: [
        yamlLang(),
        oneDark,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            onChange(update.state.doc.toString())
          }
        }),
        EditorView.theme({
          '&': { fontSize: '11px', maxHeight: '480px' },
          '.cm-scroller': { overflow: 'auto', fontFamily: "'JetBrains Mono', 'Fira Code', Menlo, monospace" },
          '.cm-content': { padding: '12px 0' },
          '.cm-gutters': { backgroundColor: '#0d1117', border: 'none' },
          '&.cm-editor': { backgroundColor: '#0d1117', borderRadius: '8px' },
        }),
        lineNumbers(),
      ],
      parent: editorRef.current,
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Sync external `value` changes (starter template picks) into the
  // editor without rebuilding. Only fires when the prop differs from
  // the current doc to avoid feedback loops with the change listener.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const current = view.state.doc.toString()
    if (current !== value) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
      })
    }
  }, [value])

  return <div ref={editorRef} className="rounded-lg overflow-hidden border border-kb-border" />
}
