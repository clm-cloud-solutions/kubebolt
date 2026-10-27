import { useRef, useState } from 'react'
import { Eye, EyeOff, FileUp } from 'lucide-react'
import type { McpFieldSchema } from '@/services/api'
import { UnsavedChip } from '@/pages/admin/settings/SettingsField'
import { CopyButton } from './ConnectorBits'

// One form row generated from a FieldSchema. Label, input, helper and error
// are wired together (htmlFor, aria-describedby, aria-invalid) because the
// drawer builds every form from data and cannot hand-label anything.

export type SecretState = 'none' | 'set' | 'unreadable'

interface Props {
  field: McpFieldSchema
  idPrefix: string
  value: string
  onChange: (v: string) => void
  dirty: boolean
  error?: string
  // For secrets: what is stored. The value itself never reaches the browser.
  secretState?: SecretState
  // For a prefilled value: where it came from ("prod-eks").
  prefillSource?: string
  disabled?: boolean
}

// The border colour, focus included, comes from the field's state (below):
// an invalid field that takes the focus must still read as invalid.
const INPUT =
  'w-full px-2.5 py-1.5 rounded-md bg-kb-bg border text-xs text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none disabled:opacity-60 disabled:cursor-not-allowed'

// A value KubeBolt created: selectable and copyable, not a field to fill.
const READ_ONLY =
  'w-full px-2.5 py-1.5 rounded-md bg-kb-elevated border border-kb-border text-xs font-mono text-kb-text-primary focus:outline-none focus:border-kb-accent'

// A long option list (every AWS region) is a searchable input, a short one a select.
const SELECT_MAX_OPTIONS = 12

