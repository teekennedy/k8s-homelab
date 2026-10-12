"""Exercise release versioning with real git-cliff and a local Forgejo stub."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
RESOURCES = Path("k8s/platform/forgejo/files/forgejo-resources")


@unittest.skipUnless(shutil.which("git-cliff"), "git-cliff is required")
class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        (self.repo / ".dagger").mkdir()
        (self.repo / RESOURCES).mkdir(parents=True)
        shutil.copy(ROOT / ".dagger/releases.json", self.repo / ".dagger")
        shutil.copy(ROOT / "cliff.toml", self.repo)
        shutil.copy(ROOT / RESOURCES / "cliff.toml", self.repo / RESOURCES)
        (self.repo / "devenv.nix").write_text("{}\n")
        (self.repo / RESOURCES / "main.go").write_text("package main\n")
        self.git("init", "-q")
        self.git("config", "user.name", "Release test")
        self.git("config", "user.email", "test@example.com")
        self.commit("chore: baseline")
        self.git("tag", "devenv-1.1.1")
        self.bin = self.repo / "bin"
        self.bin.mkdir()
        curl = self.bin / "curl"
        curl.write_text(
            "#!/usr/bin/env python3\n"
            "import json, subprocess, sys\n"
            "p = json.load(sys.stdin)\n"
            "with open('releases.jsonl', 'a') as f: f.write(json.dumps(p) + '\\n')\n"
            "subprocess.run(['git', 'tag', p['tag_name'], p['target_commitish']], check=True)\n"
            "print('201', end='')\n"
        )
        curl.chmod(0o755)

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True).strip()

    def commit(self, message):
        self.git("add", "devenv.nix", str(RESOURCES))
        self.git("commit", "-qm", message)

    def release(self):
        env = dict(
            os.environ,
            PATH=f"{self.bin}:{os.environ['PATH']}",
            CI_COMMIT_SHA=self.git("rev-parse", "HEAD"),
            CI_FORGE_URL="https://forgejo.invalid",
            CI_REPO="test/repo",
            FORGEJO_TOKEN="test",
        )
        subprocess.run(
            [str(ROOT / "scripts/release.sh")], cwd=self.repo, env=env, check=True
        )
        return [
            json.loads(line)
            for line in (self.repo / "releases.jsonl").read_text().splitlines()
        ]

    def test_migration_and_independent_versions(self):
        (self.repo / "devenv.nix").write_text("{ changed = true; }\n")
        self.commit("feat(devenv): add tooling")
        releases = self.release()
        self.assertEqual(
            [r["tag_name"] for r in releases],
            ["devenv-ci-1.2.0", "devenv-shell-1.2.0", "forgejo-resources-1.0.0"],
        )
        self.assertEqual(releases[0]["body"], releases[1]["body"])
        self.assertEqual(len(self.release()), 3)  # idempotent retry
        (self.repo / RESOURCES / "main.go").write_text("package main\n// fix\n")
        self.commit("fix(forgejo-resources): fix reconciliation")
        releases = self.release()
        self.assertEqual(releases[-1]["tag_name"], "forgejo-resources-1.0.1")
        self.assertEqual(len(releases), 4)
        (self.repo / "devenv.nix").write_text("{ refreshed = true; }\n")
        self.commit("chore(deps): refresh devenv")
        releases = self.release()
        self.assertEqual(
            [r["tag_name"] for r in releases[-2:]],
            ["devenv-ci-1.2.1", "devenv-shell-1.2.1"],
        )
        self.assertNotIn("Add tooling", releases[-1]["body"])

    def test_retry_completes_artifact_pair(self):
        (self.repo / "devenv.nix").write_text("{ fixed = true; }\n")
        self.commit("fix(devenv): repair tooling")
        self.git("tag", "devenv-ci-1.1.2")
        releases = self.release()
        self.assertEqual(releases[0]["tag_name"], "devenv-shell-1.1.2")
        self.assertIn("Repair tooling", releases[0]["body"])


if __name__ == "__main__":
    unittest.main()
