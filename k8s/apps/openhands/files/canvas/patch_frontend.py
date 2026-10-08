"""Make the frontend's built-in Claude Code command name the adapter version
sandboxes actually run.

The agent profile form recognises the Claude Code preset only by its command
text being exactly the one built into the bundle, and that text pins the
adapter version the frontend was built against. Copies the frontend, rewrites
the version, and prints the directory to serve.

Assets are served as immutable for a year, so a rewritten file under its old
name would never reach a browser that has the original. Every asset is
renamed with a tag for the version, and every reference to one rewritten.
"""

import re
import shutil
import sys
from pathlib import Path

PINNED = re.compile(r"(@agentclientprotocol/claude-agent-acp@)[0-9][0-9A-Za-z.+-]*")
TEXT = {".js", ".mjs", ".css", ".html", ".json", ".webmanifest", ".map"}


def patch(source: Path, target: Path, version: str) -> int:
    """Returns how many pinned versions were rewritten; none means the bundle
    no longer has the command in this form, and `target` is not created."""
    texts = {
        p: p.read_text(encoding="utf-8")
        for p in source.rglob("*")
        if p.is_file() and p.suffix in TEXT
    }
    found = sum(len(PINNED.findall(t)) for t in texts.values())
    if not found:
        return 0

    tag = "acp" + re.sub(r"[^0-9A-Za-z]", "-", version)
    renamed = {
        p.name: f"{p.stem}.{tag}{p.suffix}"
        for p in (source / "assets").iterdir()
        if p.is_file()
    }
    # Longest first, so a name that ends another is not matched inside it.
    names = re.compile(
        "|".join(re.escape(n) for n in sorted(renamed, key=len, reverse=True))
    )

    shutil.rmtree(target, ignore_errors=True)
    for path in source.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(source)
        out = target / relative.with_name(
            renamed.get(path.name, path.name)
            if relative.parts[0] == "assets"
            else path.name
        )
        out.parent.mkdir(parents=True, exist_ok=True)
        if path in texts:
            text = PINNED.sub(rf"\g<1>{version}", texts[path])
            out.write_text(names.sub(lambda m: renamed[m[0]], text), encoding="utf-8")
        else:
            shutil.copyfile(path, out)
    return found


if __name__ == "__main__":
    source, target, version = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
    if patch(source, target, version):
        print(target)
    else:
        print(
            "no pinned claude-agent-acp version in the frontend; serving it unchanged",
            file=sys.stderr,
        )
        print(source)
