# Grading contracts — single source of truth

**Python owns formulas.** Go is an ingest-only mirror. Shared golden vectors make drift CI-fatal.

| Role | Location |
|------|----------|
| Formula SSOT | `SaucepanServer/compute-server/grading/` (`points.py`, `dimensions.py`, `constants.py`) |
| Ingest mirror | `SaucepanServer/task-server/shared/grading/` (must match Python; do not invent new rules here) |
| Parity gate | JSON vectors in **this directory** — loaded by both pytest and `go test` |

## Vectors

| File | Covers |
|------|--------|
| [`constants.json`](constants.json) | Numeric constants both languages must share |
| [`points_vectors.json`](points_vectors.json) | `compute_frame_points` / `ComputeFramePoints` |
| [`reputation_vectors.json`](reputation_vectors.json) | EMA + `build_reputation_partial` (excludes wall-clock `last_ingested_at`) |
| [`stack_vectors.json`](stack_vectors.json) | Stack eligibility threshold |
| [`headline_vectors.json`](headline_vectors.json) | Weighted headline 0–100 |
| [`dimensions_vectors.json`](dimensions_vectors.json) | Mirrored image-quality, task-fidelity, and timeliness details |

Override search path with env `SP_GRADING_VECTORS` (directory containing these JSON files).

## How to update formulas

1. Change Python under `compute-server/grading/`.
2. Port the same change to the Go mirror.
3. Regenerate or hand-edit expected values in the JSON vectors (prefer regenerating via a small Python script that calls the SSOT).
4. Run both:
   - `.venv/bin/python3 -m pytest SaucepanServer/compute-server/grading/tests/ -q`
   - `go test ./shared/grading/...` from `SaucepanServer/task-server`
5. CI runs both on every PR (`compute` + `task-server` jobs in `.github/workflows/ci.yml`).

## Intentional divergences

- `GRADER_VERSION`: Python `1.0.0` vs Go `1.0.0-go` (identity only; not vector-checked).
- Python `compute_frame_points(..., base_points=)` override — Go always uses `BasePoints` (ingest path never overrides).
- Go rounding uses banker's half-to-even (with a tiny tie epsilon) to match Python 3 `round()` for ingest parity.

## Rounding

Python 3 `round` / `round(x, n)` is **half-to-even**. The Go mirror must use the same (`roundHalfEven` / `roundN` in `shared/grading`), not `math.Round` (half-away-from-zero). Headline weights are applied in fixed key order (`image_quality`, `task_fidelity`, `timeliness`) so float accumulation matches Python dict order.

## DTO field drift

See [`DTO_FIELD_INVENTORY.md`](DTO_FIELD_INVENTORY.md). Full OpenAPI / code generation is out of scope for this reference implementation.

## Related

- Architecture: [`docs/design/README.md`](../../../docs/design/README.md)
- The grading parity contract is kept here as a public design and test boundary.
