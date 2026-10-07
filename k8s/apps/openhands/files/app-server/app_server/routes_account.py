"""The single-user account surface, the forge's repositories, the pages the
frontend expects to exist but this deployment has nothing to put in, and the
automation service.

One user, one personal organization whose id equals the user id: what the
frontend reads as "personal workspace" (`api/cloud/types.d.ts`).
"""

from typing import Annotated, Any

from fastapi import APIRouter, Depends, HTTPException, Query, Request

from .auth import Principal, principal
from .forge import ForgeError
from .proxy import forward

router = APIRouter()
Caller = Annotated[Principal, Depends(principal)]

# Fixed, so it survives a volume loss.
USER_ID = "00000000-0000-4000-8000-000000000001"


@router.get("/api/organizations")
async def organizations(who: Caller) -> dict[str, Any]:
    return {
        "items": [{"id": USER_ID, "name": "Personal", "is_personal": True}],
        "current_org_id": USER_ID,
    }


@router.get("/api/organizations/{org_id}/me")
async def organization_me(org_id: str, who: Caller) -> dict[str, Any]:
    return {
        "org_id": USER_ID,
        "user_id": USER_ID,
        "email": None,
        "role": "owner",
        "status": "active",
    }


@router.get("/api/keys/current")
async def current_key(who: Caller) -> dict[str, Any]:
    return {
        "id": who.kind,
        "name": who.name,
        "org_id": USER_ID,
        "user_id": USER_ID,
        "auth_type": "cookie" if who.kind == "user" else "api_key",
    }


@router.get("/api/v1/users/me")
async def users_me(who: Caller) -> dict[str, Any]:
    """Who a credential belongs to, as the automation service asks before
    serving any request (`automation/auth.py`, _UsersMe). The ids become the
    owner of every automation and run."""
    return {
        "id": USER_ID,
        "org_id": USER_ID,
        "email": None,
        "role": "owner",
        "permissions": ["view_automations", "manage_automations"],
    }


@router.post("/api/authenticate")
async def authenticate(who: Caller) -> dict[str, bool]:
    """The cookie-mode session check the frontend makes on load; a 401 here
    would send the browser to a /login this deployment does not have, but
    oauth2-proxy has already authenticated anything that reaches it."""
    return {"success": True}


@router.post("/api/analytics/events", status_code=204)
async def analytics(who: Caller) -> None:
    """Product analytics. Dropped: telemetry is off for this deployment."""


@router.get("/api/organizations/{org_id}/profiles")
@router.get("/api/v1/settings/profiles")
async def llm_profiles(who: Caller, org_id: str | None = None) -> dict[str, Any]:
    """LLM profiles. Conversations here run ACP agents, which bring their own
    model; an OpenHands-kind agent profile falls back to agent_settings."""
    return {"profiles": [], "active_profile": None}


def _empty_page() -> dict[str, Any]:
    return {"items": [], "next_page_id": None}


@router.get("/api/v1/git/repositories/search")
async def repositories(
    request: Request,
    who: Caller,
    query: str | None = None,
    limit: Annotated[int, Query(ge=1)] = 100,
    page_id: str | None = None,
) -> dict[str, Any]:
    """Repositories on the forge, whatever `provider` the frontend names: its
    picker falls back to `github` when it has not read the provider list."""
    try:
        return await request.app.state.forge.repositories(query, limit, page_id)
    except ForgeError as e:
        raise HTTPException(502, str(e))


@router.get("/api/v1/git/branches/search")
async def branches(
    request: Request,
    who: Caller,
    repository: str,
    query: str | None = None,
    limit: Annotated[int, Query(ge=1)] = 30,
    page_id: str | None = None,
) -> dict[str, Any]:
    try:
        return await request.app.state.forge.branches(repository, query, limit, page_id)
    except ForgeError as e:
        raise HTTPException(502, str(e))


# No app installations or suggested tasks on the forge, and no skill or model
# marketplace.
for _path in (
    "/api/v1/git/installations/search",
    "/api/v1/git/suggested-tasks/search",
    "/api/v1/skills/search",
    "/api/v1/config/models/search",
    "/api/v1/config/providers/search",
):
    router.add_api_route(
        _path, _empty_page, methods=["GET"], dependencies=[Depends(principal)]
    )


@router.api_route(
    "/api/automation/{path:path}",
    methods=["GET", "POST", "PUT", "PATCH", "DELETE"],
    include_in_schema=False,
)
async def automation(path: str, request: Request, who: Caller):
    """The automation service authenticates a browser by a cookie this
    deployment has no issuer for, so the browser reaches it through here:
    authenticated by oauth2-proxy, forwarded with the service's own key,
    which it then checks against /api/v1/users/me."""
    s = request.app.state.settings
    return await forward(
        request,
        f"{s.automation_url}/api/automation/{path}",
        "automation service",
        {"X-Session-API-Key": s.automation_api_key},
    )
