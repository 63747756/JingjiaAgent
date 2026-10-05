import path from "path"
import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, loadEnv, searchForWorkspaceRoot } from "vite"

const fontPackageSpecifiers = [
  "@fontsource-variable/jetbrains-mono",
  "@fontsource-variable/noto-sans-sc",
]

export function resolveFontPackageDirectories(
  resolveModule: (specifier: string) => string = (specifier) =>
    import.meta.resolve(specifier),
): string[] {
  return [...new Set(
    fontPackageSpecifiers.map((specifier) =>
      path.dirname(fileURLToPath(resolveModule(`${specifier}/wght.css`))),
    ),
  )]
}

export function resolveDevServerFsAllow(
  workspaceRoot: string,
  resolveModule: (specifier: string) => string = (specifier) =>
    import.meta.resolve(specifier),
): string[] {
  return [...new Set([
    workspaceRoot,
    ...resolveFontPackageDirectories(resolveModule),
  ])]
}

interface OnlineProxyTargetOptions {
  command: string
  appEdition: string | undefined
  target: string | undefined
}

export function resolveOnlineProxyTarget({
  command,
  appEdition,
  target,
}: OnlineProxyTargetOptions): string | undefined {
  const normalizedTarget = target?.trim()

  if (command !== 'serve' || appEdition !== 'online') {
    return normalizedTarget || undefined
  }

  if (!normalizedTarget) {
    throw new Error(
      'TARGET is required for online preview. Example: TARGET=http://127.0.0.1:47424 pnpm run dev:online',
    )
  }

  let parsedTarget: URL
  try {
    parsedTarget = new URL(normalizedTarget)
  } catch {
    throw new Error('TARGET must be an absolute HTTP(S) URL')
  }

  if (parsedTarget.protocol !== 'http:' && parsedTarget.protocol !== 'https:') {
    throw new Error('TARGET must be an absolute HTTP(S) URL')
  }

  if (
    parsedTarget.username ||
    parsedTarget.password ||
    parsedTarget.pathname !== '/' ||
    parsedTarget.search ||
    parsedTarget.hash
  ) {
    throw new Error('TARGET must contain only an HTTP(S) origin')
  }

  return parsedTarget.origin
}

// https://vite.dev/config/
export default defineConfig(({ mode, command }) => {
  const sourceLock = JSON.parse(readFileSync(new URL('../runtime/jingjiaagent/source.lock.json', import.meta.url), 'utf8'))
  const env = loadEnv(mode, __dirname, '')
  const appEdition = process.env.VITE_JINGJIAAGENT_APP_EDITION ?? env.VITE_JINGJIAAGENT_APP_EDITION
  const proxyTarget = resolveOnlineProxyTarget({
    command,
    appEdition,
    target: env.TARGET,
  })
  const devPort = 11180
  const proxyBasicAuthUsername = env.PROXY_BASIC_AUTH_USERNAME?.trim()
  const proxyBasicAuthPassword = env.PROXY_BASIC_AUTH_PASSWORD?.trim()
  const proxyHeaders: Record<string, string> = {}
  const usesOnlineProxy = command === 'serve' && appEdition === 'online'
  const proxyTargetUrl = usesOnlineProxy && proxyTarget ? new URL(proxyTarget) : undefined
  const proxyTargetIsLoopback = Boolean(
    proxyTargetUrl && ['localhost', '127.0.0.1', '[::1]'].includes(proxyTargetUrl.hostname),
  )

  if (appEdition !== 'online' && appEdition !== 'offline') {
    throw new Error(
      `Invalid VITE_JINGJIAAGENT_APP_EDITION: ${appEdition ?? '(missing)'}. Expected "online" or "offline".`,
    )
  }

  if (usesOnlineProxy && Boolean(proxyBasicAuthUsername) !== Boolean(proxyBasicAuthPassword)) {
    throw new Error('Proxy basic authentication requires both username and password')
  }

  if (usesOnlineProxy && proxyBasicAuthUsername && proxyBasicAuthPassword) {
    if (proxyTargetUrl?.protocol !== 'https:' && !proxyTargetIsLoopback) {
      throw new Error('Proxy basic authentication requires an HTTPS or loopback TARGET')
    }
    proxyHeaders.Authorization = `Basic ${Buffer.from(`${proxyBasicAuthUsername}:${proxyBasicAuthPassword}`).toString('base64')}`
  }

  if (!Number.isInteger(sourceLock.frontend_patch_revision) || sourceLock.frontend_patch_revision < 1) {
    throw new Error('Missing or invalid JingjiaAgent frontend image revision in source.lock.json')
  }

  return {
    base: '/',
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    define: {
      "__JINGJIAAGENT_FRONTEND_REVISION__": JSON.stringify(`p${sourceLock.frontend_patch_revision}`),
      "global": "globalThis",
    },
    optimizeDeps: {
      include: ["buffer"],
      esbuildOptions: {
        define: {
          global: "globalThis",
        },
      },
    },
    server: {
      host: "0.0.0.0",
      port: devPort,
      allowedHosts: ['.jingjiaagent.online'],
      fs: {
        allow: resolveDevServerFsAllow(
          searchForWorkspaceRoot(__dirname),
        ),
      },
      proxy: {
        '/api': {
          target: proxyTarget,
          changeOrigin: true,
          secure: !proxyTargetIsLoopback,
          ws: true,
          ...(Object.keys(proxyHeaders).length > 0
            ? {
                headers: proxyHeaders,
              }
            : {}),
        }
      }
    }
  }
})
