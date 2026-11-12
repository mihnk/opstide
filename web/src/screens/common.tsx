import { Choice, type ChoiceOption } from '../components/Choice'
import { t } from '../i18n'
import { browserZone, offset } from '../time'

/** The zone times are shown in: the browser's ("Local time"), UTC, or one chosen earlier that is neither. A row of
 *  buttons, not a drop-down: the two usual answers are always in sight and one press away. `compact` is for a header
 *  row beside other controls; the full form is taller and names the browser's zone. */
export function ZoneSelect({ tz, setTz, compact }: { tz: string; setTz: (z: string) => void; compact?: boolean }) {
  const local = browserZone()
  const options: ChoiceOption<string>[] = []
  if (local !== 'UTC') {
    options.push({
      value: local,
      name: compact ? t('Local') : t('Local time'),
      detail: compact ? offset(local) : `${local} ${offset(local)}`,
      full: t('Local time: {zone}, {offset}', { zone: local, offset: offset(local) }),
    })
  }
  options.push({ value: 'UTC', name: 'UTC', full: local === 'UTC' ? t('UTC, also the local time') : 'UTC' })
  if (tz !== local && tz !== 'UTC') options.push({ value: tz, name: tz, detail: offset(tz), full: `${tz}, ${offset(tz)}` })
  return (
    <div className={`field${compact ? '' : ' clock-field'}`}>
      {!compact && <span className="label" aria-hidden="true">{t('Clock')}</span>}
      <Choice label={t('Clock')} value={tz} options={options} onChange={setTz} size={compact ? 'compact' : 'large'} marks className="clock"
        lead={compact ? <ClockIcon /> : undefined} />
    </div>
  )
}

function ClockIcon() {
  return (
    <span className="choice-lead" aria-hidden="true">
      <svg width="16" height="16" viewBox="0 0 16 16"><circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.5" /><path d="M8 4.2V8l2.6 1.6" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" /></svg>
    </span>
  )
}
