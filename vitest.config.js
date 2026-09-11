import { defineConfig } from 'vitest/config';

// Coverage floor (unit U8): no-regression floor for `static/` statements at the
// measured post-work level. Enforced on every `npm run check` / `npm run test:coverage`
// run via vitest's built-in coverage.thresholds (no new dependencies).
export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['test/**/*.test.js', 'static/**/*.test.js'],
    globals: true,
    deps: {
      inline: [/static\//]
    },
    coverage: {
      // Scope the floor to the hand-written frontend sources only.
      // Text report only: coverage/ holds the tracked Go HTML coverage report,
      // so vitest must not write any coverage files into the tree.
      reporters: ['text'],
      include: ['static/**'],
      exclude: [
        'static/vendor/**',
        'static/sw.js',
        'src/**',
        'scripts/**',
        'static/**/*.test.js'
      ],
      thresholds: {
        statements: 53.06
      }
    }
  },
});