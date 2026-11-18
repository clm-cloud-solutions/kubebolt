import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Circle, Cpu, Loader2, MemoryStick } from "lucide-react";
import { api } from "@/services/api";
import type { ClusterProgress } from "@/types/kubernetes";
import { MetricChart, METRIC_ACCENTS } from "@/components/shared/MetricChart";
import { KpiCard } from "@/components/shared/kpi/KpiCard";
import {
  EventTrack,
  KPI_COLOR,
  Sparkline,
} from "@/components/shared/kpi/MiniCharts";
import { formatBytes } from "@/utils/formatters";
import {
  etaSeconds,
  kindPlural,
  prepareEvents,
  prepareView,
  type StepState,
} from "@/utils/preparing";

// PreparingCluster is what a cluster page shows while the cluster's cache
// loads, in place of a spinner: how far the connect is, what is happening and
// why the wait is worth it, three live figures (types loaded, data received,
// time), the Activity log, and the cluster's metrics, which the agents ship
// before the cache has synced. The cluster's views still open together when it
// is ready; nothing here shows a partial cluster.

// Same leaf-container filter as the Capacity charts (a naive sum over-counts).
const POD_SUM_FILTER =
  'pod_uid!="",container!="",container!="POD",container!="pause"';

interface FeedLine {
  at: number;
  text: string;
}

export function useClusterProgress(
  context: string | undefined,
  enabled: boolean,
  intervalMs = 1000,
) {
  return useQuery({
    queryKey: ["cluster-progress", context ?? ""],
    queryFn: () => api.getClusterProgress(),
    enabled,
    refetchInterval: enabled ? intervalMs : false,
    retry: false,
  });
}

// useTween eases a number toward its target, so a jump from 60 to 91 reads as
// movement. Reduced motion (and tests) take the target at once.
function useTween(target: number, ms = 700): number {
  const [shown, setShown] = useState(target);
  const from = useRef(target);
  useEffect(() => {
    let reduced = true;
    try {
      reduced =
        import.meta.env.MODE === "test" ||
        window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    } catch {
      reduced = true;
    }
    if (reduced || from.current === target) {
      from.current = target;
      setShown(target);
      return;
    }
    const start = performance.now();
    const a = from.current;
    let raf = 0;
    const step = (now: number) => {
      const p = Math.min(1, (now - start) / ms);
      const v = a + (target - a) * (1 - Math.pow(1 - p, 3));
      setShown(v);
      if (p < 1) raf = requestAnimationFrame(step);
      else from.current = target;
    };
    raf = requestAnimationFrame(step);
    return () => {
      cancelAnimationFrame(raf);
      from.current = target;
    };
  }, [target, ms]);
  return shown;
}

function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(t);
  }, [everyMs]);
  return now;
}

