import '@fontsource-variable/archivo/wdth.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/latin-500.css'
import './styles.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { LiveProvider } from './live'
import { applyTheme, savedTheme, systemTheme } from './prefs'

applyTheme(savedTheme() ?? systemTheme())
// Follow the system theme until the reader picks one.
window.matchMedia?.('(prefers-color-scheme: light)').addEventListener?.('change', () => {
  if (!savedTheme()) applyTheme(systemTheme())
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <LiveProvider>
      <App />
    </LiveProvider>
  </StrictMode>,
)
