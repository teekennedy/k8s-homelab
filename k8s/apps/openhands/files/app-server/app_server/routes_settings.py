"""Settings, their schemas, and agent profiles."""

import json
from typing import Annotated, Any

from fastapi import APIRouter, Body, Depends, HTTPException, Request
from fastapi.responses import Response

from .auth import principal
from .secrets_store import SecretError, SecretsStore
from .settings_store import ProfileError, SettingsStore

router = APIRouter(dependencies=[Depends(principal)])


def _store(request: Request) -> SettingsStore:
    return request.app.state.settings_store


def _view(request: Request) -> dict[str, Any]:
    return {
        **_store(request).settings_view(),
        "provider_tokens_set": request.app.state.forge.providers(),
    }


@router.get("/api/v1/settings")
async def get_settings(request: Request) -> dict[str, Any]:
    return _view(request)


@router.post("/api/v1/settings")
async def save_settings(
    request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, Any]:
    _store(request).save_settings(body)
    return _view(request)


def _schema(request: Request, name: str) -> Response:
    path = request.app.state.settings.schemas_dir / f"{name}.json"
    if not path.exists():
        raise HTTPException(503, f"{name} schema is not available")
    return Response(path.read_bytes(), media_type="application/json")


@router.get("/api/v1/settings/agent-schema")
async def agent_schema(request: Request) -> Response:
    return _schema(request, "agent-schema")


@router.get("/api/v1/settings/conversation-schema")
async def conversation_schema(request: Request) -> Response:
    return _schema(request, "conversation-schema")


def _profiles(call):
    try:
        return call()
    except ProfileError as e:
        raise HTTPException(e.status, e.detail)


@router.get("/api/agent-profiles")
async def list_profiles(request: Request) -> dict[str, Any]:
    return _store(request).list_profiles()


@router.get("/api/agent-profiles/{name}")
async def get_profile(name: str, request: Request) -> dict[str, Any]:
    return _profiles(lambda: _store(request).get_profile(name))


@router.post("/api/agent-profiles/{name}")
async def save_profile(
    name: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, str]:
    return _profiles(lambda: _store(request).save_profile(name, body))


@router.delete("/api/agent-profiles/{name}")
async def delete_profile(name: str, request: Request) -> dict[str, str]:
    return _profiles(lambda: _store(request).delete_profile(name))


@router.post("/api/agent-profiles/{name}/rename")
async def rename_profile(
    name: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, str]:
    return _profiles(lambda: _store(request).rename_profile(name, body["new_name"]))


@router.post("/api/agent-profiles/{profile_id}/activate")
async def activate_profile(profile_id: str, request: Request) -> dict[str, Any]:
    return _profiles(lambda: _store(request).activate_profile(profile_id))


def _secrets(request: Request) -> SecretsStore:
    return request.app.state.secrets_store


def _secret_call(call):
    try:
        return call()
    except SecretError as e:
        raise HTTPException(e.status, e.detail)


@router.get("/api/v1/secrets/search")
async def search_secrets(
    request: Request, limit: int = 100, page_id: str | None = None
) -> dict[str, Any]:
    return _secrets(request).page(min(max(limit, 1), 100), page_id)


@router.post("/api/v1/secrets")
async def create_secret(
    request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, bool]:
    _secret_call(
        lambda: _secrets(request).create(
            body["name"], body["value"], body.get("description")
        )
    )
    return {"success": True}


@router.put("/api/v1/secrets/{name}")
async def update_secret(
    name: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, bool]:
    _secret_call(
        lambda: _secrets(request).update(
            name, body.get("name"), body.get("description")
        )
    )
    return {"success": True}


@router.delete("/api/v1/secrets/{name}")
async def delete_secret(name: str, request: Request) -> dict[str, bool]:
    _secret_call(lambda: _secrets(request).delete(name))
    return {"success": True}
