import { useEffect, useState } from 'react'
import { getJSON, type System } from './api'
import { Shell } from './components/Shell'
import { savedZone, saveZone } from './prefs'
import { useLocation } from './router'
import { Log } from './screens/Log'
import { Operation } from './screens/Operation'
import { Schedules } from './screens/Schedules'
import { System as SystemScreen } from './screens/System'
import { Today } from './screens/Today'

export function App() {
  const { path, params } = useLocation()
  const [tz, setTzState] = useState(savedZone)
  const [cluster, setCluster] = useState<string>()
  const setTz = (z: string) => {
    saveZone(z)
    setTzState(z)
  }

  useEffect(() => {
    getJSON<System>('/api/v1/system').then((s) => setCluster(s.cluster)).catch(() => {})
  }, [])

  useEffect(() => {
    const names: Record<string, string> = { '/': 'Today', '/log': 'Log', '/schedules': 'Schedules', '/system': 'System' }
    document.title = `${names[path] ?? 'Operation'} · OpsTide${cluster ? ` · ${cluster}` : ''}`
  }, [path, cluster])

  let screen
  const op = /^\/operations\/([^/]+)$/.exec(path)
  if (path === '/') screen = <Today params={params} tz={tz} setTz={setTz} />
  else if (path === '/log') screen = <Log params={params} tz={tz} />
  else if (path === '/schedules') screen = <Schedules params={params} tz={tz} setTz={setTz} />
  else if (op) screen = <Operation key={op[1]} id={decodeURIComponent(op[1])} params={params} tz={tz} />
  else if (path === '/system') screen = <SystemScreen tz={tz} />
  else screen = <div className="page"><div className="empty">There is no page here. <a href="/">Go to Today</a></div></div>

  return <Shell path={path} cluster={cluster}>{screen}</Shell>
}
