import type { ComponentType, ReactNode } from 'react'
import { Database, Globe, ListChecks, Server } from 'lucide-react'
import { MetricChart } from '@/components/shared/MetricChart'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Sparkline, StatusList } from '@/components/shared/kpi/MiniCharts'
import { TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { liveProcess } from '@/utils/promql'
import { AutoGrid } from './AutoGrid'
import { Breakdown } from './Breakdown'
import { duration, HttpCharts, jobStatus, JobsTable, RouteGroupsTable, VMCallsCharts } from './ApiHealthSections'
import { first, HealthFrame, useHealthSparkline, useHealthVector, type HealthFrameProps } from './healthShared'

// Administration › System › Health: is this KubeBolt healthy right now, and if
// not, which piece — the API replicas, what the API answers, its background
// jobs, the metrics store. Every series is KubeBolt's own (doc #67, O2),
// written by the API about itself (job="kubebolt-api") and by VictoriaMetrics
// about itself (job="victoria-metrics", --selfScrapeInterval).

const API = 'job="kubebolt-api"'
const VM = 'job="victoria-metrics"'
const STATE_COLOR: Record<string, string> = { active: '#22c55e', building: '#f59e0b', parked: '#94a3b8' }

function Section({ title, note, Icon, children }: { title: string; note: string; Icon: ComponentType<{ className?: string }>; children: ReactNode }) {
  return (
    <section className="rounded-xl border border-kb-border bg-kb-card p-4 sm:p-5 flex flex-col gap-4">
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
    <HealthFrame intro="KubeBolt itself: the API replicas, what the API answers, its background jobs and the metrics store. Only KubeBolt's own series — none of your workloads' metrics.">
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

  // The same verdict the jobs table gives, counted.
  const now = Date.now() / 1000
  const lastOf = new Map((jobSuccess.data ?? []).map((r) => [r.labels.name, r.value]))
  const declaredOf = new Map((jobDeclared.data ?? []).map((r) => [r.labels.name, r.value]))
  const errorsOf = new Map((jobErrors.data ?? []).map((r) => [r.labels.name, r.value]))
  const statuses = (jobIntervals.data ?? [])
    .filter((r) => r.labels.name)
    .map((r) => {
      const ts = lastOf.get(r.labels.name)
      const age = ts !== undefined && ts > 0 ? now - ts : null
      const declaredAt = declaredOf.get(r.labels.name)
      const waited = age ?? (declaredAt !== undefined ? now - declaredAt : null)
      return jobStatus(waited, r.value, Math.round(errorsOf.get(r.labels.name) ?? 0))
    })
  const late = statuses.filter((s) => s.rank <= 1).length
  const withErrors = statuses.filter((s) => s.rank === 2).length

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
        alert={failedPct >= 5 ? 'crit' : failedPct >= 1 ? 'warn' : undefined}
        value={total > 0 ? (failedPct < 0.1 && failed > 0 ? '<0.1' : Math.round(failedPct * 10) / 10) : '—'}
        unit="% 5xx"
        description={total > 0 ? `${failed.toLocaleString('en-US')} of ${total.toLocaleString('en-US')} requests` : 'no requests in the last hour'}
        caption="kubebolt_http_requests_total · 1h"
        info={
          <Breakdown
            title="Server errors"
            right="last hour"
            rows={[
              { color: KPI_COLOR.ok, label: 'requests', value: total.toLocaleString('en-US') },
              { color: KPI_COLOR.err, label: 'answered 5xx', value: failed.toLocaleString('en-US') },
            ]}
            note="503 — a cluster unreachable or a feature not configured — is a state, not a failure: it is not counted."
          />
        }
      />
      <KpiCard
        alert={late > 0 ? 'crit' : withErrors > 0 ? 'warn' : undefined}
        value={statuses.length > 0 ? late : '—'}
        unit={late === 1 ? 'job late' : 'jobs late'}
        description={
          statuses.length === 0
            ? 'no background job reported yet'
            : late > 0
              ? `of ${statuses.length} background jobs`
              : withErrors > 0
                ? `${withErrors} with failed runs in the last hour`
                : `all ${statuses.length} on time`
        }
        caption="kubebolt_job_*"
        info={
          <>
            <TooltipHeader>Background jobs</TooltipHeader>
            <TooltipNote>
              A job is late when it has not succeeded for three of its intervals (two minutes at least). The table below
              names each one.
            </TooltipNote>
          </>
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

      <Section title="HTTP" note="what the API answers, by route group, and how fast" Icon={Globe}>
        <HttpCharts charts={charts} perInterval={perInterval} perPoint={perPoint} />
        <RouteGroupsTable rangeMinutes={rangeMinutes} />
      </Section>

      <Section title="Background jobs" note="the API's periodic work, and whether each job still succeeds on time" Icon={ListChecks}>
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
    </div>
  )
}
