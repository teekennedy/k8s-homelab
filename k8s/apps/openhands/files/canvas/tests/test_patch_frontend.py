from patch_frontend import patch

COMMAND = "npx -y --prefer-offline @agentclientprotocol/claude-agent-acp@0.63.0"


def _frontend(root, vendor: str):
    assets = root / "assets"
    assets.mkdir(parents=True)
    (root / "index.html").write_text('<script src="/canvas/assets/entry-AAA.js">')
    (assets / "entry-AAA.js").write_text('import"./vendor~entry-AAA.js";')
    (assets / "vendor~entry-AAA.js").write_text(vendor)
    (assets / "logo.png").write_bytes(b"\x89PNG")
    (root / "locales").mkdir()
    (root / "locales" / "en.json").write_text('{"k": "entry-AAA.js stays"}')
    return root


def test_the_version_is_rewritten_and_every_asset_renamed(tmp_path):
    source = _frontend(tmp_path / "src", f"default_command:`{COMMAND}`")
    target = tmp_path / "out"
    assert patch(source, target, "0.87.0") == 1

    assets = sorted(p.name for p in (target / "assets").iterdir())
    assert assets == [
        "entry-AAA.acp0-87-0.js",
        "logo.acp0-87-0.png",
        "vendor~entry-AAA.acp0-87-0.js",
    ]
    vendor = (target / "assets" / "vendor~entry-AAA.acp0-87-0.js").read_text()
    assert vendor == "default_command:`" + COMMAND.replace("0.63.0", "0.87.0") + "`"
    # A name that ends a longer one is rewritten as the longer one.
    entry = (target / "assets" / "entry-AAA.acp0-87-0.js").read_text()
    assert entry == 'import"./vendor~entry-AAA.acp0-87-0.js";'
    assert (
        "/canvas/assets/entry-AAA.acp0-87-0.js" in (target / "index.html").read_text()
    )
    assert (target / "assets" / "logo.acp0-87-0.png").read_bytes() == b"\x89PNG"
    # Outside assets/ nothing is renamed, but references still follow.
    assert (target / "locales" / "en.json").exists()


def test_a_bundle_without_the_pinned_command_is_left_alone(tmp_path):
    source = _frontend(tmp_path / "src", "nothing pinned here")
    target = tmp_path / "out"
    assert patch(source, target, "0.87.0") == 0
    assert not target.exists()
