-- pathscope.sql — Pathscope-owned schema. Read paths come from the
-- existing dxdata tables (dx_raw_spots, proplab_cell_buckets,
-- proplab_sw_series). The tables here are Pathscope's own state: operator
-- profile, score log, alert log, and source weights that influence
-- scoring. They are NOT read by the main horstreporter binary.
--
-- Apply manually before the first deploy:
--   psql "$DX_POSTGRES_DSN" -f schema/pathscope.sql
-- Future versions will fold this into the apply step inside the Go
-- binary (cp. dx_proplab_store.go::proplabSchemaStmts).

-- Operator profile: persists the operator's QTH, name, and view knobs.
-- Exactly one row is expected; the primary key is a constant.
CREATE TABLE IF NOT EXISTS pathscope_operator_profile (
    id                  SMALLINT PRIMARY KEY DEFAULT 1,
    qth                 TEXT      NOT NULL DEFAULT 'JO62qm',
    operator_callsign   TEXT      NOT NULL DEFAULT '',
    default_bands       TEXT[]    NOT NULL DEFAULT ARRAY['80M','60M','40M','30M','20M','17M','15M','12M','10M','6M'],
    lookback_days       INT       NOT NULL DEFAULT 30,
    baseline_window     TEXT      NOT NULL DEFAULT '30d',
    surge_threshold     FLOAT8    NOT NULL DEFAULT 2.0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT singleton CHECK (id = 1)
);

-- Score log: every cell scored every minute. Used to chart the
-- per-cell score over time and to debug the scoring engine. Append-only.
CREATE TABLE IF NOT EXISTS pathscope_score_log (
    observed_at     TIMESTAMPTZ NOT NULL,
    band            TEXT        NOT NULL,
    region          TEXT        NOT NULL,
    score           FLOAT8      NOT NULL,
    probability     FLOAT8      NOT NULL,
    confidence      FLOAT8      NOT NULL,
    solar_modifier  FLOAT8      NOT NULL,
    baseline_n      INT         NOT NULL,
    PRIMARY KEY (observed_at, band, region)
);
CREATE INDEX IF NOT EXISTS idx_pathscope_score_band_obs
    ON pathscope_score_log (band, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_pathscope_score_region_obs
    ON pathscope_score_log (region, observed_at DESC);

-- Alert log: every "surge" or "drop" event the user has been notified
-- about. Used to avoid spamming the same surge, and to evaluate the
-- quality of the alert logic over time.
CREATE TABLE IF NOT EXISTS pathscope_alert_log (
    id              BIGSERIAL   PRIMARY KEY,
    observed_at     TIMESTAMPTZ NOT NULL,
    band            TEXT        NOT NULL,
    region          TEXT        NOT NULL,
    kind            TEXT        NOT NULL, -- 'surge', 'drop', 'rare_signal'
    score           FLOAT8      NOT NULL,
    probability     FLOAT8      NOT NULL,
    delivered_at    TIMESTAMPTZ,
    response        TEXT        NOT NULL DEFAULT '', -- 'qso', 'no_op', 'dismiss'
    meta            JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_pathscope_alert_band_obs
    ON pathscope_alert_log (band, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_pathscope_alert_kind_obs
    ON pathscope_alert_log (kind, observed_at DESC);

-- Source weights: per-source reliability weight used by the v0.5
-- bivariate scoring. In v0 the table is read-only at startup with a
-- static default; it is shaped so the v0.5 calibration study can write
-- to it without a migration.
CREATE TABLE IF NOT EXISTS pathscope_source_weights (
    source          TEXT        PRIMARY KEY, -- 'mqtt' | 'dxcluster' | 'rbn'
    weight          FLOAT8      NOT NULL,
    notes           TEXT        NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO pathscope_source_weights (source, weight, notes) VALUES
    ('mqtt',       1.0, 'PSKREPORTER — primary; trustworthy SNR scale'),
    ('dxcluster',  0.7, 'DX-cluster — sparse, replay-attack prone'),
    ('rbn',        0.4, 'RBN — high noise, different dB scale, mode=CW')
ON CONFLICT (source) DO NOTHING;

-- Predictions: future openings the engine claims will happen. Used by
-- the "upcoming openings" tab. Populated by a nightly job (v0.5).
-- Defined here so the schema ships as a single coherent unit.
CREATE TABLE IF NOT EXISTS pathscope_predictions (
    id              BIGSERIAL   PRIMARY KEY,
    predicted_for   TIMESTAMPTZ NOT NULL,
    band            TEXT        NOT NULL,
    region          TEXT        NOT NULL,
    score_hint      FLOAT8      NOT NULL,
    probability     FLOAT8      NOT NULL,
    basis           TEXT        NOT NULL, -- 'trailing_30d', 'solar_proxy', 'manual'
    meta            JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_pathscope_pred_for_band
    ON pathscope_predictions (predicted_for, band, region);

-- Resolution: outcome of a prediction once the predicted window has
-- passed. Joins to the WaveLog QSO log via callsign + timestamp. Populated
-- by the resolver service (cmd/pathscope-resolver, future).
CREATE TABLE IF NOT EXISTS pathscope_resolution (
    prediction_id   BIGINT      PRIMARY KEY REFERENCES pathscope_predictions(id),
    resolved_at     TIMESTAMPTZ NOT NULL,
    outcome         TEXT        NOT NULL, -- 'qso', 'no_qso', 'undetermined'
    qso_id          TEXT,                 -- WaveLog QSO id, when outcome=qso
    notes           TEXT        NOT NULL DEFAULT ''
);

-- Materialized view: 30-day baseline per (band, region, mode) for the
-- v0.5 path. The inline query in store_pg.go::Baseline replaces this
-- while v0 is being validated; once the per-moment-of-day numbers
-- matter, this view is the source of truth.
--
-- Refresh nightly:
--   REFRESH MATERIALIZED VIEW CONCURRENTLY pathscope_baseline_dihour;
CREATE MATERIALIZED VIEW IF NOT EXISTS pathscope_baseline_dihour AS
SELECT
    band,
    region,
    lane,
    EXTRACT(DOW  FROM TO_TIMESTAMP(bucket_start)) AS dow,
    EXTRACT(HOUR FROM TO_TIMESTAMP(bucket_start)) AS hour,
    AVG(link_count)::float8                       AS mean,
    COALESCE(STDDEV_SAMP(link_count), 0)::float8  AS stddev,
    COUNT(*)::int                                 AS samples
FROM proplab_cell_buckets
WHERE bucket_start >= EXTRACT(EPOCH FROM NOW() - INTERVAL '30 days')
GROUP BY band, region, lane, dow, hour
WITH NO DATA;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pathscope_baseline_pk
    ON pathscope_baseline_dihour (band, region, lane, dow, hour);
