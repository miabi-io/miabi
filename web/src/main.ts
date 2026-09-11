import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import '@mdi/font/css/materialdesignicons.css'
import '@fontsource/quicksand/700.css'
import './assets/styles.css'
import './assets/accents.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')
