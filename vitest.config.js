import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['test/**/*.test.js', 'static/**/*.test.js'],
    globals: true,
    deps: {
      inline: [/static\//]
    }
  },
});