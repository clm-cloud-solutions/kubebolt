import type { ComponentType, ReactNode } from 'react'
import { Cable, Database, Globe, ListChecks, Server } from 'lucide-react'
import { MetricChart } from '@/components/shared/MetricChart'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Sparkline, StatusList } from '@/components/shared/kpi/MiniCharts'
import { TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { AnchorLink } from '@/components/shared/kpi/CardLinks'
import { liveProcess } from '@/utils/promql'
import { AutoGrid } from './AutoGrid'
import { Breakdown } from './Breakdown'
import { duration, HttpCharts, jobStatus, JobsTable, RouteGroupsTable, VMCallsCharts } from './ApiHealthSections'
import { first, HealthFrame, inInterval, useHealthSparkline, useHealthVector, type HealthFrameProps } from './healthShared'

// Administration › System › Health: is this KubeBolt healthy right now, and if
// not, which piece — the API replicas, what the API answers, its background
// jobs, the metrics store, the tunnel to the agents. Every series is KubeBolt's own (doc #67, O2),
// written by the API about itself (job="kubebolt-api") and by VictoriaMetrics
// about itself (job="victoria-metrics", --selfScrapeInterval).

const API = 'job="kubebolt-api"'
const VM = 'job="victoria-metrics"'
const STATE_COLOR: Record<string, string> = { active: '#22c55e', building: '#f59e0b', parked: '#94a3b8' }
// One colour per value, so no two bars of a chart read alike.
const TUNNEL_RESULT_COLOR: Record<string, string> = {
  timeout: '#f59e0b',
  canceled: '#94a3b8',
  agent_closed: '#ef4056',
  not_connected: '#a855f7',
  send_error: '#f97316',
}
// A quantile of the tunnel calls in each interval on its own — like Kobi's
// model latency — and nothing where there were none.
const TUNNEL_OK = 'kubebolt_agent_kube_request_seconds_{kind}{result="ok"}'
const tunnelQuantile = (q: number) =>
  `histogram_quantile(${q}, ${inInterval(TUNNEL_OK.replace('{kind}', 'bucket'), 'le')}) and on () (${inInterval(TUNNEL_OK.replace('{kind}', 'count'))} > 0)`

function Section({ id, title, note, Icon, children }: { id?: string; title: string; note: string; Icon: ComponentType<{ className?: string }>; children: ReactNode }) {
  return (
    <section id={id} className="rounded-xl border border-kb-border bg-kb-card p-4 sm:p-5 flex flex-col gap-4 scroll-mt-4">
      <header className="flex items-center gap-2.5 flex-wrap pb-3 border-b border-kb-border">
        <span className="w-7 h-7 rounded-lg bg-kb-elevated flex items-center justify-center text-kb-text-secondary shrink-0">
          <Icon className="w-4 h-4" />
        </span>
        <h3 className="text-[15px] font-semibold text-kb-text-primary">{title}</h3>
        <span className="text-xs text-kb-text-tertiary">{note}</span>
      </header>
      {children}
    </section>
  )
}

export function ApiHealth() {
  return (
    <HealthFrame intro="KubeBolt itself: the API replicas, what the API answers, its background jobs, the metrics store and the tunnel to the agents. Only KubeBolt's own series — none of your workloads' metrics.">
      {(p) => <ApiHealthBody {...p} />}
    </HealthFrame>
  )
}

function KpiRow() {
  const builds = useHealthVector('builds', liveProcess('kubebolt_build_info'))
  const uptimes = useHealthVector('uptimes', `time() - ${liveProcess(`process_start_time_seconds{${API}}`)}`)
  const apiMem = useHealthVector('api-mem', liveProcess(`process_resident_memory_bytes{${API}}`))
  const goroutines = useHealthVector('goroutines', `max(${liveProcess(`go_goroutines{${API}}`)})`)
  const apiMemSpark = useHealthSparkline('api-mem', `max(${liveProcess(`process_resident_memory_bytes{${API}}`)})`)
  const requests = useHealthVector('http-1h', 'sum(increase_pure(kubebolt_http_requests_total[1h]))')
  const errors5xx = useHealthVector('http-5xx-1h', 'sum(increase_pure(kubebolt_http_requests_total{code="5xx"}[1h]))')
  // Which route groups answer them: the card names the worst, the tooltip the
  // rest, each against its own traffic.
  const errorsByGroup = useHealthVector('http-5xx-group-1h', 'sum by (group) (increase_pure(kubebolt_http_requests_total{code="5xx"}[1h])) > 0')
  const requestsByGroup = useHealthVector('http-group-1h', 'sum by (group) (increase_pure(kubebolt_http_requests_total[1h]))')
  const jobIntervals = useHealthVector('jobs-interval', 'max by (name) (max_over_time(kubebolt_job_interval_seconds[24h]))')
  const jobSuccess = useHealthVector('jobs-success', 'max by (name) (max_over_time(kubebolt_job_last_success_timestamp_seconds[24h]))')
  const jobDeclared = useHealthVector('jobs-declared', 'min by (name) (tfirst_over_time(kubebolt_job_interval_seconds[24h]))')
  const jobErrors = useHealthVector('jobs-err-1h', 'sum by (name) (increase_pure(kubebolt_job_runs_total{result="error"}[1h]))')

  const replicas = builds.data ?? []
  const versions = [...new Set(replicas.map((r) => r.labels.version ?? '?'))]
  const uptimeOf = new Map((uptimes.data ?? []).map((r) => [r.labels.instance, r.value]))
  const heaviest = (apiMem.data ?? []).reduce((m, r) => Math.max(m, r.value), 0)

  const total = Math.round(first(requests.data) ?? 0)
  const failed = Math.round(first(errors5xx.data) ?? 0)
  const failedPct = total > 0 ? (failed / total) * 100 : 0
  const http5xxAlert = failedPct >= 5 ? 'crit' : failedPct >= 1 ? 'warn' : undefined
  const groupTotal = new Map((requestsByGroup.data ?? []).map((r) => [r.labels.group ?? '', r.value]))
  const failingGroups = (errorsByGroup.data ?? [])
    .map((r) => ({ group: r.labels.group || 'other', failed: Math.round(r.value), total: Math.round(groupTotal.get(r.labels.group ?? '') ?? 0) }))
    .filter((g) => g.failed > 0)
    .sort((a, b) => b.failed - a.failed)
  const worstGroup = http5xxAlert ? failingGroups[0] : undefined

  // The same verdict the jobs table gives, counted.
  const now = Date.now() / 1000
  const lastOf = new Map((jobSuccess.data ?? []).map((r) => [r.labels.name, r.value]))
  const declaredOf = new Map((jobDeclared.data ?? []).map((r) => [r.labels.name, r.value]))
  const errorsOf = new Map((jobErrors.data ?? []).map((r) => [r.labels.name, r.value]))
  // Worst first, and within a state the one waiting longest: that is the job
  // the card names.
  const jobs = (jobIntervals.data ?? [])
    .filter((r) => r.labels.name)
    .map((r) => {
      const name = r.labels.name
      const ts = lastOf.get(name)
      const age = ts !== undefined && ts > 0 ? now - ts : null
      const declaredAt = declaredOf.get(name)
      const waited = age ?? (declaredAt !== undefined ? now - declaredAt : null)
      const errors = Math.round(errorsOf.get(name) ?? 0)
      return { name, age, waited, errors, status: jobStatus(waited, r.value, errors) }
    })
    .sort((a, b) => a.status.rank - b.status.rank || (b.waited ?? 0) - (a.waited ?? 0))
  const statuses = jobs.map((j) => j.status)
  const late = statuses.filter((s) => s.rank <= 1).length
  const withErrors = statuses.filter((s) => s.rank === 2).length
  const worstJob = late > 0 || withErrors > 0 ? jobs[0] : undefined
  const inTrouble = jobs.filter((j) => j.status.rank <= 2)
  const lastOk = (j: (typeof jobs)[number]) => (j.age !== null ? `last ok ${duration(j.age)} ago` : 'no success yet')

  return (
    <AutoGrid minCol={260} allowed={[4, 2, 1]} fallback="grid-cols-1 sm:grid-cols-2 xl:grid-cols-4">
      <KpiCard
        primary
        alert={replicas.length === 0 ? 'crit' : versions.length > 1 ? 'warn' : undefined}
        value={replicas.length}
        unit={replicas.length === 1 ? 'replica' : 'replicas'}
        description={
          replicas.length === 0
            ? 'no API replica is reporting'
            : versions.length > 1
              ? `${versions.length} versions running side by side`
              : `reporting, all on ${versions[0]}`
        }
        viz={
          <StatusList
            rows={replicas.slice(0, 3).map((r) => {
              const up = uptimeOf.get(r.labels.instance)
              return {
                color: versions.length > 1 ? KPI_COLOR.warn : KPI_COLOR.ok,
                label: r.labels.instance ?? '—',
                value: versions.length > 1 ? r.labels.version : up != null ? `up ${duration(up)}` : undefined,
              }
            })}
          />
        }
        caption="kubebolt_build_info"
        info={
          <>
            <TooltipHeader>API replicas</TooltipHeader>
            <TooltipNote>
              Each replica publishes one build-info series. Each row says how long that replica has been up, so a restart
              shows; two versions side by side is a rollout in progress. A replica that stops reporting disappears from
              this list within five minutes.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        value={heaviest > 0 ? Math.round(heaviest / 1024 ** 2) : '—'}
        unit="MiB"
        description={heaviest > 0 ? `heaviest replica · ${Math.round(first(goroutines.data) ?? 0).toLocaleString('en-US')} goroutines` : 'waiting for samples'}
        viz={(apiMemSpark.data?.length ?? 0) >= 2 ? <Sparkline values={(apiMemSpark.data ?? []).map((v) => Math.round(v / 1024 ** 2))} window="6h" /> : undefined}
        caption="process_resident_memory_bytes · API"
        info={
          <Breakdown
            title="Memory per replica"
            right="now"
            rows={(apiMem.data ?? []).map((r) => ({ color: KPI_COLOR.ok, label: r.labels.instance ?? 'api', value: `${Math.round(r.value / 1024 ** 2)} MiB` }))}
          />
        }
      />
      <KpiCard
        alert={http5xxAlert}
        value={total > 0 ? (failedPct < 0.1 && failed > 0 ? '<0.1' : Math.round(failedPct * 10) / 10) : '—'}
        unit="% 5xx"
        description={
          total === 0 ? (
            'no requests in the last hour'
          ) : worstGroup ? (
            <>
              {failed.toLocaleString('en-US')} of {total.toLocaleString('en-US')} · mostly{' '}
              <AnchorLink id="api-http">{worstGroup.group}</AnchorLink>
            </>
          ) : (
            `${failed.toLocaleString('en-US')} of ${total.toLocaleString('en-US')} requests`
          )
        }
        caption="kubebolt_http_requests_total · 1h"
        info={
          worstGroup ? (
            <Breakdown
              title="Server errors by route group"
              right="last hour"
              rows={failingGroups.slice(0, 3).map((g) => ({
                color: KPI_COLOR.err,
                label: g.group,
                value: g.total > 0 ? `${g.failed.toLocaleString('en-US')} · ${Math.round((g.failed / g.total) * 100)}%` : g.failed.toLocaleString('en-US'),
              }))}
              note="Each group against its own requests. The name opens the HTTP section, which has the split over time. 503 — a cluster unreachable or a feature not configured — is a state, not a failure: it is not counted."
            />
          ) : (
            <Breakdown
              title="Server errors"
              right="last hour"
              rows={[
                { color: KPI_COLOR.ok, label: 'requests', value: total.toLocaleString('en-US') },
                { color: KPI_COLOR.err, label: 'answered 5xx', value: failed.toLocaleString('en-US') },
              ]}
              note="503 — a cluster unreachable or a feature not configured — is a state, not a failure: it is not counted."
            />
          )
        }
      />
      <KpiCard
        alert={late > 0 ? 'crit' : withErrors > 0 ? 'warn' : undefined}
        value={statuses.length > 0 ? late : '—'}
        unit={late === 1 ? 'job late' : 'jobs late'}
        description={
          statuses.length === 0 ? (
            'no background job reported yet'
          ) : worstJob ? (
            <>
              <AnchorLink id="api-jobs">{worstJob.name}</AnchorLink>
              {(late > 0 ? late : withErrors) > 1 ? ` +${(late > 0 ? late : withErrors) - 1}` : ''} ·{' '}
              {late > 0
                ? worstJob.age !== null
                  ? `last success ${duration(worstJob.age)} ago`
                  : 'no success yet'
                : `${worstJob.errors} failed ${worstJob.errors === 1 ? 'run' : 'runs'} in the last hour`}
            </>
          ) : (
            `all ${statuses.length} on time`
          )
        }
        caption="kubebolt_job_*"
        info={
          inTrouble.length > 0 ? (
            <Breakdown
              title="Background jobs"
              right="now"
              rows={[
                ...inTrouble.slice(0, 6).map((j) => ({ color: j.status.color, label: j.name, value: `${j.status.label.toLowerCase()} · ${lastOk(j)}` })),
                ...(jobs.length > inTrouble.length
                  ? [{ color: KPI_COLOR.ok, label: `${jobs.length - inTrouble.length} more`, value: 'on time' }]
                  : []),
              ]}
              note="A job is late when it has not succeeded for three of its intervals (two minutes at least). The name opens the Background jobs table."
            />
          ) : (
            <>
              <TooltipHeader>Background jobs</TooltipHeader>
              <TooltipNote>
                A job is late when it has not succeeded for three of its intervals (two minutes at least). The table below
                names each one.
              </TooltipNote>
            </>
          )
        }
      />
    </AutoGrid>
  )
}

function ApiHealthBody({ charts, rangeMinutes, perInterval, perPoint }: HealthFrameProps) {
  const vmLimit = useHealthVector('vm-limit', `max(process_memory_limit_bytes{${VM}})`)
  const vmCap = first(vmLimit.data)
  const gib = (b: number) => (b / 1024 ** 3).toFixed(1)
  return (
    <div className="flex flex-col gap-6">
      <KpiRow />

      <Section id="api-http" title="HTTP" note="what the API answers, by route group, and how fast" Icon={Globe}>
        <HttpCharts charts={charts} perInterval={perInterval} perPoint={perPoint} />
        <RouteGroupsTable rangeMinutes={rangeMinutes} />
      </Section>

      <Section id="api-jobs" title="Background jobs" note="the API's periodic work, and whether each job still succeeds on time" Icon={ListChecks}>
        <JobsTable rangeMinutes={rangeMinutes} />
      </Section>

      <Section title="VictoriaMetrics" note="the metrics store measuring itself, and the API's calls to it" Icon={Database}>
        <AutoGrid minCol={340} allowed={[3, 1]} fallback="grid-cols-1 lg:grid-cols-3">
          <MetricChart
            {...charts}
            title="Memory against its limit"
            unit="bytes"
            chartType="area"
            accents={['#a855f7']}
            query={`max(process_resident_memory_bytes{${VM}})`}
            seriesLabel={() => 'in use'}
            referenceLines={vmCap ? [{ y: vmCap, label: `limit ${gib(vmCap)} GiB`, shortLabel: 'limit', color: '#ef4056' }] : undefined}
            emptyMessage="VictoriaMetrics is not reporting on itself."
            emptyHint="It measures itself when started with --selfScrapeInterval (on in the bundled chart and compose file)."
            footnote={'process_resident_memory_bytes · process_memory_limit_bytes{job="victoria-metrics"}'}
          />
          <MetricChart
            {...charts}
            title="Rows written per second"
            unit="count"
            chartType="area"
            accents={['#06b6d4']}
            query={`sum(rate(vm_rows_inserted_total{${VM}}[5m]))`}
            seriesLabel={() => 'rows/s'}
            emptyMessage="VictoriaMetrics is not reporting on itself."
            emptyHint="It measures itself when started with --selfScrapeInterval (on in the bundled chart and compose file)."
            footnote={'vm_rows_inserted_total{job="victoria-metrics"}'}
          />
          <MetricChart
            {...charts}
            title="Disk"
            unit="bytes"
            chartType="line"
            queries={[
              { query: `sum(vm_data_size_bytes{${VM}})`, prefix: 'data', accent: '#a855f7' },
              { query: `min(vm_free_disk_space_bytes{${VM}})`, prefix: 'free', accent: '#94a3b8' },
            ]}
            seriesLabel={(_l, prefix) => prefix ?? 'disk'}
            emptyMessage="VictoriaMetrics is not reporting on itself."
            emptyHint="It measures itself when started with --selfScrapeInterval (on in the bundled chart and compose file)."
            footnote={'vm_data_size_bytes · vm_free_disk_space_bytes{job="victoria-metrics"}'}
          />
          <VMCallsCharts charts={charts} perInterval={perInterval} perPoint={perPoint} />
        </AutoGrid>
      </Section>

      <Section title="API" note="each replica on its own line" Icon={Server}>
        <AutoGrid minCol={340} allowed={[3, 1]} fallback="grid-cols-1 lg:grid-cols-3">
          <MetricChart
            {...charts}
            title="Memory per replica"
            unit="bytes"
            chartType="line"
            query={liveProcess(`process_resident_memory_bytes{${API}}`)}
            seriesLabel={(l) => l.instance ?? 'api'}
            emptyMessage="No API replica reported in this window."
            emptyHint="The resident memory of each API process, one line per replica across its restarts."
            footnote={`process_resident_memory_bytes{${API}} · ${perPoint}`}
          />
          <MetricChart
            {...charts}
            title="Goroutines per replica"
            unit="count"
            chartType="line"
            query={liveProcess(`go_goroutines{${API}}`)}
            seriesLabel={(l) => l.instance ?? 'api'}
            emptyMessage="No API replica reported in this window."
            emptyHint="A count that only climbs is a leak: something starts goroutines that never end."
            footnote={`go_goroutines{${API}} · ${perPoint}`}
          />
          <MetricChart
            {...charts}
            title="Cluster runtimes"
            unit="count"
            chartType="area"
            query={`sum by (state) (${liveProcess('kubebolt_api_runtimes')})`}
            seriesLabel={(l) => l.state ?? 'runtimes'}
            seriesColor={(l) => STATE_COLOR[l.state ?? '']}
            emptyMessage="No cluster runtimes in this window."
            emptyHint="Runtimes appear as soon as a cluster is connected to this API."
            footnote="kubebolt_api_runtimes by (state)"
          />
        </AutoGrid>
      </Section>

      <Section title="Agents and tunnel" note="where each agent channel lives, and what crossing the tunnel costs" Icon={Cable}>
        <AutoGrid minCol={340} allowed={[3, 1]} fallback="grid-cols-1 lg:grid-cols-3">
          <MetricChart
            {...charts}
            title="Live channels per replica"
            unit="count"
            chartType="area"
            accents={['#22c55e', '#94a3b8', '#06b6d4']}
            query={liveProcess('kubebolt_agent_channels')}
            seriesLabel={(l) => l.instance ?? 'api'}
            emptyMessage="No agent channel in this window."
            emptyHint="A channel is pinned to the replica the agent connected to; with several replicas this shows how they share them."
            footnote="kubebolt_agent_channels by (instance)"
          />
          <MetricChart
            {...charts}
            sparse
            title="Requests through the tunnel"
            unit="seconds"
            chartType="line"
            queries={[
              { query: tunnelQuantile(0.95), prefix: 'p95', accent: '#f59e0b' },
              { query: tunnelQuantile(0.5), prefix: 'p50', accent: '#94a3b8' },
            ]}
            seriesLabel={(_l, prefix) => prefix ?? 'latency'}
            emptyMessage="No requests through the tunnel in this window."
            emptyHint="Unary Kubernetes API calls the API makes through an agent, each interval on its own; no calls, no point. Watches and exec sessions are not timed."
            footnote={`kubebolt_agent_kube_request_seconds{result="ok"} · ${perPoint}`}
          />
          <MetricChart
            {...charts}
            sparse
            title="Tunnel failures"
            unit="count"
            chartType="bar"
            query={`${inInterval('kubebolt_agent_kube_request_seconds_count{result!="ok"}', 'result')} > 0`}
            seriesLabel={(l) => (l.result ?? 'failed').replace('_', ' ')}
            seriesColor={(l) => TUNNEL_RESULT_COLOR[l.result ?? '']}
            emptyMessage="No tunnel failures in this window."
            emptyHint="Timeouts, agents that dropped mid-request and requests for a cluster with no agent show up here."
            footnote={`kubebolt_agent_kube_request_seconds_count{result!="ok"} by (result) · ${perInterval}`}
          />
        </AutoGrid>
      </Section>
    </div>
  )
}
