import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { Suspense } from 'react'
import App from './App.jsx'
import './i18n.js'
import './fluent-reveal.js'

import { CurrencyProvider } from './context/CurrencyContext.jsx'
import { ThemeProvider } from './context/ThemeContext.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <ThemeProvider>
      <CurrencyProvider>
        <Suspense fallback={<div className="h-screen w-screen bg-surface flex items-center justify-center text-on-surface font-mono">Loading System Matrices...</div>}>
          <App />
        </Suspense>
      </CurrencyProvider>
    </ThemeProvider>
  </StrictMode>,
)