const clock = (s: number) =>
  `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;

export function PreparingCluster({
  name,
  context,
}: {
  name: string;
  context?: string;
}) {
  const queryClient = useQueryClient();
  const { data } = useClusterProgress(context, true);
  const prev = useRef<ClusterProgress | undefined>(undefined);
  const pctMax = useRef(0);
  const mountedAt = useRef(Date.now());
  const samples = useRef<Array<{ at: number; loaded: number; bytes: number }>>(
    [],
  );
  const [feed, setFeed] = useState<FeedLine[]>([]);
  const now = useNow(1000);

  const view = prepareView(data, name, pctMax.current);
  pctMax.current = view.pct;
  const pctShown = useTween(view.pct);
  const startedAt = data?.startedAt
    ? Date.parse(data.startedAt)
    : mountedAt.current;
  const elapsed = Math.max(0, (now - startedAt) / 1000);

  useEffect(() => {
    if (!data) return;
    const events = prepareEvents(prev.current, data, name);
    prev.current = data;
    if (events.length > 0) {
      const at = Math.max(0, (Date.now() - startedAt) / 1000);
      setFeed((f) =>
        [...events.map((text) => ({ at, text })).reverse(), ...f].slice(0, 5),
      );
    }
    const t = Date.now();
    samples.current = [
      ...samples.current.filter((s) => t - s.at <= 30_000),
      { at: t, loaded: view.loadedObjects, bytes: data.bytesReceived ?? 0 },
    ];
    if (data.state === "ready") {
      // The views are served now: bring the page's own queries back at once
      // instead of on their next retry.
      void queryClient.invalidateQueries({ queryKey: ["cluster-overview"] });
      void queryClient.invalidateQueries({ queryKey: ["clusters"] });
    }
    // view.loadedObjects and startedAt derive from data
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, name, queryClient]);

  const failed = data?.state === "failed";
  const loading = !failed && data?.state !== "ready";
  const recent = samples.current.filter((s) => now - s.at <= 8_000);
  const eta = etaSeconds(recent, view.totalObjects - view.loadedObjects);

  // Data received, and its rate between readings (KB/s), for the sparkline.
  const bytes = data?.bytesReceived ?? 0;
  const rates = samples.current.slice(1).map((s, i) => {
    const a = samples.current[i];
    const dt = (s.at - a.at) / 1000;
    return dt > 0 ? Math.max(0, (s.bytes - a.bytes) / 1000 / dt) : 0;
  });
  const rateNow = rates.length > 0 ? rates[rates.length - 1] : 0;
  const [bytesValue, bytesUnit] = formatBytes(bytes).split(" ");

  // A kubeconfig cluster has no agents shipping metrics before the load.
  const viaAgent = data?.viaAgent !== false;
  const types = data?.types ?? [];
  const synced = types.filter((t) => t.synced).length;
  const pending = types.filter((t) => !t.synced);

  return (
    <section
      aria-label={`Preparing ${name}`}
      className={`kb-panel max-w-[1400px] mx-auto w-full p-5 grid gap-5 ${viaAgent ? "lg:grid-cols-[minmax(0,1fr)_minmax(300px,380px)]" : ""}`}
    >
      <div className="min-w-0 flex flex-col gap-3.5">
        <div className="flex items-start gap-4">
          <div className="flex-1 min-w-0 flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              Preparing {name}
            </span>
            <h2
              key={failed ? "failed" : view.what}
              role="status"
              aria-live="polite"
              className="animate-fade-up text-lg font-semibold text-kb-text-primary leading-snug"
            >
              {failed ? "The connect stopped" : view.what}
            </h2>
          </div>
          <div className="flex flex-col items-end shrink-0">
            <span className="text-2xl font-bold font-mono text-kb-text-primary tabular-nums leading-none">
              {Math.round(pctShown)}%
            </span>
            <span className="text-[11px] text-kb-text-tertiary mt-1">
              {eta ? `about ${eta} s left` : " "}
            </span>
          </div>
        </div>
        <p
          key={failed ? "failed-why" : view.why}
          className="animate-fade-up text-[13px] text-kb-text-secondary leading-relaxed"
        >
          {failed
            ? `${data?.error ?? "The cluster did not answer."}${data?.retryAt ? ` KubeBolt tries again at ${new Date(data.retryAt).toLocaleTimeString()}.` : ""}`
            : view.why}
        </p>

        <div
          role="progressbar"
          aria-label={`Preparing ${name}`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={view.pct}
          className="h-2 rounded-full bg-kb-elevated overflow-hidden"
        >
          <div
            className={`h-2 rounded-full bg-kb-accent ${loading ? "kb-progress-flow" : ""}`}
            style={{ width: `${pctShown}%` }}
          />
        </div>

        <ol className="flex flex-wrap gap-x-5 gap-y-1.5">
          {view.steps.map((s) => (
            <li
              key={s.key}
              className="flex items-center gap-1.5 min-w-0 text-xs"
            >
              <StepIcon state={s.state} />
              <span
                className={`font-semibold ${s.state === "pending" ? "text-kb-text-tertiary" : "text-kb-text-primary"}`}
              >
                {s.label}
              </span>
              <span className="font-mono text-kb-text-tertiary truncate">
                {s.detail}
              </span>
            </li>
          ))}
        </ol>

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <KpiCard
            compact
            primary
            value={types.length > 0 ? `${synced}/${types.length}` : "–"}
            description="resource types loaded"
            viz={
              types.length > 0 ? (
                // One cell per type, thin enough for a cluster's 26 to fit a
                // compact card (the shared UnitStrip keeps 6 px per cell).
                <div className="flex gap-[3px]" aria-hidden="true">
                  {types.map((t) => (
                    <span
                      key={t.kind}
                      title={`${kindPlural(t.kind)}${t.synced ? " · loaded" : t.total > 0 ? ` · ${t.loaded.toLocaleString("en-US")} of ${t.total.toLocaleString("en-US")}` : " · loading"}`}
                      className={`flex-1 min-w-0 h-3 rounded-[3px] transition-colors duration-500 ${t.synced ? "" : "animate-pulse"}`}
                      style={{
                        background: t.synced
                          ? KPI_COLOR.ok
                          : t.loaded > 0
                            ? KPI_COLOR.info
                            : KPI_COLOR.track,
                      }}
                    />
                  ))}
                </div>
              ) : undefined
            }
            caption={
              types.length === 0
                ? "waiting for the first list"
                : pending.length === 0
                  ? "all loaded"
                  : pending.length <= 2
                    ? `waiting on ${pending.map((t) => kindPlural(t.kind)).join(", ")}`
                    : `${pending.length} still loading`
            }
          />
          <KpiCard
            compact
            value={bytesValue}
            unit={bytesUnit}
            description={
              viaAgent
                ? "received from the agents"
                : "received from the cluster"
            }
            viz={
              rates.length >= 2 ? (
                <Sparkline
                  values={rates}
                  height={34}
                  fromZero
                  left="KB/s, last 30 s"
                />
              ) : undefined
            }
            caption={
              rateNow > 0
                ? `${formatBytes(rateNow * 1000)}/s now`
                : "measuring the tunnel"
            }
          />
          <KpiCard
            compact
            value={clock(elapsed)}
            description="since the connect began"
            viz={
              <EventTrack
                events={feed.map((f) => f.at)}
                from={0}
                to={Math.max(elapsed, 1)}
                color={KPI_COLOR.ok}
                left="each dot, a step done"
              />
            }
            caption={
              eta
                ? `about ${eta} s left`
                : loading
                  ? "estimating what is left"
                  : "done"
            }
          />
        </div>

        <div className="flex flex-col gap-1 border-t border-kb-border pt-3">
          <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Activity
          </span>
          <ul className="flex flex-col gap-0.5">
            {feed.length === 0 && (
              <li className="text-[12.5px] text-kb-text-tertiary">
                Waiting for the first answer from {name}…
              </li>
            )}
            {feed.map((f, i) => (
              <li
                key={`${f.at}-${f.text}`}
                className={`animate-fade-up flex gap-3 text-[12.5px] leading-relaxed ${i === 0 ? "text-kb-text-primary" : "text-kb-text-secondary"}`}
              >
                <span className="w-14 shrink-0 text-right font-mono text-kb-text-tertiary tabular-nums">
                  +{f.at.toFixed(1)} s
                </span>
                <span className="min-w-0">{f.text}</span>
              </li>
            ))}
          </ul>
        </div>

        <p className="text-xs text-kb-text-tertiary leading-relaxed">
          <span className="font-semibold text-kb-text-secondary">
            Not on every visit:
          </span>{" "}
          KubeBolt loads this when it connects to the cluster — the first time,
          after KubeBolt restarts, or after the connection was lost. Afterwards,
          changes stream in as they happen.
        </p>
      </div>

      {viaAgent && (
        <div className="min-w-0 flex flex-col gap-3 lg:border-l lg:border-kb-border lg:pl-5">
          <div className="flex flex-col gap-0.5">
            <span className="flex items-center gap-1.5 text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              <span
                className="w-1.5 h-1.5 rounded-full bg-status-ok animate-pulse"
                aria-hidden="true"
              />
              Live now
            </span>
            <span className="text-xs text-kb-text-secondary">
              Metrics come from the agents, not from this load.
            </span>
          </div>
          <MetricChart
            recessed
            title="CPU usage"
            icon={<Cpu className="w-4 h-4" />}
            unit="cores"
            queries={[
              {
                query: `sum(rate(node_cpu_usage_seconds_total[1m]))`,
                prefix: "node",
              },
              {
                query: `sum(rate(container_cpu_usage_seconds_total{${POD_SUM_FILTER}}[1m]))`,
                prefix: "pods",
              },
            ]}
            seriesLabel={(_l, prefix) =>
              prefix === "pods" ? "pods" : "node total"
            }
            accents={[METRIC_ACCENTS.cpu[0], "#94a3b8"]}
            chartType="area"
            showStats={false}
            height={110}
            controlledRangeMinutes={60}
          />
          <MetricChart
            recessed
            title="Memory working set"
            icon={<MemoryStick className="w-4 h-4" />}
            unit="bytes"
            queries={[
              { query: `sum(node_memory_working_set_bytes)`, prefix: "node" },
              {
                query: `sum(container_memory_working_set_bytes{${POD_SUM_FILTER}})`,
                prefix: "pods",
              },
            ]}
            seriesLabel={(_l, prefix) =>
              prefix === "pods" ? "pods" : "node total"
            }
            accents={[METRIC_ACCENTS.memory[0], "#94a3b8"]}
            chartType="area"
            showStats={false}
            height={110}
            controlledRangeMinutes={60}
          />
        </div>
      )}
    </section>
  );
}

function StepIcon({ state }: { state: StepState }) {
  if (state === "done")
    return (
      <CheckCircle2
        className="w-3.5 h-3.5 text-status-ok shrink-0"
        aria-hidden="true"
      />
    );
  if (state === "active")
    return (
      <Loader2
        className="w-3.5 h-3.5 text-kb-accent animate-spin shrink-0"
        aria-hidden="true"
      />
    );
  return (
    <Circle
      className="w-3.5 h-3.5 text-kb-text-tertiary shrink-0"
      aria-hidden="true"
    />
  );
}
