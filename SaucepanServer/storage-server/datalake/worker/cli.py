"""CLI: python -m worker.cli once|loop"""

from __future__ import annotations

import argparse
import json
import logging
import sys

from worker.orchestrator import main_loop, run_once


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Saucepan pull bridge worker")
    parser.add_argument("command", choices=("once", "loop"), help="Run one poll or forever")
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    if args.command == "once":
        print(json.dumps(run_once(), indent=2, default=str))
        return 0
    main_loop()
    return 0


if __name__ == "__main__":
    sys.exit(main())
