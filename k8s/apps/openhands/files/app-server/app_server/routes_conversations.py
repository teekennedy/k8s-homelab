"""/api/v1/app-conversations and the conversation history endpoints.

Calls and shapes: the frontend's cloud client (`CloudClient` in
@openhands/typescript-client, and `api/cloud/conversation-service.api.js`).
Batch gets take `ids`, not `id`.
"""

from typing import Annotated, Any

from fastapi import APIRouter, Body, Depends, HTTPException, Query, Request

from .auth import Principal, principal
from .conversations import ConversationService

router = APIRouter(dependencies=[Depends(principal)])
Ids = Annotated[list[str], Query()]


def _svc(request: Request) -> ConversationService:
    return request.app.state.conversations


@router.post("/api/v1/app-conversations")
async def start(
    request: Request,
    who: Annotated[Principal, Depends(principal)],
    body: Annotated[dict[str, Any], Body()],
) -> dict[str, Any]:
    return _svc(request).start(body, who.name)


@router.get("/api/v1/app-conversations/start-tasks")
async def start_tasks(request: Request, ids: Ids = []) -> list[dict | None]:
    return _svc(request).tasks(ids)


@router.get("/api/v1/app-conversations/search")
async def search(
    request: Request,
    limit: Annotated[int, Query(ge=1, le=100)] = 20,
    page_id: str | None = None,
    sort_order: str = "UPDATED_AT_DESC",
) -> dict[str, Any]:
    svc = _svc(request)
    rows, next_page = svc.page(limit, page_id, sort_order)
    return {"items": await svc.views(rows), "next_page_id": next_page}


@router.get("/api/v1/app-conversations/count")
async def count(request: Request) -> int:
    return _svc(request).count()


@router.get("/api/v1/app-conversations")
async def batch_get(request: Request, ids: Ids = []) -> list[dict | None]:
    svc = _svc(request)
    return await svc.views([svc.row(i) for i in ids])


def _row(request: Request, conv_id: str) -> dict[str, Any]:
    row = _svc(request).row(conv_id)
    if row is None:
        raise HTTPException(404, "unknown conversation")
    return row


@router.get("/api/v1/app-conversations/{conv_id}")
async def get(conv_id: str, request: Request) -> dict[str, Any]:
    return (await _svc(request).views([_row(request, conv_id)]))[0]


@router.patch("/api/v1/app-conversations/{conv_id}")
async def update(
    conv_id: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, Any]:
    svc = _svc(request)
    if not svc.update(conv_id, body):
        raise HTTPException(404, "unknown conversation")
    return (await svc.views([svc.row(conv_id)]))[0]


@router.delete("/api/v1/app-conversations/{conv_id}")
async def delete(conv_id: str, request: Request) -> dict[str, bool]:
    if not await _svc(request).delete(conv_id):
        raise HTTPException(404, "unknown conversation")
    return {"success": True}


@router.get("/api/v1/conversation/{conv_id}/events/search")
async def search_events(
    conv_id: str,
    request: Request,
    limit: Annotated[int, Query(ge=1, le=100)] = 100,
    page_id: str | None = None,
    sort_order: str = "TIMESTAMP",
    timestamp__gte: str | None = None,
    timestamp__lt: str | None = None,
    kind: str | None = None,
) -> dict[str, Any]:
    _row(request, conv_id)
    return _svc(request).search_events(
        conv_id, limit, page_id, sort_order, timestamp__gte, timestamp__lt, kind
    )


@router.get("/api/v1/conversation/{conv_id}/events/count")
async def count_events(conv_id: str, request: Request) -> int:
    _row(request, conv_id)
    return _svc(request).count_events(conv_id)
