import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n } from './locales'
import { useUIStore } from './stores/ui'
import { registerDirectives } from './directives'
import './styles/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(i18n)
registerDirectives(app)

// Apply persisted theme/lang before mount.
useUIStore().applyTheme()

app.mount('#app')
