import { browserZone, offset } from '../time'

/** The zone times are shown in: the browser's, UTC, or one chosen. */
export function ZoneSelect({ tz, setTz, compact }: { tz: string; setTz: (z: string) => void; compact?: boolean }) {
  const zones = Array.from(new Set([browserZone(), 'UTC', tz]))
  return (
    <label className="field">
      <span className={compact ? 'sr' : 'label'}>Clock</span>
      <select className="control mono" style={{ fontSize: 13 }} value={tz} onChange={(e) => setTz(e.target.value)} aria-label="Clock">
        {zones.map((z) => (
          <option key={z} value={z}>{z === 'UTC' ? 'UTC' : `${z} ${offset(z)}`}</option>
        ))}
      </select>
    </label>
  )
}
