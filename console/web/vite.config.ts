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
    // The output directory is deliberately not emptied. It holds the tracked
    // placeholder that keeps `//go:embed all:dist` valid, and emptying it would
    // delete that placeholder on every build: the working tree would show a
    // deleted file, and building from an emptied tree would fail.
    //
    // Stale hashed assets from earlier builds are left behind. They are
    // unreferenced, so they are only disk noise; index.html always points at the
    // bundle this build produced.
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': { target: runtime, changeOrigin: false },
    },
  },
})
