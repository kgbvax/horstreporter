# Dead Code & Unused Feature Inventory

This file is the verified inventory of dead, orphan, or deprecated code
in the horstreporter Go binary and its in-tree frontend (`static/`).
**It is inventory only — no deletions are proposed in this commit.**
A follow-up commit can act on this list; each removal must re-run the
relevant grep on the then-current tree to confirm no caller has been
added between this commit and the removal.

## Scope and method

- Go binary: `*.go` at the repo root, including `cmd/horstoperator-agent`
  and `cmd/horstprop` (subtree-level; exported functions that are
  exercised from `main_test.go` are covered by the same grep).
- Frontend: everything under `static/`.
- Out of scope: third-party scripts, the operator's local browser
  automation, and any external API consumer. The legacy response shapes
  stay so external callers don't break.
- Conservativity rule: a symbol is listed only when a positive
  zero-match `grep` proves the absence of in-tree callers. If there is
  any second match in the source (including comments, tests, or
  docstrings) the finding is dropped from the "confirmed" lists.

Verification greps used in this pass (all run on the repo root):

- `grep -rn '\b<SYMBOL>\b' --include='*.go' .` — Go symbol callers.
- `grep -rn "from ['\"]\./<file>.js['\"]" static/` — ES module importers.
- `grep -rn "fetch(['\"`][^'\"]*/api/" static/` — API callers.
- `grep -rn 'navigator.serviceWorker.register' static/` — Service Worker
  registrations.
- `grep -n 'localStorage\.\(get\|set\)Item' static/` — localStorage keys.

---

## 1. Confirmed dead (Go)

### 1.1 Baseline engine methods

Both methods are declared on `*DxBaselineEngine` and have no in-tree
callers. The Postgres-backed engine (`dx_postgres.go`) replaces them.

- `dx_conditions.go:285` — `func (e *DxBaselineEngine) LoadSpotsBetween(start, end int64) ([]MQTTMessage, error)`
  - Grep: `grep -rn '\bLoadSpotsBetween\b' --include='*.go' .`
    → only the declaration at `dx_conditions.go:285`. Zero call sites.
  - Reason: leftover from the in-memory engine prototype; the
    Postgres-backed engine (`dx_postgres.go`) replaces it.
- `dx_conditions.go:310` — `func (e *DxBaselineEngine) NumBuckets() int`
  - Grep: `grep -rn '\bNumBuckets\b' --include='*.go' .`
    → only the declaration at `dx_conditions.go:310`. Zero call sites.
  - Reason: same as above; the Postgres-backed engine reports bucket
    counts via a different path.

### 1.2 `CellBucketFeed` methods

The four methods below are exported on `*CellBucketFeed` but have no
external callers. The binary has no graceful-shutdown path, so
`Stop` is never invoked; `loop` is the goroutine started by `Start`;
`tick` is its internal callback; `cleanupDedup` is called only from
`tick`. Together they are ~80 lines that a follow-up can delete while
keeping `Start`/`Observe`/`Backfill` (the hot path for the pathscope
integration).

- `dx_cellfeed.go:222` — `func (s *CellBucketFeed) Stop()`
  - Grep: `grep -rn 'cellBucketFeed\.Stop' --include='*.go' .`
    → zero matches.
- `dx_cellfeed.go:230` — `func (s *CellBucketFeed) loop()`
  - Grep: `grep -rn 'cellBucketFeed\.loop' --include='*.go' .`
    → zero matches (only the internal `go s.loop()` in `Start` and the
    method declaration itself; both are intra-file).
- `dx_cellfeed.go:244` — `func (s *CellBucketFeed) tick()`
  - Grep: `grep -rn 'cellBucketFeed\.tick' --include='*.go' .`
    → zero matches (only `s.tick()` inside `loop`, intra-file).
- `dx_cellfeed.go:318` — `func (s *CellBucketFeed) cleanupDedup()`
  - Grep: `grep -rn 'cellBucketFeed\.cleanupDedup' --include='*.go' .`
    → zero matches (only `s.cleanupDedup()` inside `tick`, intra-file).

`Observe` (`dx_cellfeed.go:274`) and `Backfill` (`dx_cellfeed.go:309`)
ARE called from `dx_conditions.go:425-445` and `main.go:386-389`; do
not list them.

### 1.3 Deprecated opmode command-line flags (RESOLVED 2026-09-08: `-opmode-enable` and `-opmode-agent-timeout-ms` removed; `-opmode-agent-url` kept registered so the prod systemd unit keeps starting)

Each flag below is accepted on the command line and consumed only by
a single `logInfo(...)` line that announces the flag as deprecated and
ignored. No other effect on runtime behavior.

- `main.go:258` — `flag.Bool("opmode-enable", true, "Deprecated: backend opmode integration endpoints are always enabled")`
  - Wired at `main.go:293`:
    `if !*opModeEnableFlag { logInfo("-opmode-enable=false is deprecated and ignored; backend opmode endpoints remain active") }`
  - External references: `README.md:113` shows it in a run example
    (`-opmode-enable \`). Not zero-caller, but its only effect is the
    log line. Recommendation: drop the flag and the log line; update
    `README.md` accordingly.
- `main.go:260` — `flag.String("opmode-agent-url", "", "Deprecated and ignored: backend never proxies to local operator agent")`
  - Wired at `main.go:296`:
    `if strings.TrimSpace(*opModeAgentURLFlag) != "" { logInfo("-opmode-agent-url is deprecated and ignored; browser must call local operator agent directly") }`
  - External references: `docs/deployment-secrets.md:52` includes it
    in a sample systemd unit. Not zero-caller; the flag exists only to
    log a deprecation. Recommendation: remove the flag and the
    systemd-unit mention in `docs/deployment-secrets.md`.
- `main.go:261` — `flag.Int("opmode-agent-timeout-ms", 1500, "Deprecated and ignored: backend never proxies to local operator agent")`
  - Wired at `main.go:299`:
    `if *opModeAgentTimeoutMsFlag != 1500 { logInfo("-opmode-agent-timeout-ms is deprecated and ignored; backend no longer calls operator agent") }`
  - External references: zero. Recommendation: drop the flag and the
    log line.

---

## 2. Candidates for unexport (Go)

These symbols are exported in their files but have no external callers
in the in-tree code. They are still called from inside the same file
(so a deletion is unsafe), but a follow-up can rename them to be
package-private and remove them from the public surface.

- `dxcluster.go:222` — `func sendDXClusterCommand(conn net.Conn, command string, verbose bool)`
  - Grep: `grep -rn '\bsendDXClusterCommand\b' --include='*.go' .`
    → only the two call sites at `dxcluster.go:114-115` and the
    declaration at `dxcluster.go:222`. No external callers.
  - Reason: same-file helper; safe to unexport.
- `dx_postgres.go:264` — `func (s *dxPostgresStore) mergePendingBack(...)`
  - Grep: `grep -rn '\bmergePendingBack\b' --include='*.go' .`
    → four call sites inside `dx_postgres.go` (`flushPending` uses it
    at lines 201, 246, 251, 257, plus a comment reference at line 865)
    and the declaration at line 264. No external callers.
  - Reason: same-file helper for the `flushPending` retry path.
- `dx_postgres.go:179` — `func (s *dxPostgresStore) flushPending(ctx context.Context) error`
  - Grep: `grep -rn '\bflushPending\b' --include='*.go' .`
    → one external call (`flushPendingWithTimeout` at line 171) plus
    the declaration. No other callers.
  - Reason: the public `flushPendingWithTimeout` is the only consumer
    of the ctx-aware `flushPending`; safe to make private.
- `wspr.go:95` — `func bandFromWSPR(band int) string`
  - Grep: `grep -rn '\bbandFromWSPR\b' --include='*.go' .`
    → the call site at `wspr.go:217`, the test at `wspr_test.go:29`,
    and the declaration at `wspr.go:95`. No external callers.
  - Reason: package-private helper; the test is in the same package
    (`package main`).
- `push.go:223` — `func defaultPushSendFunc(s *pushSubscriptionStore) pushSendFunc`
  - Grep: `grep -rn '\bdefaultPushSendFunc\b' --include='*.go' .`
    → the assignment at `push.go:215` and the declaration at
    `push.go:223`. No external callers.
  - Reason: same-file indirection (captured into `s.sendFunc`). Safe
    to inline at the call site.

---

## 3. Refactor candidates (Go)

- `dxcluster.go:358` — `func bandFromFrequencyKHz(freq float64) string`
  - Grep: `grep -rn '\bbandFromFrequencyKHz\b' --include='*.go' .`
    → `dxcluster.go:265` (call site in `handleDXClusterSpot`),
    `dxcluster_test.go:33` (test), `rbn.go:258` (call site in
    `handleRBNSpot`), and the declaration at `dxcluster.go:358`.
  - Status: not dead; TWO callers across TWO files (`dxcluster.go`
    and `rbn.go`).
  - Recommendation: refactor — move the function to a shared file
    (e.g. `spot.go` or a new `bands.go`) so it does not live in the
    DX-cluster module while also being used by the RBN module.

---

## 4. Confirmed dead (frontend)

- `static/constants.js` — 20 lines.
  - Grep: `grep -rn 'CONSTANTS' static/`
    → only `static/constants.js:1` (the export). No importers
    reference the `CONSTANTS` symbol anywhere else in `static/`
    (the only other matches are inside the `coverage/` artifacts,
    which are not part of the source tree).
  - Grep: `grep -rn "from ['\"]\./constants" static/`
    → zero matches. No `import` clause pulls the file in.
  - Grep: `grep -n "constants\.js" static/index.html`
    → no `<script>` tag references it (only Bootstrap / Leaflet /
    Turf / `app.js` / `dxcluster.js` / `dist/horst-ui.js` are
    listed at lines 368-373).
  - The values it exports (`DEFAULT_MAP_CENTER`, `DEFAULT_MAP_ZOOM`,
    `RENDER_THROTTLE_MS`, etc.) are duplicated as locally-scoped
    `const`s in each consuming module (e.g. `app.js`, `band-lab.js`,
    `prop-matrix.js`, `azimuth-runtime.js`).
  - Recommendation: delete the file in a follow-up.

---

## 5. No findings (verified clean)

These passes ran the verification grep and concluded that nothing
in the named surface is dead.

- **Pass 6 — `static/utils.js` exports.** Every exported symbol in
  `static/utils.js` has at least one importer in `static/`.
  `grep -rn "from ['\"]\./utils\.js['\"]" static/`
  returns twelve importers (`app.js:13`, `azimuth-runtime.js:1`,
  `band-lab.js:2`, `dxcluster.js:10`, `horst-kevin.js:16`,
  `hot-band-indicator.js:1`, `map.js:8`, `opmode.js:2`,
  `prop-matrix.js:1`, `push.js:25`, `renderers.js:3`, `ui.js:3`,
  `utils.test.js:31`). The symbol-level cross-check (every export
  in `utils.js` used by at least one importer) confirms zero
  orphans.
- **Pass 7 — `localStorage` keys.** Three keys were checked:
  `wsprSpotsVisible` (`app.js:1557, 1564, 1585, 1592`), `rbnSpotsVisible`
  (same lines), and the `enable-${band}` family (`app.js:582, 1635`;
  read at `config.js:45`). All three are read AND written, so none
  are dead writes.
- **Pass 8 — `index.html` asset references.**
  `static/dl9et_2_140.jpg` is referenced from `static/sw.js:62, 63`
  (Service Worker notification `icon` and `badge`); used.
  `static/hk.jpg` is referenced from `index.html:11, 321`,
  `horst-kevin.js:271`, and `style.css:214`; used.
- **Pass 9 — `static/` files with no importer.** After Pass 5 + 8,
  only `static/constants.js` is orphaned. All other `static/*.js`
  files have at least one ES-module importer or one
  `<script src="…">` reference in `index.html` (lines 368-373).
- **Pass 10 — In-tree `static/` API calls vs registered routes.**
  In-tree frontend calls (deduped, sorted):
  `/api/capture_snapshot`, `/api/dx_conditions`, `/api/dxspots`,
  `/api/hot_bands`, `/api/prop_intel`, `/api/push/subscribe`,
  `/api/push/subscription-status`, `/api/push/unsubscribe`,
  `/api/push/vapid-public-key`, `/api/square_details`, `/api/stats`,
  `/api/stream` (via `EventSource` at `app.js:1902`). Every
  registered route in `main.go:511-528` is consumed by the
  in-tree frontend, with the following intentional exception:
  `/api/opmode/status` was incorrectly justified here as consumed
  by the operator agent — it had zero consumers and was removed
  2026-09-08 (opmode.go deleted).

---

## 6. Out of scope (intentional)

- Web Push push-store methods (`pushStore.add`, `pushStore.remove`,
  `pushStore.has`, `pushStore.NotifySurges`, `pushStore.size`,
  `pushStore.isEnabled`, `pushStore.publicKey`, `pushStore.configure`).
  These are exercised from `push_test.go`, `server.go`, `prop_intel.go`,
  and `main.go` (greps under `pushStore.` confirm), so they are not
  dead. Listed here so a future re-audit doesn't re-flag them.
- `static/sw.js` (Service Worker). Registered exactly once, by
  `static/push.js:116` (`navigator.serviceWorker.register(SW_PATH, …)`).
  Used as the runtime that displays Web Push notifications for the
  push flow. Not dead; observation only — the push flow is the only
  reason the SW is registered.
- `cmd/horstoperator-agent` and `cmd/horstprop` subtrees. Exported
  functions tested from `main_test.go` are covered by the same grep
  rules used for the root package. A separate pass can audit the
  rest of those subtrees; the user's "Inventory only — no deletions"
  choice limited the scope of this plan to the in-tree root binary
  + frontend.

---

No deletions in this plan. Future cleanup commits may act on this
inventory; each removal must re-run the relevant grep on the
then-current tree to confirm no caller was added between this commit
and the removal.
