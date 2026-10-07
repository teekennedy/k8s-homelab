import shutil
import subprocess

import httpx
import pytest

from app_server.forge import ForgeError

REPOS = [
    {
        "id": 1,
        "full_name": "ops/k8s-homelab",
        "private": False,
        "default_branch": "main",
        "stars_count": 2,
        "updated_at": "2026-10-07T22:16:23Z",
        "owner": {"login": "ops"},
    },
    {
        "id": 7,
        "full_name": "alice/k8s-notes",
        "private": True,
        "default_branch": "trunk",
        "owner": {"login": "alice"},
    },
]
BRANCHES = [
    {
        "name": name,
        "protected": name == "main",
        "commit": {"id": f"sha-{name}", "timestamp": "2026-10-07T16:13:35-06:00"},
    }
    for name in ("main", "feat/one", "feat/two")
]


class FakeForge:
    def __init__(self):
        self.requests: list[httpx.Request] = []
        self.status = 200

    def __call__(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        if self.status != 200:
            return httpx.Response(self.status)
        if request.url.path == "/api/v1/repos/search":
            return httpx.Response(
                200, json={"ok": True, "data": REPOS}, headers={"X-Total-Count": "5"}
            )
        if request.url.path == "/api/v1/repos/ops/k8s-homelab/branches":
            return httpx.Response(200, json=BRANCHES, headers={"X-Total-Count": "3"})
        return httpx.Response(404)

    @property
    def params(self) -> dict[str, str]:
        return dict(self.requests[-1].url.params)


@pytest.fixture
def forge(state) -> FakeForge:
    fake = FakeForge()
    state.forge.http = httpx.AsyncClient(transport=httpx.MockTransport(fake))
    return fake


def test_settings_name_the_forge_as_the_one_provider(api, user):
    got = api.get("/api/v1/settings", headers=user).json()
    assert got["provider_tokens_set"] == {"forgejo": "forge.example"}


def test_repositories_come_from_the_forge_whatever_the_provider(api, user, forge):
    page = api.get(
        "/api/v1/git/repositories/search",
        params={"provider": "github", "limit": 2},
        headers=user,
    ).json()
    request = forge.requests[-1]
    assert request.url.host == "forge.example"
    assert request.headers["Authorization"] == "token forge-token"
    assert forge.params == {
        "limit": "2",
        "page": "1",
        "sort": "updated",
        "order": "desc",
    }
    assert page["next_page_id"] == "2"
    assert page["items"][0] == {
        "id": "1",
        "full_name": "ops/k8s-homelab",
        "git_provider": "forgejo",
        "is_public": True,
        "stargazers_count": 2,
        "link_header": None,
        "pushed_at": "2026-10-07T22:16:23Z",
        "owner_type": None,
        "main_branch": "main",
    }
    assert page["items"][1]["is_public"] is False


def test_an_owner_in_the_query_narrows_the_forges_name_search(api, user, forge):
    page = api.get(
        "/api/v1/git/repositories/search",
        params={"provider": "forgejo", "query": "ops/k8s", "page_id": "3"},
        headers=user,
    ).json()
    assert forge.params == {"limit": "50", "page": "3", "q": "k8s"}
    assert [r["full_name"] for r in page["items"]] == ["ops/k8s-homelab"]
    assert page["next_page_id"] is None


def test_branches_are_filtered_and_paged_here(api, user, forge):
    def search(**params):
        return api.get(
            "/api/v1/git/branches/search",
            params={"provider": "github", "repository": "ops/k8s-homelab", **params},
            headers=user,
        ).json()

    page = search(limit=2)
    assert page["items"][0] == {
        "name": "main",
        "commit_sha": "sha-main",
        "protected": True,
        "last_push_date": "2026-10-07T16:13:35-06:00",
    }
    assert len(page["items"]) == 2 and page["next_page_id"] == "2"
    assert [b["name"] for b in search(limit=2, page_id="2")["items"]] == ["feat/two"]
    assert [b["name"] for b in search(query="FEAT")["items"]] == [
        "feat/one",
        "feat/two",
    ]
    # Never interpolated into a forge path.
    assert search(repository="../admin/users")["items"] == []


def test_a_forge_failure_is_a_bad_gateway(api, user, forge):
    forge.status = 500
    r = api.get("/api/v1/git/repositories/search", headers=user)
    assert r.status_code == 502 and "500" in r.json()["detail"]


def test_no_token_means_no_provider_and_no_results(api, user, state, forge):
    state.forge.token = ""
    assert api.get("/api/v1/settings", headers=user).json()["provider_tokens_set"] == {}
    page = api.get("/api/v1/git/repositories/search", headers=user).json()
    assert page == {"items": [], "next_page_id": None} and not forge.requests


def _git(cwd, *args: str) -> str:
    return subprocess.run(
        ["git", "-c", "user.name=t", "-c", "user.email=t@example", *args],
        cwd=cwd,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


@pytest.mark.skipif(shutil.which("git") is None, reason="needs git")
@pytest.mark.parametrize("branch", ["feat/it's", None])
def test_clone_command_checks_out_into_an_initialised_directory(
    state, tmp_path, branch
):
    origin = tmp_path / "forge" / "ops" / "repo.git"
    seed = tmp_path / "seed"
    seed.mkdir()
    _git(seed, "init", "-q", "-b", "trunk")
    (seed / "a.txt").write_text("trunk")
    _git(seed, "add", "."), _git(seed, "commit", "-qm", "one")
    _git(seed, "checkout", "-qb", "feat/it's")
    (seed / "a.txt").write_text("feature")
    _git(seed, "commit", "-qam", "two")
    _git(seed, "clone", "-q", "--bare", str(seed), str(origin))
    _git(origin, "symbolic-ref", "HEAD", "refs/heads/trunk")

    # As the agent server leaves it: a repository with nothing in it.
    project = tmp_path / "project"
    project.mkdir()
    _git(project, "init", "-q")

    state.forge.url = f"file://{tmp_path}/forge"
    command = state.forge.clone_command("ops/repo", branch)
    subprocess.run(["sh", "-c", command], cwd=project, check=True)
    assert (project / "a.txt").read_text() == ("feature" if branch else "trunk")
    assert _git(project, "rev-parse", "--abbrev-ref", "HEAD") == (branch or "trunk")
    assert _git(project, "rev-parse", "--abbrev-ref", "@{u}") == (
        f"origin/{branch or 'trunk'}"
    )


def test_clone_command_refuses_what_is_not_a_repository_name(state):
    with pytest.raises(ForgeError):
        state.forge.clone_command("ops/x; rm -rf /", None)
