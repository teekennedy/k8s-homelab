"""Subscription LLM profiles owned by the app server."""

from typing import Any

from fastapi import APIRouter, Depends, Request

from .auth import principal
from .routes_settings import _profiles, _store

router = APIRouter(dependencies=[Depends(principal)])


@router.get("")
async def listing(request: Request) -> dict[str, Any]:
    return _store(request).list_llm_profiles()


@router.get("/{name}")
async def detail(name: str, request: Request) -> dict[str, Any]:
    return {
        "name": name,
        "config": _profiles(lambda: _store(request).llm_config(name)),
        "api_key_set": False,
    }


@router.post("/{name}")
async def save(name: str, request: Request, body: dict[str, Any]) -> dict[str, str]:
    return _profiles(
        lambda: _store(request).save_llm_profile(name, body.get("llm") or {})
    )


@router.delete("/{name}")
async def delete(name: str, request: Request) -> dict[str, str]:
    return _profiles(lambda: _store(request).mutate_llm_profile(name, None))


@router.post("/{name}/rename")
async def rename(name: str, request: Request, body: dict[str, Any]) -> dict[str, str]:
    return _profiles(
        lambda: _store(request).mutate_llm_profile(name, body.get("new_name") or "")
    )


@router.post("/{name}/activate")
async def activate(name: str, request: Request) -> dict[str, Any]:
    return _profiles(lambda: _store(request).activate_llm_profile(name))
