import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Legend, Sparkline, SplitBar, StatusList } from '@/components/shared/kpi/MiniCharts'
import { TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import { fmtCredits, fmtUsd, useAiSpendUnit } from '@/hooks/useAiSpendUnit'
import type { CopilotUsageBucket, CopilotUsageSummary } from '@/types/copilotUsage'

// CopilotUsageKpis — the row Copilot's usage view opens with, in the site's
// card anatomy (KpiCard), the same family as Home, Fleet and the Overview:
//
//   sessions  how much Kobi was used, over the range
//   health    how many sessions ended normally; errors, fallback, max rounds
//   tokens    what was billed, and how much of the input came from cache
//   spend     credits where the platform runs the AI, the estimated provider
//             cost where the org brings its own key (useAiSpendUnit)
//
// It replaces the two rows of small tiles (summary + reliability) without
// dropping a figure: every number they showed is on a card, its chart or its
// caption.

const KPI_MIN_WIDTH = 240

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function fmtDuration(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`
}

export function CopilotUsageKpis({
  summary,
  buckets,
  range,
}: {
  summary: CopilotUsageSummary
  buckets: CopilotUsageBucket[]
  range: string
}) {
  const unit = useAiSpendUnit()
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()

  const sessions = summary.sessions
  const automatic = sessions - summary.interactiveSessions
  const finishedPct = sessions > 0 ? Math.round(100 - summary.errorRate) : null

  const spend = unit === 'credits' ? summary.credits ?? 0 : summary.estimatedUsd
  const spendSeries = buckets.map((b) => (unit === 'credits' ? b.credits ?? 0 : b.estimatedUsd))
  const fmtSpend = unit === 'credits' ? fmtCredits : fmtUsd
  const spendLo = spendSeries.length ? Math.min(...spendSeries) : 0
  const spendHi = spendSeries.length ? Math.max(...spendSeries) : 0

  const warnIf = (n: number) => (n > 0 ? KPI_COLOR.warn : KPI_COLOR.muted)

  return (
    <div
      ref={gridRef}
      className={`grid gap-3 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 12, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        compact
        primary
        value={sessions}
        unit={sessions === 1 ? 'session' : 'sessions'}
        description={
          sessions === 0
            ? 'no sessions in this range'
            : automatic > 0
              ? `${summary.interactiveSessions.toLocaleString('en-US')} interactive, ${automatic.toLocaleString('en-US')} automatic`
              : 'all of them interactive'
        }
        viz={buckets.length >= 2 ? <Sparkline height={34} values={buckets.map((b) => b.sessions)} window={range} fromZero /> : undefined}
        caption={`avg ${fmtDuration(summary.avgDurationMs)} · ${summary.avgRounds.toFixed(1)} rounds per session`}
        info={
          <>
            <TooltipHeader>Sessions</TooltipHeader>
            <TooltipNote>
              Every Kobi turn recorded in this range. Automatic records are the ones Kobi makes on its own (conversation
              titles, compactions) and do not count as interactive use.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        compact
        alert={summary.errorSessions > 0 ? 'warn' : undefined}
        value={finishedPct ?? '—'}
        unit={finishedPct != null ? '% finished' : undefined}
        description={
          sessions === 0
            ? 'nothing to measure yet'
            : summary.errorSessions > 0
              ? `${plural(summary.errorSessions, 'session')} ended in an error`
              : 'every session ended normally'
        }
        viz={
          <StatusList
            rows={[
              { color: summary.errorSessions > 0 ? KPI_COLOR.err : KPI_COLOR.muted, label: 'ended in an error', value: summary.errorSessions },
              { color: warnIf(summary.fallbackSessions), label: 'used the fallback provider', value: summary.fallbackSessions },
              { color: warnIf(summary.maxRoundsSessions), label: 'hit the round limit', value: summary.maxRoundsSessions },
            ]}
          />
        }
        caption={`${summary.errorRate.toFixed(0)}% errors · ${summary.fallbackRate.toFixed(0)}% on fallback`}
        info={
          <>
            <TooltipHeader>Session health</TooltipHeader>
            <TooltipNote>
              A session finishes when Kobi gives its answer. One that ends in an error, runs out of rounds before
              answering, or needs the fallback provider is counted here.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        compact
        value={fmtTokens(summary.totalBilledTokens)}
        unit="tokens"
        description={`${summary.cacheHitPct.toFixed(0)}% of the input read from cache`}
        viz={
          <div className="space-y-2.5">
            <SplitBar
              parts={[
                { value: summary.cacheReadTokens, color: KPI_COLOR.ok },
                { value: summary.inputTokens, color: KPI_COLOR.info },
                { value: summary.cacheCreationTokens, hatched: true },
              ]}
            />
            <Legend
              rows={[
                { color: KPI_COLOR.ok, label: `${fmtTokens(summary.cacheReadTokens)} from cache` },
                { color: KPI_COLOR.info, label: `${fmtTokens(summary.inputTokens)} new input` },
              ]}
            />
          </div>
        }
        caption={`${fmtTokens(summary.outputTokens)} out · ${plural(summary.compacts, 'compaction')}`}
        info={
          <>
            <TooltipHeader>Tokens</TooltipHeader>
            <TooltipNote>
              Billed tokens are the input and output of every model call. Input read from the provider's cache costs a
              fraction of new input; the hatched part is input written to the cache.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        compact
        value={unit === 'credits' ? fmtCredits(spend) : fmtUsd(spend)}
        unit={unit === 'credits' ? 'credits' : undefined}
        description={
          spend === 0
            ? unit === 'credits'
              ? 'nothing spent in this range'
              : 'no known pricing for these models'
            : sessions > 0
              ? `${fmtSpend(spend / sessions)} per session on average`
              : 'in this range'
        }
        viz={
          spendSeries.length >= 2 && spendHi > 0 ? (
            <Sparkline
              height={34}
              values={spendSeries}
              fromZero
              left={spendHi > spendLo ? `${range} · ${fmtSpend(spendLo)}–${fmtSpend(spendHi)}` : `${range} · steady at ${fmtSpend(spendHi)}`}
            />
          ) : undefined
        }
        caption={unit === 'credits' ? 'from the shared monthly pool' : 'estimated at list prices · your provider bills the exact amount'}
        info={
          unit === 'credits' ? (
            <>
              <TooltipHeader>Credits</TooltipHeader>
              <TooltipNote>
                AI credits Kobi Copilot used in this range. Copilot and Autopilot draw from the same monthly pool, and a
                credit is the same unit of work in both.
              </TooltipNote>
            </>
          ) : (
            <>
              <TooltipHeader>Estimated cost</TooltipHeader>
              <TooltipNote>
                What these sessions cost at the provider's list prices, from the tokens each model used. Kobi runs on
                your own key, so the exact amount is on your provider's bill.
              </TooltipNote>
            </>
          )
        }
      />
    </div>
  )
}
