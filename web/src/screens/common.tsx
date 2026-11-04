import { browserZone, offset } from '../time'

/** The zone times are shown in: the browser's, UTC, or one chosen. */
export function ZoneSelect({ tz, setTz }: { tz: string; setTz: (z: string) => void }) {
  const zones = Array.from(new Set([browserZone(), 'UTC', tz]))
  return (
    <label className="field">
      <span className="label">Clock</span>
      <select className="control mono" style={{ fontSize: 13 }} value={tz} onChange={(e) => setTz(e.target.value)}>
        {zones.map((z) => (
          <option key={z} value={z}>{z === 'UTC' ? 'UTC' : `${z} ${offset(z)}`}</option>
        ))}
      </select>
    </label>
  )
}
