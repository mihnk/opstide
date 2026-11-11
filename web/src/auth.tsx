import { createContext, useCallback, useContext, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { UNAUTHORIZED } from './api'
import { t } from './i18n'

// OpsTide can ask for a token before it shows anything. The page and its
// scripts are open; the data is not. AuthGate asks the server whether a
// token is needed and whether this browser already has a session, shows the
// sign-in form if not, and shows it again whenever the API answers 401 (the
// token was rotated).

interface Auth {
  required: boolean
  logout: () => void
}

const AuthContext = createContext<Auth>({ required: false, logout: () => {} })

export function useAuth(): Auth {
  return useContext(AuthContext)
}

type State = { phase: 'checking' } | { phase: 'in'; required: boolean } | { phase: 'out'; error?: string }

async function post(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State>({ phase: 'checking' })
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  // Signing in mounts the app again from scratch, so its live stream
  // reconnects with the new cookie.
  const [session, setSession] = useState(0)

  useEffect(() => {
    fetch('/api/v1/session', { headers: { Accept: 'application/json' } })
      .then((res) => {
        if (!res.ok) throw new Error(String(res.status))
        return res.json() as Promise<{ authRequired: boolean; authenticated: boolean }>
      })
      .then((s) => setState(s.authRequired && !s.authenticated ? { phase: 'out' } : { phase: 'in', required: s.authRequired }))
      // The server is not reachable; let the app show its own errors.
      .catch(() => setState({ phase: 'in', required: false }))
    // A 401 means a token is needed, whatever the first check made of it
    // (a proxy error page, a server that has since been given a token).
    const again = () => setState((s) => (s.phase === 'out' ? s : { phase: 'out' }))
    window.addEventListener(UNAUTHORIZED, again)
    return () => window.removeEventListener(UNAUTHORIZED, again)
  }, [])

  const logout = useCallback(() => {
    void post('/api/v1/logout').finally(() => {
      setToken('')
      setState({ phase: 'out' })
    })
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !token.trim()) return
    setBusy(true)
    try {
      const res = await post('/api/v1/login', { token })
      if (res.ok) {
        setToken('')
        setSession((n) => n + 1)
        setState({ phase: 'in', required: true })
      } else if (res.status === 429) {
        const secs = Number(res.headers.get('Retry-After'))
        setState({ phase: 'out', error: secs > 0 ? t('Too many wrong tokens. Try again in {n} seconds.', { n: secs }) : t('Too many wrong tokens. Try again in a minute.') })
      } else if (res.status === 401) {
        setState({ phase: 'out', error: t('That is not the token.') })
      } else {
        setState({ phase: 'out', error: t('The server answered {status}.', { status: res.status }) })
      }
    } catch {
      setState({ phase: 'out', error: t('Could not reach OpsTide.') })
    } finally {
      setBusy(false)
    }
  }

  if (state.phase === 'checking') return <div className="empty" aria-busy="true" style={{ padding: 40 }}>{t('Loading…')}</div>
  if (state.phase === 'out') {
    return (
      <div className="login">
        <h1>OpsTide</h1>
        <p>{t('This OpsTide asks for a token. The person who installed it can read it from the cluster; see the README.')}</p>
        <form onSubmit={submit}>
          <label className="field">
            <span className="muted">{t('Token')}</span>
            <input className="control mono" type="password" autoComplete="current-password" autoFocus
              value={token} onChange={(e) => setToken(e.target.value)} aria-invalid={state.error ? true : undefined}
              aria-describedby={state.error ? 'login-error' : undefined} />
          </label>
          {state.error && <div id="login-error" className="error" role="alert">{state.error}</div>}
          <button type="submit" className="button" disabled={busy || !token.trim()}>{t('Sign in')}</button>
        </form>
      </div>
    )
  }
  return (
    <AuthContext.Provider value={{ required: state.required, logout }}>
      <div key={session} style={{ display: 'contents' }}>{children}</div>
    </AuthContext.Provider>
  )
}
