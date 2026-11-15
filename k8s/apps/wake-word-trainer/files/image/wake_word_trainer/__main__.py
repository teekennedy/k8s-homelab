"""wake-word-trainer serve | fetch-model | train | publish | seed"""

import argparse
import logging
import os
import sys
from pathlib import Path


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="wake-word-trainer")
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("serve", help="the collector: capture API, processor, review UI")
    s.add_argument("--host", default="0.0.0.0")
    s.add_argument("--port", type=int, default=8080)
    f = sub.add_parser(
        "fetch-model", help="download the Whisper model if it is missing"
    )
    f.add_argument("--url", required=True)
    f.add_argument("--dest", type=Path, required=True)
    sub.add_parser("train", help="run the gate and, if it passes, a training run")
    p = sub.add_parser("publish", help="publish the model the trainer promoted")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument(
        "--live-dir",
        type=Path,
        default=None,
        help="with --dry-run: the real folder, copied into the scratch target first",
    )
    sd = sub.add_parser(
        "seed", help="download datasets and build the static features (steps 1-3)"
    )
    sd.add_argument("--segment-python", default=sys.executable)
    sd.add_argument("--whisper-url", default=os.environ.get("WWT_WHISPER_URL", ""))
    args = ap.parse_args(argv)

    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
    )
    if args.cmd == "serve":
        import uvicorn

        from .app import create_app

        uvicorn.run(
            create_app(),
            host=args.host,
            port=args.port,
            proxy_headers=False,
            access_log=False,
        )
        return 0

    from . import trainer

    if args.cmd == "fetch-model":
        trainer.fetch(args.url, args.dest)
        return 0
    cfg = trainer.TrainerConfig()
    if args.cmd == "train":
        return trainer.train(cfg)
    if args.cmd == "publish":
        return trainer.publish(cfg, args.dry_run, args.live_dir)
    if args.cmd == "seed":
        return trainer.seed(cfg, args.segment_python, args.whisper_url)
    return 2


if __name__ == "__main__":
    sys.exit(main())
