import { useState } from 'react'
import { Link } from 'react-router-dom'
import { MetricChart } from '@/components/shared/MetricChart'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Sparkline } from '@/components/shared/kpi/MiniCharts'
import { AnchorLink } from '@/components/shared/kpi/CardLinks'
import { AutoGrid } from './AutoGrid'
import { Breakdown } from './Breakdown'
import {
  compact,
  failuresOrZero,
  FAILURE_PALETTE,
  first,
  HealthFrame,
  inInterval,
  OUTCOME_COLOR,
  sum,
  TOKEN_COLOR,
  useHealthSparkline,
  useHealthVector,
  type HealthFrameProps,
} from './healthShared'

// Administration › AI › Health: how Kobi is doing — its turns, the AI
// provider, its tools, the prompt cache and what users think of its answers.
// Read from the kubebolt_kobi_copilot_* series the API writes about itself
// (doc #67, phase 1); the comment a user leaves with a 👎 never reaches them.

const COPILOT = (family: string, matchers = '') => `kubebolt_kobi_copilot_${family}${matchers ? `{${matchers}}` : ''}`
const COPILOT_USED = `${inInterval(COPILOT('sessions_total'))} > 0`
const INPUT_KINDS = 'input|cache_read|cache_write_5m|cache_write_1h'
const WRITE_KINDS = 'cache_write_5m|cache_write_1h'
const INPUT_KIND_LABEL: Record<string, string> = {
  input: 'uncached',
  cache_read: 'cache read',
  cache_write_5m: 'cache write 5m',
  cache_write_1h: 'cache write 1h',
}

// 👍 green, each 👎 reason its own colour.
const FEEDBACK_COLOR: Record<string, string> = {
  up: '#22c55e',
  incorrect: '#ef4056',
  incomplete: '#f59e0b',
  off_topic: '#a855f7',
  slow: '#4c9aff',
  none: '#94a3b8',
}
const FEEDBACK_REASON_LABEL: Record<string, string> = {
  incorrect: 'incorrect',
  incomplete: 'incomplete',
  off_topic: "didn't answer",
  slow: 'too slow',
  none: 'no reason',
}
// How a failed turn ended, in the card's words.
const FAILED_LABEL: Record<string, string> = { max_rounds: 'out of rounds', error: 'errors' }
// The sessions table of Usage, over the card's 24 h, filtered to the turns
// that failed.
const FAILED_SESSIONS = '/admin/ai?tab=usage&range=24h&sessions=errors#kobi-sessions-table'
const LINK = 'underline decoration-dotted underline-offset-2 hover:text-kb-text-primary transition-colors'

const feedbackLabel = (l: Record<string, string>) =>
  l.rating === 'up' ? '👍 helpful' : `👎 ${FEEDBACK_REASON_LABEL[l.reason ?? ''] ?? l.reason ?? ''}`

// Input tokens over a window.
const tokensOf = (kinds: string, window: string) => `sum(increase_pure(${COPILOT('tokens_total', `kind=~"${kinds}"`)}[${window}]))`
const cacheReadRatio = (w: string) => `${tokensOf('cache_read', w)} / ${tokensOf(INPUT_KINDS, w)}`

type TokenGroup = 'kind' | 'model'

