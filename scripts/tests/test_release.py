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
SANDBOX = Path("k8s/apps/openhands/files/sandbox")


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        (self.repo / ".dagger/modules/homelab").mkdir(parents=True)
        (self.repo / RESOURCES).mkdir(parents=True)
        shutil.copy(
            ROOT / ".dagger/modules/homelab/releases.json",
            self.repo / ".dagger/modules/homelab",
        )
        (self.repo / "scripts").mkdir()
        shutil.copy(ROOT / "scripts/release-metadata.sh", self.repo / "scripts")
        shutil.copy(ROOT / "cliff.toml", self.repo)
        shutil.copy(ROOT / RESOURCES / "cliff.toml", self.repo / RESOURCES)
        # Left untracked: only tests that stage SANDBOX release the sandbox.
        (self.repo / SANDBOX).mkdir(parents=True)
        shutil.copy(ROOT / SANDBOX / "cliff.toml", self.repo / SANDBOX)
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
        self.git("add", "devenv.nix", str(RESOURCES), ".dagger")
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

    def metadata(self, tag):
        return json.loads(
            subprocess.check_output(
                [str(ROOT / "scripts/release-metadata.sh"), tag],
                cwd=self.repo,
                text=True,
            )
        )

    def test_migration_and_independent_versions(self):
        (self.repo / "devenv.nix").write_text("{ changed = true; }\n")
        self.commit("feat(devenv): add tooling")
        releases = self.release()
        self.assertEqual(
            [r["tag_name"] for r in releases],
            ["devenv-ci-1.2.0", "devenv-shell-1.2.0", "forgejo-resources-1.0.0"],
        )
        self.assertEqual(releases[0]["body"], releases[1]["body"])
        ci = self.metadata("devenv-ci-1.2.0")
        shell = self.metadata("devenv-shell-1.2.0")
        resources = self.metadata("forgejo-resources-1.0.0")
        self.assertEqual(ci["changelog"], releases[0]["body"])
        self.assertEqual(shell["changelog"], ci["changelog"])
        self.assertEqual(resources["changelog"], releases[2]["body"])
        self.assertEqual(ci["revision"], self.git("rev-parse", "HEAD"))
        self.assertEqual(ci["created"], self.git("show", "-s", "--format=%cI", "HEAD"))
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
        self.assertNotIn(
            "Add tooling", self.metadata("devenv-shell-1.2.1")["changelog"]
        )

    def test_metadata_rejects_wrong_checkout(self):
        self.git("tag", "devenv-ci-1.1.1")
        (self.repo / "devenv.nix").write_text("{ changed = true; }\n")
        self.commit("fix(devenv): change tooling")
        with self.assertRaises(subprocess.CalledProcessError):
            self.metadata("devenv-ci-1.1.1")

    def test_shared_metadata_changes_do_not_release_projects(self):
        self.release()
        (self.repo / ".dagger/releases.go").write_text("// Updated image labels\n")
        self.commit("feat(containers): add image labels")
        releases = self.release()
        self.assertEqual(len(releases), 1)
        self.assertEqual(releases[0]["tag_name"], "forgejo-resources-1.0.0")

    def test_metadata_for_tag_without_project_changes(self):
        self.release()
        (self.repo / ".dagger/releases.go").write_text("// Shared change\n")
        self.commit("fix(containers): adjust shared labels")
        for tag in ["devenv-ci-1.1.2", "devenv-shell-1.1.2", "forgejo-resources-1.0.1"]:
            self.git("tag", tag)
        self.assertEqual(self.metadata("forgejo-resources-1.0.1")["changelog"], "")
        self.assertEqual(self.metadata("devenv-shell-1.1.2")["changelog"], "")
        (self.repo / RESOURCES / "main.go").write_text("package main\n// repair\n")
        self.commit("fix(forgejo-resources): repair reconciliation")
        releases = self.release()
        self.assertEqual(len(releases), 2)
        self.assertEqual(releases[-1]["tag_name"], "forgejo-resources-1.0.2")
        self.assertIn(
            "Repair reconciliation",
            self.metadata("forgejo-resources-1.0.2")["changelog"],
        )
        (self.repo / "devenv.nix").write_text("{ repaired = true; }\n")
        self.commit("fix(devenv): repair tooling")
        releases = self.release()
        self.assertEqual(
            [r["tag_name"] for r in releases[-2:]],
            ["devenv-ci-1.1.3", "devenv-shell-1.1.3"],
        )

    def test_minor_and_major_after_tag_without_project_changes(self):
        for message, expected in [
            ("feat(forgejo-resources): add reconciliation", "1.1.0"),
            ("feat(forgejo-resources)!: replace reconciliation", "2.0.0"),
        ]:
            with self.subTest(message=message):
                (self.repo / ".dagger/releases.go").write_text(f"// {message}\n")
                self.commit("fix(containers): adjust shared labels")
                self.git("tag", "forgejo-resources-1.0.1")
                (self.repo / RESOURCES / "main.go").write_text(f"// {message}\n")
                self.commit(message)
                releases = self.release()
                self.assertEqual(
                    releases[-1]["tag_name"], f"forgejo-resources-{expected}"
                )
                self.assertNotIn("Baseline", releases[-1]["body"])
                self.git("tag", "-d", "forgejo-resources-1.0.1")
                self.git("tag", "-d", f"forgejo-resources-{expected}")

    def test_sandbox_starts_at_initial_tag(self):
        self.release()
        for content, message, expected in [
            ("FROM scratch\n", "chore(deps): update agent-server", "0.2.0"),
            ("FROM scratch\n# fix\n", "fix(openhands): repair sandbox", "0.2.1"),
            ("FROM scratch\n# feat\n", "feat(openhands): add a CLI", "0.3.0"),
        ]:
            with self.subTest(message=message):
                (self.repo / SANDBOX / "Dockerfile").write_text(content)
                self.git("add", str(SANDBOX))
                self.commit(message)
                releases = self.release()
                tag = f"openhands-sandbox-{expected}"
                self.assertEqual(releases[-1]["tag_name"], tag)
                self.assertEqual(self.metadata(tag)["changelog"], releases[-1]["body"])
        self.assertEqual(len(releases), 4)

    def test_retry_completes_artifact_pair(self):
        (self.repo / "devenv.nix").write_text("{ fixed = true; }\n")
        self.commit("fix(devenv): repair tooling")
        self.git("tag", "devenv-ci-1.1.2")
        releases = self.release()
        self.assertEqual(releases[0]["tag_name"], "devenv-shell-1.1.2")
        self.assertIn("Repair tooling", releases[0]["body"])


