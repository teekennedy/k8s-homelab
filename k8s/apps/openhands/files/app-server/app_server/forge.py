"""The forge (Forgejo) as the frontend's one git provider: repository and
branch search, and the clone a conversation started on a repository begins
with.

Page shapes are the frontend's `Repository` and `Branch`
(`api/git-service/git-service.api.js`, `searchRepositories` and
`getRepositoryBranches`). Searches run as the agent's own forge account, so
the picker offers exactly what a sandbox can clone.
"""

import re
import shlex
from typing import Any
from urllib.parse import urlsplit

import httpx

# The provider name the frontend knows a Forgejo instance by.
PROVIDER = "forgejo"
# Forgejo's default MAX_RESPONSE_ITEMS.
MAX_PAGE = 50
# Branches have no server-side search, so a query filters this many at most.
MAX_BRANCHES = 500
REPOSITORY = re.compile(r"[\w.-]+/[\w.-]+")


class ForgeError(Exception):
    pass


def _page_number(page_id: str | None) -> int:
    return int(page_id) if page_id and page_id.isdigit() else 1


class Forge:
    def __init__(self, http: httpx.AsyncClient, url: str, token: str):
        self.http = http
        self.url = url.rstrip("/")
        self.token = token

    @property
    def configured(self) -> bool:
        return bool(self.url and self.token)

    @property
    def provider(self) -> str | None:
        return PROVIDER if self.configured else None

    def providers(self) -> dict[str, str | None]:
        """Settings' `provider_tokens_set`: provider -> host. The frontend
        enables its repository picker when this is not empty, and builds its
        links to the forge from the host."""
        return {PROVIDER: urlsplit(self.url).netloc} if self.configured else {}

    async def _get(self, path: str, params: dict[str, Any]) -> httpx.Response:
        try:
            resp = await self.http.get(
                f"{self.url}/api/v1{path}",
                params=params,
                headers={"Authorization": f"token {self.token}"},
                timeout=15,
            )
        except httpx.HTTPError as e:
            raise ForgeError(f"the forge did not answer: {e}") from e
        if resp.status_code >= 400:
            raise ForgeError(f"the forge answered {resp.status_code} for {path}")
        return resp

    async def repositories(
        self, query: str | None, limit: int, page_id: str | None
    ) -> dict[str, Any]:
        if not self.configured:
            return {"items": [], "next_page_id": None}
        # The picker searches `owner/name` when given a URL; the forge matches
        # on the name alone.
        owner, _, name = (query or "").strip().rpartition("/")
        limit = max(1, min(limit, MAX_PAGE))
        page = _page_number(page_id)
        params: dict[str, Any] = {"limit": limit, "page": page}
        if name:
            params["q"] = name
        else:
            params.update(sort="updated", order="desc")
        resp = await self._get("/repos/search", params)
        repos = resp.json().get("data") or []
        total = int(resp.headers.get("X-Total-Count") or 0)
        return {
            "items": [
                {
                    "id": str(r["id"]),
                    "full_name": r["full_name"],
                    "git_provider": PROVIDER,
                    "is_public": not r.get("private"),
                    "stargazers_count": r.get("stars_count"),
                    "link_header": None,
                    "pushed_at": r.get("updated_at"),
                    "owner_type": None,
                    "main_branch": r.get("default_branch"),
                }
                for r in repos
                if not owner or r["full_name"].lower().startswith(f"{owner.lower()}/")
            ],
            "next_page_id": str(page + 1) if page * limit < total else None,
        }

    async def branches(
        self, repository: str, query: str | None, limit: int, page_id: str | None
    ) -> dict[str, Any]:
        if not self.configured or not REPOSITORY.fullmatch(repository):
            return {"items": [], "next_page_id": None}
        found: list[dict[str, Any]] = []
        page = 1
        while len(found) < MAX_BRANCHES:
            resp = await self._get(
                f"/repos/{repository}/branches", {"limit": MAX_PAGE, "page": page}
            )
            batch = resp.json()
            found.extend(batch)
            if len(found) >= int(resp.headers.get("X-Total-Count") or 0) or not batch:
                break
            page += 1
        needle = (query or "").strip().lower()
        if needle:
            found = [b for b in found if needle in b["name"].lower()]
        offset = (_page_number(page_id) - 1) * limit
        return {
            "items": [
                {
                    "name": b["name"],
                    "commit_sha": (b.get("commit") or {}).get("id", ""),
                    "protected": bool(b.get("protected")),
                    "last_push_date": (b.get("commit") or {}).get("timestamp"),
                }
                for b in found[offset : offset + limit]
            ],
            "next_page_id": (
                str(_page_number(page_id) + 1) if offset + limit < len(found) else None
            ),
        }

    def clone_command(self, repository: str, branch: str | None) -> str:
        """Shell command that checks a repository out into the directory it
        runs in. Not `git clone`: the agent server has already made that
        directory a repository of its own. Credentials are the sandbox's: its
        git credential helper."""
        if not self.url or not REPOSITORY.fullmatch(repository):
            raise ForgeError(f"{repository!r} is not a repository on the forge")
        remote = shlex.quote(f"{self.url}/{repository}.git")
        if branch:
            ref = shlex.quote(f"origin/{branch}")
            checkout = f"git checkout -q -B {shlex.quote(branch)} --track {ref}"
        else:
            checkout = (
                "git remote set-head origin -a >/dev/null"
                ' && ref="$(git symbolic-ref --short refs/remotes/origin/HEAD)"'
                ' && git checkout -q -B "${ref#origin/}" --track "$ref"'
            )
        return (
            "git init -q && { git remote remove origin 2>/dev/null; "
            f"git remote add origin {remote}; }} && git fetch -q origin && {checkout}"
        )
