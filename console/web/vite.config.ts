import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The built assets are embedded into the Runtime binary from ../dist, so the
// build output is the contract between this project and console/embed.go.
//
// During development the Console runs on the Vite dev server and proxies the API
// to a locally running Runtime. Point WIA_DEV_RUNTIME at it when the Runtime is
// not on the default development address.
const runtime = process.env.WIA_DEV_RUNTIME ?? 'http://127.0.0.1:8765'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../dist',
    // The output directory is deliberately not emptied by Vite: this directory
    // holds the tracked placeholder that keeps `//go:embed all:dist` valid, and
    // emptying it would delete that placeholder on every build.
    //
    // Stale bundles must not be left behind either, because the embed packs
    // whatever is here into the Runtime binary. `npm run build` therefore runs
    // scripts/prepare-dist.mjs first, which removes everything but the
    // placeholder. Run Vite directly only if you accept a growing binary.
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': { target: runtime, changeOrigin: false },
    },
  },
})
