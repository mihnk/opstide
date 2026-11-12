import { useEffect, useState } from 'react'
import { getJSON, type System } from './api'
import { Shell } from './components/Shell'
import { savedZone, saveZone } from './prefs'
import { logToExplorer } from './explorer'
import { navigate, useLocation } from './router'
import { Explorer } from './screens/Explorer'
import { Operation } from './screens/Operation'
import { Schedules } from './screens/Schedules'
import { System as SystemScreen } from './screens/System'
import { Overview } from './screens/Overview'
import { Node } from './screens/Node'
import { Release } from './screens/Release'
import { Releases } from './screens/Releases'
import { Workload } from './screens/Workload'
import { Workloads } from './screens/Workloads'
import { Image, Images } from './screens/Images'
import { Workspace } from './screens/Workspace'
import { Onboarding, onboarded } from './screens/Onboarding'
import { LANG_EVENT, t } from './i18n'

/** The Log became the Explorer; an old link or bookmark lands on the same records there. */
function LogMoved({ params }: { params: URLSearchParams }) {
  useEffect(() => navigate(logToExplorer(params), true), [params])
  return null
}

export function App() {
  const { path, params } = useLocation()
  const [tz, setTzState] = useState(savedZone)
  const [cluster, setCluster] = useState<string>()
  const [demo, setDemo] = useState(false)
  const [, redraw] = useState(0)
  useEffect(() => {
    const on = () => redraw((n) => n + 1)
    window.addEventListener(LANG_EVENT, on)
    return () => window.removeEventListener(LANG_EVENT, on)
  }, [])
  const setTz = (z: string) => {
    saveZone(z)
    setTzState(z)
  }

  useEffect(() => {
    getJSON<System>('/api/v1/system').then((s) => { setCluster(s.cluster); setDemo(!!s.demo) }).catch(() => {})
  }, [])

  useEffect(() => {
    const names: Record<string, string> = { '/': 'Overview', '/explorer': 'Explorer', '/workspace': 'Workspace', '/releases': 'Releases', '/workloads': 'Workloads', '/images': 'Images', '/schedules': 'Schedules', '/system': 'System' }
    const intro = path === '/welcome' || (path === '/' && !onboarded() && params.toString() === '')
    document.title = `${t(intro ? 'Welcome' : names[path] ?? (path.startsWith('/images/') ? 'Image' : 'Operation'))} · OpsTide${cluster ? ` · ${cluster}` : ''}`
  }, [path, params, cluster])

  let screen
  const op = /^\/operations\/([^/]+)$/.exec(path)
  const rel = /^\/releases\/([^/]+)$/.exec(path)
  const wl = /^\/workloads\/([^/]+)$/.exec(path)
  const node = /^\/nodes\/([^/]+)$/.exec(path)
  const img = /^\/images\/([A-Za-z0-9_-]+)$/.exec(path)
  // The first visit in a browser starts with the introduction; it is one click away afterwards, from System. A link
  // that names a window (/?date=…) is someone sharing what they saw: it opens that, not the introduction.
  const intro = path === '/welcome' || (path === '/' && params.toString() === '' && !onboarded())
  if (intro) return <Onboarding params={params} />
  if (path === '/') screen = <Overview params={params} tz={tz} setTz={setTz} />
  else if (path === '/explorer') screen = <Explorer params={params} tz={tz} setTz={setTz} />
  else if (path === '/workspace') screen = <Workspace tz={tz} setTz={setTz} />
  else if (path === '/releases') screen = <Releases params={params} tz={tz} />
  else if (rel) screen = <Release key={rel[1]} id={decodeURIComponent(rel[1])} params={params} tz={tz} setTz={setTz} />
  else if (path === '/workloads') screen = <Workloads params={params} tz={tz} />
  else if (wl) screen = <Workload key={wl[1]} id={decodeURIComponent(wl[1])} params={params} tz={tz} />
  else if (path === '/images') screen = <Images params={params} tz={tz} />
  else if (img) screen = <Image key={img[1]} refId={img[1]} tz={tz} />
  else if (node) screen = <Node key={node[1]} id={decodeURIComponent(node[1])} tz={tz} />
  else if (path === '/log') screen = <LogMoved params={params} />
  else if (path === '/schedules') screen = <Schedules params={params} tz={tz} setTz={setTz} />
  else if (op) screen = <Operation key={op[1]} id={decodeURIComponent(op[1])} params={params} tz={tz} />
  else if (path === '/system') screen = <SystemScreen tz={tz} />
  else screen = <div className="page"><div className="empty">{t('There is no page here.')} <a href="/">{t('Go to the Overview')}</a></div></div>

  return <Shell path={path} cluster={cluster} demo={demo}>{screen}</Shell>
}
