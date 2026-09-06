"""CLI for subprocess sidecar mode and session rollups."""

from __future__ import annotations

import argparse
import json
import os
import sys


def main() -> None:
    parser = argparse.ArgumentParser(description="Saucepan metrics sidecar")
    sub = parser.add_subparsers(dest="cmd", required=True)

    emit = sub.add_parser(
        "emit-upload",
        help="Emit metrics for upload_id (datalake hook or METRICS_CONTEXT_JSON)",
    )
    emit.add_argument("upload_id")
    emit.add_argument(
        "--context-json",
        help="Path to EntityContext JSON (subprocess mode). "
        "Also accepts METRICS_CONTEXT_JSON env or stdin when '-' ",
    )

    rollup = sub.add_parser(
        "rollup-session",
        help="Roll up L2 session metrics for telescope_id + night_id",
    )
    rollup.add_argument("--telescope-id", required=True)
    rollup.add_argument("--night-id", required=True)
    rollup.add_argument(
        "--frames-json",
        help="Optional JSON file: list of frame metric dicts for aggregation",
    )
    rollup.add_argument(
        "--from-store",
        action="store_true",
        help="Load L1 frames from datalake frame_catalog / metric_observations",
    )

    sub.add_parser("registry-stats", help="Print live/wait counts")

    args = parser.parse_args()
    if args.cmd == "registry-stats":
        from metrics.registry import load_registry

        reg = load_registry()
        live = sum(1 for s in reg.values() if s.status == "live")
        wait = sum(1 for s in reg.values() if s.status == "wait")
        print(json.dumps({"live": live, "wait": wait, "total": len(reg)}))
        return

    if args.cmd == "rollup-session":
        from metrics.observation import build_observation
        from metrics.producers.session_rollup import produce
        from metrics.projectors.session import rollup_night
        from metrics.registry import wait_metrics
        from metrics.slo import check_slos

        frames: list[dict] = []
        if args.frames_json:
            with open(args.frames_json, encoding="utf-8") as fh:
                loaded = json.load(fh)
            if not isinstance(loaded, list):
                print(json.dumps({"error": "frames-json must be a JSON array"}), file=sys.stderr)
                sys.exit(1)
            frames = loaded
        elif args.from_store or not args.frames_json:
            try:
                from metrics_store import list_l1_frames_for_night

                frames = list_l1_frames_for_night(args.telescope_id, args.night_id)
            except ImportError:
                # Running outside datalake — empty frames unless --frames-json given
                if args.from_store:
                    print(
                        json.dumps({"error": "metrics_store not importable; pass --frames-json"}),
                        file=sys.stderr,
                    )
                    sys.exit(1)

        rollup_data = rollup_night(args.telescope_id, args.night_id, frames=frames)
        ctx = {
            "telescope_id": args.telescope_id,
            "night_id": args.night_id,
            "_session_rollup": rollup_data,
        }
        metrics = produce(ctx)
        obs = build_observation(
            producer="session_rollup",
            entity_type="session",
            entity_id=args.night_id,
            context=ctx,
            metrics=metrics,
            wait_pile=[s.id for s in wait_metrics()],
        )
        result = {
            "observation": obs,
            "slo_events": check_slos(metrics),
        }
        print(json.dumps(result, default=str))
        return

    if args.cmd == "emit-upload":
        ctx = _load_emit_context(args.upload_id, args.context_json)
        if ctx is None:
            print(
                json.dumps(
                    {
                        "error": "emit-upload needs context: --context-json, "
                        "METRICS_CONTEXT_JSON, stdin '-', or datalake metrics_hook"
                    }
                ),
                file=sys.stderr,
            )
            sys.exit(1)

        from metrics.sidecar import _run_dispatch

        def _save(obs: dict) -> None:
            try:
                from metrics_store import save_metric_observation

                save_metric_observation(obs)
            except ImportError:
                from metrics.privacy import sanitize_observation

                safe_obs = sanitize_observation(obs)
                print(json.dumps(safe_obs, default=str))

        _run_dispatch(ctx, save_fn=_save)
        print(json.dumps({"ok": True, "upload_id": args.upload_id}))
        return


def _load_emit_context(upload_id: str, context_json: str | None) -> dict | None:
    """Resolve EntityContext for subprocess emit-upload (#44)."""
    raw: str | None = None
    if context_json == "-":
        raw = sys.stdin.read()
    elif context_json:
        with open(context_json, encoding="utf-8") as fh:
            raw = fh.read()
        _remove_temporary_context_file(context_json)
    elif os.environ.get("METRICS_CONTEXT_JSON"):
        path = os.environ["METRICS_CONTEXT_JSON"]
        if path == "-":
            raw = sys.stdin.read()
        elif os.path.isfile(path):
            with open(path, encoding="utf-8") as fh:
                raw = fh.read()
            if os.environ.get("METRICS_CONTEXT_TEMP_FILE") == "1":
                _remove_temporary_context_file(path)
        else:
            raw = path  # inline JSON

    if raw:
        data = json.loads(raw)
        if not isinstance(data, dict):
            return None
        data.setdefault("upload_id", upload_id)
        return data

    # Prefer datalake hook rebuild when available
    try:
        from metrics_hook import _build_context

        return _build_context(upload_id)
    except ImportError:
        return None


def _remove_temporary_context_file(path: str) -> None:
    """Remove a subprocess context file after its contents are loaded."""
    if os.environ.get("METRICS_CONTEXT_TEMP_FILE") != "1":
        return
    try:
        os.unlink(path)
    except OSError:
        pass


if __name__ == "__main__":
    main()
