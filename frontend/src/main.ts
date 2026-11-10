/**
 * 管理端前端应用入口，安装状态仓库和路由后挂载根组件。
 *
 * @author Andersen
 */
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/main.css'

createApp(App).use(createPinia()).use(router).mount('#app')