export function ConnectorFieldInput({
  field,
  idPrefix,
  value,
  onChange,
  dirty,
  error,
  secretState = 'none',
  prefillSource,
  disabled,
}: Props) {
  const [reveal, setReveal] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const id = `${idPrefix}-${field.name}`
  const helpId = `${id}-help`
  const errId = `${id}-error`
  const border = error ? 'border-status-error focus:border-status-error' : 'border-kb-border focus:border-kb-accent'

  // A credential file (a Google service-account key) is easier to load than
  // to paste. It is read locally and only ever sent as the secret's value.
  const acceptsFile = field.secret && (!!field.multiline || /json/i.test(field.name))
  // A multiline secret stays a masked single-line input while hidden (a
  // textarea cannot be masked; pasted JSON survives the browser folding its
  // line breaks) and opens as a textarea when revealed, so it can be read.
  const asTextarea = !!field.multiline && (!field.secret || reveal)
  // Blank keeps a stored secret, so the field is no longer required to be
  // filled; a generated value is never the admin's to fill.
  const required = field.required && !field.generated && !(field.secret && secretState === 'set')

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    try {
      onChange(await file.text())
    } catch {
      // Unreadable file: leave the field as it was.
    }
  }

  let helper: React.ReactNode = field.help ?? null
  let helperWarn = false
  if (field.generated) {
    helper = field.help ?? 'KubeBolt created this value for your organization. Copy it where the setup guide asks for it.'
  } else if (field.secret && secretState === 'set' && value.trim() === '') {
    helper = 'Already set — leave blank to keep it, or enter a new value to replace it.'
  } else if (field.secret && secretState === 'unreadable') {
    helper = "The stored value can't be read anymore — enter it again to restore access."
    helperWarn = true
  } else if (prefillSource) {
    helper = (
      <>
        Detected from cluster <span className="font-mono">{prefillSource}</span>.{field.help ? ` ${field.help}` : ''}
      </>
    )
  }

  const describedBy = [error ? errId : '', helper ? helpId : ''].filter(Boolean).join(' ')
  const common = {
    id,
    disabled,
    'aria-invalid': error ? true : undefined,
    'aria-describedby': describedBy || undefined,
    'aria-required': required || undefined,
  }

  let control: React.ReactNode
  if (field.generated) {
    // Read-only, not disabled: it stays focusable and selectable, so it can
    // be copied by hand when the clipboard is blocked.
    control = (
      <div className="flex items-center gap-1.5">
        <input
          {...common}
          type="text"
          value={value}
          readOnly
          autoComplete="off"
          spellCheck={false}
          className={READ_ONLY}
        />
        {value && <CopyButton value={value} name={`Copy ${field.label}`} />}
      </div>
    )
  } else if (field.secret) {
    control = (
      <div className={`flex gap-1.5 ${asTextarea ? 'items-start' : 'items-center'}`}>
        <div className="relative flex-1 min-w-0">
          {asTextarea ? (
            <textarea
              {...common}
              value={value}
              onChange={(e) => onChange(e.target.value)}
              rows={6}
              autoComplete="off"
              spellCheck={false}
              placeholder={secretState === 'set' ? '••••••••' : field.placeholder}
              className={`${INPUT} ${border} pr-9 font-mono resize-y`}
            />
          ) : (
            <input
              {...common}
              type={reveal ? 'text' : 'password'}
              value={value}
              onChange={(e) => onChange(e.target.value)}
              // "new-password" keeps the browser from offering the admin's own
              // KubeBolt password for a cloud credential; "off" is ignored for
              // password inputs by most browsers.
              autoComplete="new-password"
              spellCheck={false}
              placeholder={secretState === 'set' ? '••••••••' : field.placeholder}
              className={`${INPUT} ${border} pr-9 font-mono`}
            />
          )}
          <button
            type="button"
            onClick={() => setReveal((v) => !v)}
            disabled={disabled}
            className={`absolute right-1.5 p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary ${
              asTextarea ? 'top-1.5' : 'top-1/2 -translate-y-1/2'
            }`}
            aria-label={reveal ? `Hide ${field.label}` : `Show ${field.label}`}
            aria-controls={id}
          >
            {reveal ? <EyeOff className="w-3.5 h-3.5" aria-hidden /> : <Eye className="w-3.5 h-3.5" aria-hidden />}
          </button>
        </div>
        {acceptsFile && (
          <>
            <button
              type="button"
              onClick={() => fileRef.current?.click()}
              disabled={disabled}
              className="inline-flex items-center gap-1 px-2 py-1.5 rounded-md border border-kb-border bg-kb-elevated text-[11px] text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-card-hover transition-colors shrink-0 disabled:opacity-60"
            >
              <FileUp className="w-3.5 h-3.5" aria-hidden />
              Load file
            </button>
            <input
              ref={fileRef}
              type="file"
              accept=".json,application/json"
              className="hidden"
              onChange={onFile}
              tabIndex={-1}
              aria-hidden
            />
          </>
        )}
      </div>
    )
  } else if (field.options && field.options.length > 0 && field.options.length <= SELECT_MAX_OPTIONS) {
    control = (
      <select {...common} value={value} onChange={(e) => onChange(e.target.value)} className={`${INPUT} ${border}`}>
        {!field.required && <option value="">—</option>}
        {field.required && value === '' && <option value="">Choose…</option>}
        {field.options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    )
  } else if (asTextarea) {
    control = (
      <textarea
        {...common}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={4}
        placeholder={field.placeholder}
        autoComplete="off"
        spellCheck={false}
        className={`${INPUT} ${border} font-mono resize-y`}
      />
    )
  } else {
    const listId = field.options && field.options.length > 0 ? `${id}-options` : undefined
    control = (
      <>
        <input
          {...common}
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={field.placeholder}
          autoComplete="off"
          spellCheck={false}
          list={listId}
          className={`${INPUT} ${border} font-mono`}
        />
        {listId && (
          <datalist id={listId}>
            {field.options!.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </datalist>
        )}
      </>
    )
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <label htmlFor={id} className="text-[11px] font-semibold text-kb-text-primary">
          {field.label}
        </label>
        {!field.required && <span className="text-[10px] text-kb-text-tertiary">Optional</span>}
        {dirty && <UnsavedChip />}
      </div>
      {control}
      {error && (
        <p id={errId} className="text-[11px] text-status-error leading-relaxed">
          {error}
        </p>
      )}
      {helper && (
        <p id={helpId} className={`text-[11px] leading-relaxed ${helperWarn ? 'text-kobi-st-warn' : 'text-kb-text-tertiary'}`}>
          {helper}
        </p>
      )}
    </div>
  )
}