class RecordDevenvCiTests(unittest.TestCase):
    """scripts/record-devenv-ci.sh against a local bare repository."""

    RECORD = Path(".dagger/modules/homelab/devenv-ci.json")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        base = Path(self.temp.name)
        self.remote = base / "remote.git"
        self.git(base, "init", "-q", "--bare", str(self.remote))
        self.seed = base / "seed"
        self.clone(self.seed)
        (self.seed / "devenv.nix").write_text("{}\n")
        self.git(self.seed, "add", "devenv.nix")
        self.git(self.seed, "commit", "-qm", "chore: baseline")
        self.git(self.seed, "push", "-q", str(self.remote), "HEAD:main")

    def git(self, cwd, *args):
        return subprocess.check_output(["git", *args], cwd=cwd, text=True).strip()

    def clone(self, path):
        self.git(path.parent, "clone", "-q", str(self.remote), str(path))
        self.git(path, "config", "user.name", "Release test")
        self.git(path, "config", "user.email", "test@example.com")

    def record(self, tag):
        """Runs the script from a detached checkout of main with tag's record."""
        work = Path(tempfile.mkdtemp(dir=self.temp.name)) / "work"
        self.clone(work)
        self.git(work, "checkout", "-q", "--detach", "origin/main")
        (work / self.RECORD).parent.mkdir(parents=True, exist_ok=True)
        (work / self.RECORD).write_text(json.dumps({"tag": tag}, indent=2) + "\n")
        env = dict(os.environ, CI_COMMIT_TAG=tag, RELEASE_REMOTE=str(self.remote))
        result = subprocess.run(
            [str(ROOT / "scripts/record-devenv-ci.sh")],
            cwd=work,
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
        return result.stdout

    def main_record(self):
        return json.loads(self.git(self.remote, "show", f"main:{self.RECORD}"))["tag"]

    def commits(self):
        return int(self.git(self.remote, "rev-list", "--count", "main"))

    def test_records_and_advances(self):
        self.record("devenv-ci-1.2.9")
        self.assertEqual(self.main_record(), "devenv-ci-1.2.9")
        self.record("devenv-ci-1.2.10")
        self.assertEqual(self.main_record(), "devenv-ci-1.2.10")
        self.assertEqual(self.commits(), 3)

    def test_rerun_is_a_no_op(self):
        self.record("devenv-ci-1.2.3")
        self.assertIn("already records", self.record("devenv-ci-1.2.3"))
        self.assertEqual(self.commits(), 2)

    def test_older_release_does_not_replace_newer(self):
        self.record("devenv-ci-1.3.0")
        self.assertIn("newer", self.record("devenv-ci-1.2.10"))
        self.assertEqual(self.main_record(), "devenv-ci-1.3.0")


if __name__ == "__main__":
    unittest.main()
