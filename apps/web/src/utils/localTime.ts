// One zone per document: the reader's.
//
// A postmortem mixed three: the response steps in UTC, Kubernetes times in
// whatever zone the tool reported (+02:00 from describe, Z from conditions),
// and the report header in local time. Asking the model to convert zones is
// arithmetic on clocks, which is exactly what it should not do. So the model
// quotes every timestamp WITH its zone, untouched, and the conversion happens
// here, in code, for every surface that shows the report.

const FORMAT: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  // The zone is printed: an exported report travels, and "21:01:24" means
  // nothing to a reader in another country.
  timeZoneName: 'short',
}

/** A date-time in the reader's zone, with the zone named. */
export function formatLocalDateTime(value: string | number | Date | null | undefined): string {
  if (value === null || value === undefined || value === '') return '—'
  const d = value instanceof Date ? value : new Date(value)
  return Number.isNaN(d.getTime()) ? String(value) : d.toLocaleString(undefined, FORMAT)
}

// Only stamps that carry a date, a time AND a zone are converted. Anything less
// — "2026-08-08", "19:01:24 UTC", "about 21 minutes before" — cannot be placed
// on the reader's clock without inventing the missing part, so it is left as
// written.
const ISO_ZONED =
  /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?\s*(?:Z|[+-]\d{2}:?\d{2})$/i
const RFC2822_ZONED =
  /^(?:[A-Z][a-z]{2},\s*)?\d{1,2}\s+[A-Z][a-z]{2}\s+\d{4}\s+\d{2}:\d{2}(?::\d{2})?\s+[+-]\d{4}$/

/** The instant a zoned timestamp names, or null when it is not one. */
export function parseZonedTimestamp(raw: string): Date | null {
  // Earlier reports wrote the offset in parentheses: "2026-09-22 12:04:53 (+02:00)".
  const s = raw.trim().replace(/\s*\(([+-]\d{2}:?\d{2}|Z)\)$/i, '$1')
  let iso = s
  if (ISO_ZONED.test(s)) {
    // Date.parse wants "T" and a colon in the offset on every engine.
    iso = s.replace(' ', 'T').replace(/([+-]\d{2})(\d{2})$/, '$1:$2').replace(/T\s+/, 'T')
    iso = iso.replace(/\s+(?=Z|[+-]\d{2}:\d{2}$)/i, '')
  } else if (!RFC2822_ZONED.test(s)) {
    return null
  }
  const t = Date.parse(iso)
  return Number.isNaN(t) ? null : new Date(t)
}

const localDateKey = (d: Date) => d.toLocaleDateString('en-CA') // YYYY-MM-DD, local
const localTime = (d: Date) =>
  d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const zoneName = (d: Date) =>
  new Intl.DateTimeFormat(undefined, { timeZoneName: 'short' })
    .formatToParts(d)
    .find((p) => p.type === 'timeZoneName')?.value ?? ''

/**
 * Rewrites every bold timestamp in a markdown timeline — "**<stamp>** — …" — to
 * the reader's zone, compactly. Bold that is not a zoned stamp (a percentage, a
 * date on its own) is left alone.
 *
 * Compact because the full form on every line ("Sep 23, 2026, 09:10:25 PM GMT+2",
 * eight times over) buried the only part that changes. So:
 *   - the date appears only when it changes — within a response it rarely does;
 *   - each "###" section starts by showing its date, so workload history does
 *     not silently inherit the response's day;
 *   - the year only when it differs from the first stamp's;
 *   - the zone once, and again only if it changes (a May and a December stamp
 *     are an hour apart under daylight saving, and would read as the same zone).
 */
export function localizeTimelineTimestamps(markdown: string): string {
  let prevDate = ''
  let shownZone = ''
  let firstYear: number | null = null
  return markdown
    .split('\n')
    .map((line) => {
      if (/^\s*#{1,6}\s/.test(line)) prevDate = ''
      return line.replace(/\*\*([^*\n]+?)\*\*/g, (whole, inner: string) => {
        const d = parseZonedTimestamp(inner)
        if (!d) return whole
        const year = d.getFullYear()
        if (firstYear === null) firstYear = year
        const dateKey = localDateKey(d)
        let label = localTime(d)
        if (dateKey !== prevDate) {
          const date = d.toLocaleDateString(undefined, {
            month: 'short',
            day: 'numeric',
            ...(year !== firstYear ? { year: 'numeric' } : {}),
          })
          label = `${date}, ${label}`
          prevDate = dateKey
        }
        const zone = zoneName(d)
        if (zone && zone !== shownZone) {
          label = `${label} ${zone}`
          shownZone = zone
        }
        return `**${label}**`
      })
    })
    .join('\n')
}

/**
 * Timestamps the model writes INSIDE the text, as inline code —
 * "last terminated at `2026-09-22T12:04:28+02:00`" — become the reader's local
 * time in plain text (a converted time is no longer code). The bold stamp that
 * opens a timeline bullet is localizeTimelineTimestamps' job; this covers the
 * rest of the report, so "one zone per document" holds in every section.
 */
export function localizeInlineTimestamps(markdown: string): string {
  return markdown.replace(/`([^`\n]+)`/g, (whole, inner: string) => {
    const d = parseZonedTimestamp(inner)
    return d ? formatLocalDateTime(d) : whole
  })
}

/**
 * When something happened, as short as the reader can place it: the time of
 * day alone when it was today ("11:51 AM"), with its date otherwise
 * ("Oct 11, 11:51 PM"). The "since …" of the KPI cards and Home's shift report.
 */
export function clockOrDate(value: string | number | Date, now: Date = new Date()): string {
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

/**
 * clockOrDate for tight spaces (a KPI card's list, its tooltip): the time of
 * day for today, the date alone for an earlier day ("Aug 19"). After a day the
 * minute no longer helps, and the long form does not fit beside a name.
 */
export function clockOrDay(value: string | number | Date, now: Date = new Date()): string {
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
