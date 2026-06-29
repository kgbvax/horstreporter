import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { resolve } from 'path';

// Builds the Svelte UI shell into static/dist so //go:embed and -dev disk
// serving both pick it up without backend changes. index.html loads the
// emitted entry as a module; the canvas/vanilla modules stay in static/.
export default defineConfig({
    plugins: [svelte()],
    root: resolve(__dirname, 'src'),
    build: {
        outDir: resolve(__dirname, 'static/dist'),
        emptyOutDir: true,
        lib: {
            entry: resolve(__dirname, 'src/main.js'),
            formats: ['es'],
            fileName: () => 'horst-ui.js',
        },
        rollupOptions: {
            output: { assetFileNames: 'horst-ui.[ext]' },
        },
    },
});
