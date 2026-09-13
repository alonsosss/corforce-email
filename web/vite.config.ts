/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// En desarrollo Vite sirve la aplicacion y reenvia /api al gateway: asi el navegador ve un
// solo origen, igual que en produccion, y la cookie HttpOnly del refresh (SameSite=Strict,
// Path=/api/v1/auth) viaja sin ninguna excepcion de CORS.
const DEFAULT_DEV_PROXY_TARGET = 'http://127.0.0.1:8080';

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  return {
    plugins: [react()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port: Number(env.VITE_DEV_PORT) || 3000,
      strictPort: false,
      proxy: {
        '/api': {
          target: env.VITE_DEV_PROXY_TARGET || DEFAULT_DEV_PROXY_TARGET,
          changeOrigin: false,
        },
      },
    },
    build: {
      target: 'es2020',
      sourcemap: false,
      // CSP con nonce y 'strict-dynamic': el gateway solo estampa el nonce en los <script>
      // de index.html. Por eso el HTML no debe llevar scripts inline (el polyfill de
      // modulepreload va dentro del bundle de entrada) ni <link rel="modulepreload">
      // escritos por el parser, que sin nonce quedarian bloqueados. Sin manualChunks la
      // entrada no importa estaticamente otros chunks y el HTML solo lleva el script de
      // entrada; los chunks de cada pantalla se cargan con import() desde codigo ya
      // confiable, que 'strict-dynamic' admite.
      modulePreload: { polyfill: true },
    },
    test: {
      environment: 'jsdom',
      globals: false,
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: false,
    },
  };
});