// GroupBy is the «group by» control of a chart, in its header.
function GroupBy<T extends string>({ value, options, onChange }: { value: T; options: { key: T; label: string }[]; onChange: (v: T) => void }) {
  return (
    <div className="inline-flex rounded-md border border-kb-border overflow-hidden" role="group" aria-label="Group by">
      {options.map((o) => (
        <button
          key={o.key}
          type="button"
          onClick={() => onChange(o.key)}
          className={`px-2 py-0.5 text-[11px] border-r border-kb-border last:border-r-0 transition-colors ${
            value === o.key ? 'bg-kb-elevated text-kb-text-primary' : 'bg-kb-bg text-kb-text-tertiary hover:text-kb-text-secondary'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function KobiHealth() {
  return (
    <HealthFrame intro="How Kobi is doing: its turns, the AI provider, its tools, the prompt cache and what users think of its answers. A 👎's comment stays with its conversation and never shows here.">
      {(p) => <KobiHealthBody {...p} />}
    </HealthFrame>
  )
}

function KpiRow() {
  const sessions = useHealthVector('kobi-sessions', `sum by (outcome) (increase_pure(${COPILOT('sessions_total')}[24h]))`)
  const sessionsSpark = useHealthSparkline('kobi-sessions', `sum(increase_pure(${COPILOT('sessions_total')}[1h]))`, 1440, '1h')
  const ratings = useHealthVector('kobi-ratings', `sum by (rating) (increase_pure(${COPILOT('feedback_total')}[24h]))`)
  const downReasons = useHealthVector('kobi-down-reasons', `sum by (reason) (increase_pure(${COPILOT('feedback_total', 'rating="down"')}[24h])) > 0`)
  const ratio = useHealthVector('kobi-cache-ratio', cacheReadRatio('24h'))
  const ratioSpark = useHealthSparkline('kobi-cache-ratio', cacheReadRatio('1h'), 1440, '1h')

  const byOutcome = Object.fromEntries((sessions.data ?? []).map((r) => [r.labels.outcome, r.value])) as Record<string, number>
  const turns = Math.round(sum(sessions.data))
  const failed = Math.round((byOutcome.error ?? 0) + (byOutcome.max_rounds ?? 0))
  const failedPct = turns > 0 ? Math.round((failed / turns) * 1000) / 10 : 0
  // The way most failed turns ended: the card names it.
  const [mainFailure, mainFailures] = (['max_rounds', 'error'] as const)
    .map((o) => [o, Math.round(byOutcome[o] ?? 0)] as const)
    .reduce((a, b) => (b[1] > a[1] ? b : a))

  const byRating = Object.fromEntries((ratings.data ?? []).map((r) => [r.labels.rating, Math.round(r.value)])) as Record<string, number>
  const up = byRating.up ?? 0
  const down = byRating.down ?? 0
  const rated = up + down
  const downPct = rated > 0 ? Math.round((down / rated) * 100) : null
  const downLit = downPct != null && rated >= 20 && downPct > 20
  const reasons = (downReasons.data ?? [])
    .map((r) => ({ reason: r.labels.reason || 'none', n: Math.round(r.value) }))
    .filter((r) => r.n > 0)
    .sort((a, b) => b.n - a.n)

  const r = first(ratio.data)
  return (
    <AutoGrid minCol={300} allowed={[3, 2, 1]} fallback="grid-cols-1 sm:grid-cols-2 xl:grid-cols-3">
      <KpiCard
        primary
        alert={failedPct >= 20 ? 'crit' : failedPct >= 5 ? 'warn' : undefined}
        value={compact(turns)}
        unit={turns === 1 ? 'turn' : 'turns'}
        description={
          turns === 0
            ? 'nobody used Kobi in the last 24h'
            : failed === 0
              ? 'none failed'
              : failedPct >= 5
                ? `${failed} failed or cut off · ${mainFailures === failed ? `all ${FAILED_LABEL[mainFailure]}` : `mostly ${FAILED_LABEL[mainFailure]} (${mainFailures})`}`
                : `${failed} failed or cut off · ${failedPct}%`
        }
        viz={(sessionsSpark.data?.length ?? 0) >= 2 ? <Sparkline values={(sessionsSpark.data ?? []).map(Math.round)} window="24h" fromZero /> : undefined}
        caption={
          failed > 0 ? (
            <>
              Copilot · 24h ·{' '}
              <Link to={FAILED_SESSIONS} className={LINK}>
                the conversations
              </Link>
            </>
          ) : (
            'Copilot · 24h'
          )
        }
        info={
          <Breakdown
            title="Copilot turns"
            right="24h"
            rows={Object.entries(byOutcome).map(([outcome, n]) => ({
              color: OUTCOME_COLOR[outcome] ?? null,
              label: outcome === 'max_rounds' ? 'ran out of rounds' : outcome.replace('_', ' '),
              value: Math.round(n),
            }))}
            note="A turn that ended in an error or ran out of rounds counts as failed; one the user stopped does not. The conversations link opens Usage with the sessions that failed."
          />
        }
      />
      <KpiCard
        alert={downLit ? 'warn' : undefined}
        value={downPct != null ? downPct : '—'}
        unit="% 👎"
        description={
          rated === 0
            ? 'no ratings in the last 24h'
            : downLit && reasons.length > 0
              ? `${down} 👎 of ${rated} · mostly ${FEEDBACK_REASON_LABEL[reasons[0].reason] ?? reasons[0].reason}`
              : `${rated} ${rated === 1 ? 'rating' : 'ratings'} · ${up} 👍 · ${down} 👎`
        }
        caption={
          downLit ? (
            <>
              answer feedback · 24h · <AnchorLink id="kobi-feedback">by reason</AnchorLink>
            </>
          ) : (
            'answer feedback · 24h'
          )
        }
        info={
          <Breakdown
            title="Answer feedback"
            right="24h"
            rows={[
              { color: KPI_COLOR.ok, label: '👍 helpful', value: up },
              ...(reasons.length > 0
                ? reasons.map((r) => ({
                    color: FEEDBACK_COLOR[r.reason] ?? KPI_COLOR.err,
                    label: `👎 ${FEEDBACK_REASON_LABEL[r.reason] ?? r.reason}`,
                    value: r.n,
                  }))
                : [{ color: KPI_COLOR.err, label: '👎 not helpful', value: down }]),
            ]}
            note="The share of 👎 among the answers users rated, each 👎 by the reason the user chose. Above 20 % with at least 20 ratings, something in Kobi's answers needs a look."
          />
        }
      />
      <KpiCard
        value={r != null && Number.isFinite(r) ? Math.round(r * 1000) / 10 : '—'}
        unit="% read from cache"
        description="of every input token"
        viz={(ratioSpark.data?.length ?? 0) >= 2 ? <Sparkline values={(ratioSpark.data ?? []).filter(Number.isFinite).map((v) => Math.round(v * 1000) / 10)} window="24h" /> : undefined}
        caption="prompt cache · 24h"
        info={
          <Breakdown
            title="Cache read ratio"
            right="24h"
            rows={[{ color: KPI_COLOR.ok, label: 'input tokens served from cache', value: r != null && Number.isFinite(r) ? `${Math.round(r * 1000) / 10}%` : '—' }]}
            note="Cache reads over every input token (uncached, read and written). Higher is cheaper: a read costs a tenth of a fresh input token."
          />
        }
      />
    </AutoGrid>
  )
}

function KobiHealthBody({ charts, perInterval, perPoint }: HealthFrameProps) {
  const [tokenGroup, setTokenGroup] = useState<TokenGroup>('kind')
  return (
    <div className="flex flex-col gap-5">
      <KpiRow />
      <AutoGrid minCol={340} allowed={[3, 2, 1]} fallback="grid-cols-1 lg:grid-cols-3">
        <MetricChart
          {...charts}
          sparse
          title="Turns by outcome"
          unit="count"
          chartType="bar"
          query={`${inInterval(COPILOT('sessions_total'), 'outcome')} > 0`}
          seriesLabel={(l) => (l.outcome ?? 'turns').replace('_', ' ')}
          seriesColor={(l) => OUTCOME_COLOR[l.outcome ?? '']}
          emptyMessage="No Kobi turns in this window."
          emptyHint="Every chat turn is counted here once it finishes."
          footnote={`kubebolt_kobi_copilot_sessions_total by (outcome) · ${perInterval}`}
        />
        <div id="kobi-feedback" className="min-w-0 scroll-mt-4 [&>*]:h-full">
          <MetricChart
            {...charts}
            sparse
            title="Answer feedback"
            unit="count"
            chartType="bar"
            query={`${inInterval(COPILOT('feedback_total'), 'rating, reason')} > 0`}
            seriesLabel={feedbackLabel}
            seriesColor={(l) => FEEDBACK_COLOR[l.rating === 'up' ? 'up' : (l.reason ?? 'none')]}
            emptyMessage="No ratings in this window."
            emptyHint="Each 👍 and 👎 users give Kobi's answers, 👎 split by the reason they chose. A changed rating counts again; a withdrawn one does not."
            footnote={`kubebolt_kobi_copilot_feedback_total by (rating, reason) · ${perInterval}`}
          />
        </div>
        <MetricChart
          {...charts}
          sparse
          title="Provider errors"
          unit="count"
          chartType="bar"
          queries={[
            { query: failuresOrZero(COPILOT('provider_errors_total'), 'kind', 'kind', COPILOT_USED) },
            { query: `${inInterval(COPILOT('fallback_total', 'rescued="true"'))} > 0`, prefix: 'rescued by fallback', accent: '#22c55e' },
          ]}
          seriesLabel={(l, prefix) => prefix ?? (l.kind === 'none' ? 'no errors' : (l.kind ?? 'errors').replace('_', ' '))}
          accents={FAILURE_PALETTE}
          seriesColor={(l, prefix) => (!prefix && l.kind === 'none' ? '#94a3b8' : undefined)}
          emptyMessage="Kobi was not used in this window."
          emptyHint="Rate limits, overloads, timeouts and failed calls to the AI provider, with the ones the fallback rescued. An interval where Kobi worked without errors reads as 0."
          footnote={`kubebolt_kobi_copilot_provider_errors_total by (kind) · …_fallback_total{rescued="true"} · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="Refusals and cut-short answers"
          unit="count"
          chartType="bar"
          query={failuresOrZero(COPILOT('model_calls_total', 'stop_reason=~"refusal|max_tokens"'), 'stop_reason', 'stop_reason', COPILOT_USED)}
          seriesLabel={(l) => (l.stop_reason === 'none' ? 'none' : l.stop_reason === 'max_tokens' ? 'cut short' : l.stop_reason ?? 'calls')}
          seriesColor={(l) => (l.stop_reason === 'none' ? '#94a3b8' : l.stop_reason === 'refusal' ? '#ef4056' : '#f59e0b')}
          emptyMessage="No Kobi turns in this window."
          emptyHint="A model call that refused, or ran out of output tokens, shows up here even when the turn ended as done. A turn without either reads as 0."
          footnote={`kubebolt_kobi_copilot_model_calls_total{stop_reason=~"refusal|max_tokens"} · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="Empty or failed tool calls"
          unit="count"
          chartType="bar"
          query={failuresOrZero(COPILOT('tool_results_total', 'result!="ok"'), 'source, result', 'result', COPILOT_USED)}
          seriesLabel={(l) => (l.result === 'none' ? 'none' : `${l.source ?? 'tool'} · ${(l.result ?? '').replace('_', ' ')}`)}
          accents={FAILURE_PALETTE}
          seriesColor={(l) => (l.result === 'none' ? '#94a3b8' : undefined)}
          emptyMessage="No Kobi turns in this window."
          emptyHint="Empty, not found, denied, timed-out and failed results, one colour per source and result. A turn whose tools all answered reads as 0."
          footnote={`kubebolt_kobi_copilot_tool_results_total{result!="ok"} by (source, result) · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="Model latency, p95"
          unit="seconds"
          chartType="line"
          accents={['#f59e0b', '#a855f7', '#06b6d4', '#3b82f6']}
          query={`histogram_quantile(0.95, ${inInterval(COPILOT('model_call_seconds_bucket'), 'le, model')}) and on (model) (${inInterval(COPILOT('model_call_seconds_count'), 'model')} > 0)`}
          seriesLabel={(l) => l.model ?? 'model'}
          emptyMessage="No model calls in this window."
          emptyHint="The p95 of the model calls that answered in each interval, per model; no calls, no point."
          footnote={`histogram_quantile(0.95, kubebolt_kobi_copilot_model_call_seconds) by (model) · ${perPoint}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="AI cost"
          unit="usd"
          chartType="bar"
          query={`${inInterval(COPILOT('cost_usd_total'), 'model')} > 0`}
          seriesLabel={(l) => l.model ?? '—'}
          emptyMessage="No AI cost in this window."
          emptyHint="Estimated per call from the pricing table of each model — what the AI provider bills you."
          footnote={`kubebolt_kobi_copilot_cost_usd_total by (model) · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="AI tokens"
          unit="count"
          chartType="bar"
          query={`${inInterval(COPILOT('tokens_total'), tokenGroup)} > 0`}
          seriesLabel={(l) => (tokenGroup === 'model' ? (l.model ?? '—') : (l.kind ?? 'tokens').replace(/_/g, ' '))}
          seriesColor={(l) => (tokenGroup === 'kind' ? TOKEN_COLOR[l.kind ?? ''] : undefined)}
          headerRight={
            <GroupBy
              value={tokenGroup}
              onChange={setTokenGroup}
              options={[
                { key: 'kind', label: 'Kind' },
                { key: 'model', label: 'Model' },
              ]}
            />
          }
          emptyMessage="No AI tokens spent in this window."
          emptyHint="Split into input, output, reasoning and cache."
          footnote={`kubebolt_kobi_copilot_tokens_total by ${tokenGroup} · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          title="Input token composition"
          unit="count"
          chartType="bar"
          query={`${inInterval(COPILOT('tokens_total', `kind=~"${INPUT_KINDS}"`), 'kind')} > 0`}
          seriesLabel={(l) => INPUT_KIND_LABEL[l.kind ?? ''] ?? l.kind ?? 'input'}
          seriesColor={(l) => TOKEN_COLOR[l.kind ?? '']}
          emptyMessage="No input tokens in this window."
          emptyHint="How much of the input the cache served, how much it stored, and how much was paid at full price. Drive the uncached part down."
          footnote={`kubebolt_kobi_copilot_tokens_total{kind=input|cache_*} · ${perInterval}`}
        />
        <MetricChart
          {...charts}
          sparse
          logScale
          title="Write amortization by model"
          unit="times"
          chartType="line"
          accents={['#f59e0b', '#a855f7', '#06b6d4', '#3b82f6', '#22c55e']}
          query={`(${inInterval(COPILOT('tokens_total', 'kind="cache_read"'), 'model')} / (${inInterval(COPILOT('tokens_total', `kind=~"${WRITE_KINDS}"`), 'model')} > 0)) > 0`}
          seriesLabel={(l) => l.model ?? 'model'}
          emptyMessage="No cache writes in this window."
          emptyHint="Tokens read back per token written to the cache, per model. A cache write costs more than a fresh input token (1.25× for 5 minutes, 2× for an hour); above about 1× a cached prompt pays its write back."
          footnote={`cache_read ÷ cache_write, kubebolt_kobi_copilot_tokens_total by model · ${perPoint}`}
        />
      </AutoGrid>
    </div>
  )
}
