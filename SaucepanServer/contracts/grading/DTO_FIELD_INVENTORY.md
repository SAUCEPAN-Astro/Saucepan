# DTO field inventory — Go / Rust / Python

Inventory of cross-language DTO drift for task handoff and grade ingest.
**Not** a full schema generator — minimum handoff fields + known mismatches.
Generated contracts and a client spine are intentionally out of scope for this reference implementation.

Sources:

| Language | Path |
|----------|------|
| Go (apiserver) | `SaucepanServer/task-server/cmd/apiserver/models.go`, `grades.go` |
| Historical Rust pier client | Outside this repository; the current Go pier tools are under `SaucepanServer/task-server/cmd/` |
| Python (SDK) | `SaucepanSDK/python/saucepan/models.py`, `campaigns.py` |

## Task identity

| Field | Go | Rust | Python SDK | Notes |
|-------|----|------|------------|-------|
| Task public id | `Task.PublicID` → JSON `"id"` (**string**) | `Task.id: i64` | `Task.id: int` | **Drift:** Go wire id is string; Rust/SDK treat numeric. The underlying database may still use an internal numeric ID. |
| Campaign id | `Task.CampaignID` **string** | `Task.campaign_id: Option<i64>` | Campaign pack UUID string via campaigns API | **Drift:** string UUID vs i64 |
| Telescope id | string `telescope_id` | string `telescope_id` | n/a on Task | Aligned |
| Assigned telescope | `*string` | `Option<String>` | n/a | Aligned when present |

## Task requirements (handoff / matching)

| Field (wire / intent) | Go `Task` | Rust `Task` | Python `TaskSpec` | Notes |
|----------------------|-----------|-------------|-------------------|-------|
| `name` | yes | yes | yes | OK |
| `integration_time` | yes | yes | yes | OK |
| `min_power` | yes | yes | yes | OK |
| `required_filters` | yes | yes | `filters` → `required_filters` in `to_dict` | OK |
| `priority` | yes | yes | yes | OK |
| `target_ra` / `target_dec` | yes | `Option` | absent on TaskSpec | SDK create path incomplete |
| `max_psf_fwhm_arcsec` | yes | absent | `max_psf_fwhm` → wire `max_psf_fwhm` (**not** `_arcsec`) | **Drift:** JSON key name |
| `min_aperture_mm` | yes | Option on capabilities side | yes | Partial |
| `normalized_integration_budget_s` | yes | absent | yes | Rust lag |
| `allow_emulator` | yes | absent | absent | Server-only flag |
| `science_band` / FOV / plate-scale bounds | yes | mostly absent | `max_plate_scale` only | Drift |

## Grade ingest (`POST /api/v1/grades/ingest`)

Consumed by Go apiserver; produced by compute worker (Python). Not modeled in Rust/SDK today.

| Field | Required | Type | Notes |
|-------|----------|------|-------|
| `telescope_id` | yes | string | External node id |
| `upload_id` | yes* | string | Idempotency companion |
| `idempotency_key` | yes* | string | Dedup |
| `headline` | yes | int 0–100 | Cheap weighted score |
| `dimensions` | yes | object | `{image_quality,task_fidelity,timeliness}.score` |
| `sp_exptime` | preferred | float | Falls back from fidelity metadata |
| `grader_version` | optional | string | |
| `task_id` | optional | int/string | DB PK when present |
| `object_key` / landing path | optional | string | R2 key validation |
| `sp_emulator` | optional | bool | Emulator policy |

Parity for **points / reputation math** on ingest is gated by [`points_vectors.json`](points_vectors.json) + [`reputation_vectors.json`](reputation_vectors.json), not by this matrix.

## Compatibility stance (until codegen)

1. Prefer **string campaign UUIDs** and document task public id as opaque string on the wire.
2. Do not add a third hand-maintained grade-points formula — use Python SSOT + Go mirror + vectors.
3. When changing a handoff field, update this inventory in the same PR and add a fixture under `contracts/` if the field is load-bearing.

## Explicit compatibility check (minimum)

Golden grade-ingest shape used by Go tests (`sampleGradePayload`) — keep aligned with worker emit:

```json
{
  "upload_id": "string",
  "telescope_id": "string",
  "idempotency_key": "string",
  "headline": 75,
  "sp_exptime": 30.0,
  "grader_version": "string",
  "dimensions": {
    "image_quality": {"score": 0.0},
    "task_fidelity": {"score": 0.0},
    "timeliness": {"score": 0.0}
  }
}
```

See also [`grade_ingest_min.json`](grade_ingest_min.json).
