import { createRoot } from 'react-dom/client'
import App from './App'
import { useUIStore } from './stores/ui'
import './styles/main.css'

// Apply persisted theme/lang before mount.
useUIStore.getState().applyTheme()

createRoot(document.getElementById('app')!).render(<App />)
