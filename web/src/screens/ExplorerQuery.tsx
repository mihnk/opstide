import { useEffect, useRef, useState, type FocusEvent, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { query } from '../api'
import { ChevronDown, ClockIcon, CloseIcon, DownloadIcon, FilterIcon, LayersIcon, LinkIcon, SearchIcon } from '../components/icons'
import { parseToken, quickRanges, tokenKeys, tokenKeyWord, tokensOf, typingToken, withoutToken, withToken } from '../explorer'
import { t, tn } from '../i18n'
import { Link } from '../router'
import { dayTime, offset, utcToZoned, zonedToUtc } from '../time'
import { ZoneSelect } from './common'

// The Explorer's query toolbar: one search field that holds every active filter as a token, the time range (with the
// clock it is read in), whether the view follows new records, and the actions on the view (Workspace, download, link).

/** The wrapper of a button and its popover: Escape closes it (focus back on the button), and so do a click outside and
 *  focus leaving it. */
function PopWrap({ open, setOpen, btn, children }: { open: boolean; setOpen: (o: boolean) => void; btn: RefObject<HTMLButtonElement | null>; children: ReactNode }) {
  const wrap = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const down = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', down)
    return () => document.removeEventListener('mousedown', down)
  }, [open, setOpen])
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && open) {
      e.stopPropagation()
      setOpen(false)
      btn.current?.focus()
    }
  }
  const onBlur = (e: FocusEvent) => {
    const next = e.relatedTarget as Node | null
    if (open && next && wrap.current && !wrap.current.contains(next)) setOpen(false)
  }
  return <div className="ex-popwrap" ref={wrap} onKeyDown={onKeyDown} onBlur={onBlur}>{children}</div>
}

const rangeWords: Record<string, () => string> = {
  '15m': () => t('Last 15 minutes'), '1h': () => t('Last hour'), '6h': () => t('Last 6 hours'), '24h': () => t('Last 24 hours'), '7d': () => t('Last 7 days'),
}

/** The search field: the free text, after the tokens of every active filter. */
export function SearchField({ params, set, inputRef }: { params: URLSearchParams; set: (c: Record<string, string | null>) => void; inputRef: RefObject<HTMLInputElement | null> }) {
  const q = params.get('q') ?? ''
  const [text, setText] = useState(q)
  const [seenQ, setSeenQ] = useState(q)
  const [hint, setHint] = useState('')
  if (q !== seenQ) {
    setSeenQ(q)
    setText(q)
  }
  useEffect(() => {
    if (typingToken(text)) return
    const timer = window.setTimeout(() => {
      if (text.trim() !== q) set({ q: text.trim() || null })
    }, 400)
    return () => window.clearTimeout(timer)
  }, [text, q, set])

  // "/" puts the cursor in the field from anywhere on the page that is not itself a field.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return
      const el = e.target as HTMLElement | null
      if (el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))) return
      e.preventDefault()
      inputRef.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [inputRef])

  const tokens = tokensOf(params)
  const key = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      const tok = parseToken(text)
      if (tok) {
        setHint('')
        setText(tok.rest)
        setSeenQ(tok.rest)
        set({ ...withToken(params, tok.param, tok.value), q: tok.rest || null })
        return
      }
      if (typingToken(text)) {
        const m = /([^\s:]+):[^:]*$/.exec(text)
        const k = tokenKeys.find((x) => m && (x.param === m[1].toLowerCase() || tokenKeyWord(x.param).toLowerCase() === m[1].toLowerCase()))
        if (k?.values) setHint(t('{key} takes: {values}', { key: tokenKeyWord(k.param), values: k.values().map((v) => v.word).join(', ') }))
        else setHint(t('Type a value after the colon.'))
        return
      }
      setHint('')
      set({ q: text.trim() || null })
    } else if (e.key === 'Backspace' && text === '' && tokens.length > 0) {
      e.preventDefault()
      const last = tokens[tokens.length - 1]
      set(withoutToken(params, last.param, last.value))
    }
  }
  return (
    <div className="ex-search-wrap">
      <div className="ex-search" onClick={(e) => { if (e.target === e.currentTarget) inputRef.current?.focus() }}>
        <SearchIcon size={16} className="lead" />
        <ul className="toks" aria-label={tn(tokens.length, '{n} active filter', '{n} active filters')}>
          {tokens.map((x) => {
            const fail = (x.param === 'outcome' && x.value === 'failed') || (x.param === 'severity' && x.value === 'error')
            return (
              <li key={`${x.param}=${x.value}`} className={`tok${fail ? ' fail' : ''}`} title={`${x.param}=${x.value}`}>
                <span className="tok-k">{x.key}:</span><span className="tok-v">{x.word.length > 28 ? `${x.word.slice(0, 26)}…` : x.word}</span>
                <button type="button" aria-label={t('Remove the filter {key} {value}', { key: x.key, value: x.word })} onClick={() => set(withoutToken(params, x.param, x.value))}>
                  <CloseIcon size={12} />
                </button>
              </li>
            )
          })}
        </ul>
        <input ref={inputRef} type="search" value={text} aria-label={t('Search the records')} aria-describedby="ex-search-help"
          placeholder={tokens.length ? t('Search, or add key:value') : t('Names, reasons or summaries… (filter: key:value)')}
          onChange={(e) => { setText(e.target.value); setHint('') }} onKeyDown={key} />
        <kbd aria-hidden="true">/</kbd>
      </div>
      <span id="ex-search-help" className="sr">{t('Press / to search from anywhere. Type key:value and press Enter to add a filter, for example namespace:shop. Backspace in an empty field removes the last filter.')}</span>
      {hint && <span className="ex-hint" role="status">{hint}</span>}
    </div>
  )
}

