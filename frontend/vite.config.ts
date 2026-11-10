/**
 * 管理端开发代理和生产静态资源构建配置。
 *
 * @author Andersen
 */
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

/** projectRoot 是加载仓库根目录 .env 的绝对路径。 */
const projectRoot = fileURLToPath(new URL('..', import.meta.url))

/** managementProxyTarget 将 Go 监听地址转换为 Vite 可访问的管理端代理地址。 */
function managementProxyTarget(listenAddress: string): string {
  /** address 是去除空白后的监听地址。 */
  const address = listenAddress.trim() || ':8080'
  if (address.startsWith(':')) {
    return `http://127.0.0.1${address}`
  }
  if (address.startsWith('0.0.0.0:')) {
    return `http://127.0.0.1:${address.slice('0.0.0.0:'.length)}`
  }
  if (address.startsWith('[::]:')) {
    return `http://127.0.0.1:${address.slice('[::]:'.length)}`
  }
  return `http://${address}`
}

/** Vite 配置回调按当前模式加载 ACME_ 前缀环境变量。 */
export default defineConfig(({ mode }) => {
  /** env 是仓库根目录加载的 ACME_ 配置。 */
  const env = loadEnv(mode, projectRoot, 'ACME_')
  /** proxyTarget 是开发服务器转发管理 API 的目标地址。 */
  const proxyTarget = managementProxyTarget(env.ACME_LISTEN_ADDRESS ?? ':8080')

  return {
    plugins: [vue()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port: 5173,
      proxy: {
        '/api': proxyTarget,
        '/readyz': proxyTarget,
        '/healthz': proxyTarget,
      },
    },
    build: {
      outDir: '../internal/web/dist',
      emptyOutDir: true,
      chunkSizeWarningLimit: 700,
    },
  }
})
