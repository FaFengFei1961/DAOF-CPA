import { StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
import './i18n.js'

import { CurrencyProvider } from './context/CurrencyContext.jsx'
import { ThemeProvider } from './context/ThemeContext.jsx'
import { redirectLegacyHash } from './utils/hashRedirect.js'

// Phase 0：旧 hash 路由兼容（必须在 BrowserRouter 挂载前执行）
redirectLegacyHash();

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <ThemeProvider>
      <CurrencyProvider>
        <Suspense fallback={<div className="h-screen w-screen bg-surface flex items-center justify-center text-sm text-on-surface-variant">Loading...</div>}>
          <App />
        </Suspense>
      </CurrencyProvider>
    </ThemeProvider>
  </StrictMode>,
)