/** The time range button and its popover: quick ranges, a range by hand, and the clock times are read in. */
export function TimePicker({ params, set, tz, setTz, from, to }: {
  params: URLSearchParams
  set: (c: Record<string, string | null>) => void
  tz: string
  setTz: (z: string) => void
  /** The range now shown (from the answer), so a range by hand starts from it. */
  from?: string
  to?: string
}) {
  const [open, setOpen] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const custom = !!(params.get('from') || params.get('to'))
  const range = custom ? '' : params.get('range') || '24h'
  const [editFrom, setFrom] = useState<string | null>(null)
  const [editTo, setTo] = useState<string | null>(null)
  const [bad, setBad] = useState(false)
  const cFrom = editFrom ?? (from ? utcToZoned(Date.parse(from), tz) : '')
  const cTo = editTo ?? (to ? utcToZoned(Date.parse(to), tz) : '')
  const first = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (open) first.current?.focus()
  }, [open])

  const apply = () => {
    const a = zonedToUtc(cFrom, tz)
    const b = zonedToUtc(cTo, tz)
    if (isNaN(a) || isNaN(b) || b <= a) {
      setBad(true)
      return
    }
    setBad(false)
    setFrom(null)
    setTo(null)
    setOpen(false)
    set({ range: null, from: new Date(a).toISOString().replace('.000Z', 'Z'), to: new Date(b).toISOString().replace('.000Z', 'Z') })
  }
  const label = custom ? `${from ? dayTime(from, tz) : '…'} – ${to ? dayTime(to, tz) : '…'}` : (rangeWords[range] ?? (() => range))()
  return (
    <PopWrap open={open} setOpen={setOpen} btn={btn}>
      <button ref={btn} type="button" className="ex-btn time" aria-haspopup="dialog" aria-expanded={open} aria-controls="ex-time"
        onClick={() => setOpen(!open)}>
        <ClockIcon size={16} />
        <span className="time-label">{label}</span>
        <span className="mono muted time-zone">{tz === 'UTC' ? 'UTC' : offset(tz)}</span>
        <ChevronDown size={12} />
      </button>
      {open && (
        <div id="ex-time" role="dialog" aria-label={t('Time range')} className="ex-pop time-pop">
          <div className="tp-col">
            <h3 className="label">{t('Quick ranges')}</h3>
            <div className="tp-presets" role="group" aria-label={t('Quick ranges')}>
              {quickRanges.map((r, i) => (
                <button key={r} ref={(range === r || (!range && i === 0)) ? first : undefined} type="button" aria-pressed={range === r}
                  onClick={() => { setOpen(false); btn.current?.focus(); set({ range: r === '24h' ? null : r, from: null, to: null }) }}>
                  <span>{rangeWords[r]()}</span><span className="mono muted">{r}</span>
                </button>
              ))}
            </div>
          </div>
          <div className="tp-col tp-custom">
            <h3 className="label">{t('Custom range')}</h3>
            <label className="field"><span>{t('From ({tz})', { tz })}</span><input className="control" type="datetime-local" value={cFrom} onChange={(e) => setFrom(e.target.value)} /></label>
            <label className="field"><span>{t('To ({tz})', { tz })}</span><input className="control" type="datetime-local" value={cTo} onChange={(e) => setTo(e.target.value)} /></label>
            {bad && <span className="fail tp-bad" role="status">{t('The end has to be after the start.')}</span>}
            <button type="button" className="button" onClick={apply}>{t('Apply')}</button>
          </div>
          <div className="tp-zonerow">
            <h3 className="label">{t('Clock')}</h3>
            <ZoneSelect tz={tz} setTz={setTz} compact />
          </div>
        </div>
      )}
    </PopWrap>
  )
}

