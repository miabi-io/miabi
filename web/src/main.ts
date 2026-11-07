import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n } from './i18n'
import { useLanguageStore } from './stores/language'
import '@mdi/font/css/materialdesignicons.css'
import '@fontsource/quicksand/700.css'
import './assets/styles.css'
import './assets/accents.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(i18n)
app.use(router)

// Applied before first paint so an in-flight /me doesn't flash the wrong language.
void useLanguageStore(pinia)
  .init()
  .finally(() => app.mount('#app'))
