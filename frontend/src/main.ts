import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n } from './locales'
import { useUIStore } from './stores/ui'
import { vAutofocus } from './directives/autofocus'
import './styles/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(i18n)
app.directive('autofocus', vAutofocus)

// Apply persisted theme/lang before mount.
useUIStore().applyTheme()

app.mount('#app')