/** Whether the view follows new records: on while the range ends now; off pins it where it is. */
export function LiveToggle({ live, connected, onToggle }: { live: boolean; connected: boolean; onToggle: () => void }) {
  const tip = !live ? t('Paused: the range is fixed. Press to follow new records again.') : connected ? t('Live: refreshes as new records arrive. Press to pause.') : t('Live, but the update stream is not connected: reload to retry.')
  return (
    <button type="button" className={`ex-btn live${live ? ' on' : ''}${connected ? '' : ' offline'}`} aria-pressed={live} data-tip={tip} aria-label={`${t('Live')}. ${tip}`} onClick={onToggle}>
      <i className="dot" aria-hidden="true" />{t('Live')}
    </button>
  )
}

/** Workspace, download and link: icon buttons with their words in a tooltip and for screen readers. */
export function Actions({ wsCount, filterQuery }: { wsCount: number; filterQuery: Record<string, string> }) {
  const [open, setOpen] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const [msgs, setMsgs] = useState(false)
  const [copied, setCopied] = useState('')
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(location.href)
      setCopied(t('Link copied'))
    } catch {
      setCopied(t('Could not copy; the link is in the address bar.'))
    }
    window.setTimeout(() => setCopied(''), 2500)
  }
  return (
    <>
      <Link href="/workspace" className="ibtn" data-tip={tn(wsCount, 'Workspace ({n} record)', 'Workspace ({n} records)')}
        aria-label={tn(wsCount, 'Workspace, {n} record', 'Workspace, {n} records')}>
        <LayersIcon size={18} />
        {wsCount > 0 && <span className="badge mono" aria-hidden="true">{wsCount}</span>}
      </Link>
      <PopWrap open={open} setOpen={setOpen} btn={btn}>
        <button ref={btn} type="button" className="ibtn" aria-haspopup="dialog" aria-expanded={open} aria-controls="ex-export"
          data-tip={t('Download the filtered records as JSON')} aria-label={t('Download JSON')} onClick={() => setOpen(!open)}>
          <DownloadIcon size={18} />
        </button>
        {open && (
          <div id="ex-export" role="dialog" aria-label={t('Download JSON')} className="ex-pop export-pop">
            <h3 className="label">{t('Download JSON')}</h3>
            <p>{t('Downloads what the filters match (oldest first, up to 5,000 records) as a versioned JSON file. The file says if it stopped early. Nothing is sent anywhere.')}</p>
            <label className="check">
              <input type="checkbox" checked={msgs} onChange={(e) => setMsgs(e.target.checked)} />
              <span>{t('Include Kubernetes Event messages and quoted condition messages')}{msgs && <b className="fail">{t(' — they can contain names, addresses or other sensitive text, and go into the file.')}</b>}</span>
            </label>
            <a className="button" download href={`/api/v1/explorer/export${query({ ...filterQuery, messages: msgs ? '1' : '' })}`} onClick={() => setOpen(false)}>
              <DownloadIcon size={16} />{t('Download filtered view')}
            </a>
          </div>
        )}
      </PopWrap>
      <button type="button" className="ibtn" data-tip={copied || t('Copy a link to this view')} aria-label={t('Copy link')} onClick={copy}>
        <LinkIcon size={18} />
      </button>
      <span className="sr" role="status">{copied}</span>
    </>
  )
}

/** On a narrow screen the facet sidebar is behind this button. */
export function FiltersButton({ n, open, onClick }: { n: number; open: boolean; onClick: () => void }) {
  return (
    <button type="button" className="ex-btn filters-btn" aria-expanded={open} aria-controls="ex-facets" onClick={onClick}>
      <FilterIcon size={16} />{n > 0 ? t('Filters ({n})', { n }) : t('Filters')}
    </button>
  )
}

export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="ex-bar" role="search" aria-label={t('Query')}>{children}</div>
}
