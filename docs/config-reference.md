| Key / env | Default | Notes |
|---|---|---|
| `ARGUS_HTTP_ADDR` | `:8080` | single listener for ingest + API + UI |
| `ARGUS_DATABASE_URL` | — | required |
| `ARGUS_DB_MAX_CONNS` | `10` | pgxpool |
| `ARGUS_AUTO_MIGRATE` | `true` | run migrations at serve start (advisory-locked) |
| `ARGUS_SHUTDOWN_GRACE` | `15s` | graceful-shutdown budget (§3.8) |
| `ARGUS_INGEST_QUEUE` | `1024` | batches |
| `ARGUS_INGEST_WORKERS` | `4` |  |
| `ARGUS_INGEST_BATCH_SIZE` | `500` | events |
| `ARGUS_INGEST_FLUSH` | `250ms` |  |
| `ARGUS_INGEST_MAX_BODY_BYTES` | `8388608` | decompressed |
| `ARGUS_INGEST_WRITE_TIMEOUT` | `30s` | per-batch write budget (M6) |
| `ARGUS_INGEST_RETRY_CONFLICT` | `8` | deadlock/serialization attempts |
| `ARGUS_INGEST_RETRY_TRANSIENT` | `3` | connection-error attempts |
| `ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY` | `false` | §1.5.2 |
| `ARGUS_INGEST_TOKEN` | empty | ingest auth seam |
| `ARGUS_API_TOKEN` | empty | read-API auth seam |
| `ARGUS_RETENTION_RAW_DAYS` | `90` | DECISIONS.md; also the clock-clamp lower bound (§1.2) |
| `ARGUS_RETENTION_SESSION_DAYS` | `0` | 0 = never |
| `ARGUS_RETENTION_HOUR` | `4` | local hour for the daily job |
| `ARGUS_ATTRS_RETENTION_DAYS` | `0` | 0 = keep attrs for the full raw retention (OQ-5) |
| `ARGUS_DEDUP_WINDOW` | `7d` | ingest_dedup retention = the exact-dedup guarantee |
| `ARGUS_ROLLUP_INTERVAL` | `60s` |  |
| `ARGUS_ROLLUP_MAX_BUCKETS` | `200` | per run |
| `ARGUS_ROLLUP_SESSION_REMARK_MAX` | `720` | cap on buckets re-dirtied by a late project change |
| `ARGUS_SWEEP_INTERVAL` | `60s` | abandoned-session sweep |
| `ARGUS_SESSION_IDLE_TIMEOUT` | `15m` | active→abandoned boundary |
| `ARGUS_STREAM_BUFFER` | `256` | per-subscriber channel |
| `ARGUS_STREAM_HEARTBEAT` | `15s` |  |
| `ARGUS_STREAM_REPLAY_WINDOW` | `5m` | SSE replay bound (also bounds the ts predicate) |
| `ARGUS_STREAM_REPLAY_MAX` | `2000` | events per reconnect |
| `ARGUS_STREAM_MAX_SUBSCRIBERS` | `100` | 503 beyond it |
| `ARGUS_LOG_LEVEL` | `info` | slog |
| `ARGUS_LOG_FORMAT` | `json` | tint handler for text |
| `ARGUS_CORS_ORIGINS` | empty | needed only for pnpm dev on :5173 |
| `ARGUS_UI_ENABLED` | `true` | serve the embedded SPA |
