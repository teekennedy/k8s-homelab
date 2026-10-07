"""Settings and agent profiles: one document each, one user.

Wire shapes are the frontend's: `api/cloud/settings-service.api.js` (GET
returns agent/conversation settings plus app preferences flattened beside
them; POST sends `*_diff` objects plus preferences) and the agent server's own
`agent_profiles_router.py`, which the cloud client calls unchanged at
`/api/agent-profiles`.
"""

import copy
import json
import uuid
from pathlib import Path
from typing import Any

from .db import Database

SETTINGS_KEY = "settings"
PROFILES_KEY = "agent_profiles"

# Fields an ACP profile carries over onto agent_settings at launch.
ACP_PROFILE_FIELDS = (
    "acp_server",
    "acp_model",
    "acp_session_mode",
    "acp_prompt_timeout",
    "acp_command",
    "acp_args",
)
ACP_PROFILE_DEFAULTS = {
    "acp_model": None,
    "acp_session_mode": None,
    "acp_prompt_timeout": 1800.0,
    "acp_command": None,
    "acp_args": None,
}
PROFILE_SCHEMA_VERSION = 2


class ProfileError(Exception):
    def __init__(self, status: int, detail: str):
        super().__init__(detail)
        self.status = status
        self.detail = detail


def deep_merge(base: dict[str, Any], diff: dict[str, Any]) -> dict[str, Any]:
    out = copy.deepcopy(base)
    for k, v in diff.items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = deep_merge(out[k], v)
        else:
            out[k] = copy.deepcopy(v)
    return out


class SettingsStore:
    def __init__(self, db: Database, seed_file: Path | None):
        self.db = db
        self._seed = json.loads(seed_file.read_text()) if seed_file else {}

    def _get(self, key: str, default: dict[str, Any]) -> dict[str, Any]:
        row = self.db.one("SELECT value FROM documents WHERE key = ?", key)
        return json.loads(row["value"]) if row else copy.deepcopy(default)

    def _put(self, key: str, value: dict[str, Any]) -> None:
        self.db.run(
            "INSERT INTO documents (key, value) VALUES (?, ?)"
            " ON CONFLICT (key) DO UPDATE SET value = excluded.value",
            key,
            json.dumps(value),
        )

    # --- settings -----------------------------------------------------------

    def settings(self) -> dict[str, Any]:
        """The stored document: agent_settings, conversation_settings,
        app_preferences."""
        seed = {
            "agent_settings": self._seed.get("agent_settings", {}),
            "conversation_settings": self._seed.get("conversation_settings", {}),
            "app_preferences": self._seed.get("app_preferences", {}),
        }
        return self._get(SETTINGS_KEY, seed)

    def settings_view(self) -> dict[str, Any]:
        """GET /api/v1/settings. Credentials are reported as set, never
        returned."""
        doc = self.settings()
        agent = copy.deepcopy(doc["agent_settings"])
        llm = agent.get("llm") or {}
        api_key_set = bool(llm.get("api_key"))
        if "api_key" in llm:
            llm["api_key"] = None
        return {
            **doc["app_preferences"],
            "agent_settings": agent,
            "conversation_settings": doc["conversation_settings"],
            "llm_api_key_set": api_key_set,
            "search_api_key_set": False,
            "provider_tokens_set": {},
        }

    def save_settings(self, body: dict[str, Any]) -> None:
        """POST /api/v1/settings."""
        doc = self.settings()
        agent_diff = copy.deepcopy(body.get("agent_settings_diff") or {})
        # The view returns the key as null; sending that back means "unchanged".
        llm_diff = agent_diff.get("llm")
        if isinstance(llm_diff, dict) and llm_diff.get("api_key") is None:
            llm_diff.pop("api_key", None)
        doc["agent_settings"] = deep_merge(doc["agent_settings"], agent_diff)
        doc["conversation_settings"] = deep_merge(
            doc["conversation_settings"], body.get("conversation_settings_diff") or {}
        )
        prefs = {
            k: v
            for k, v in body.items()
            if k not in ("agent_settings_diff", "conversation_settings_diff")
        }
        doc["app_preferences"] = {**doc["app_preferences"], **prefs}
        self._put(SETTINGS_KEY, doc)

    # --- agent profiles -----------------------------------------------------

    def _profiles(self) -> dict[str, Any]:
        row = self.db.one("SELECT value FROM documents WHERE key = ?", PROFILES_KEY)
        if row:
            return json.loads(row["value"])
        # Seeded once and stored, so the profile's id is stable from first read.
        doc: dict[str, Any] = {"profiles": {}, "active": None}
        seeded = self._seed.get("agent_profile")
        if seeded:
            profile = self._new_profile(seeded["name"], seeded, None)
            doc = {"profiles": {profile["name"]: profile}, "active": profile["id"]}
        self._put(PROFILES_KEY, doc)
        return doc

    @staticmethod
    def _new_profile(
        name: str, body: dict[str, Any], existing: dict[str, Any] | None
    ) -> dict[str, Any]:
        kind = body.get("agent_kind")
        if kind not in ("acp", "openhands"):
            raise ProfileError(422, "agent_kind must be 'acp' or 'openhands'")
        if kind == "openhands" and not body.get("llm_profile_ref"):
            raise ProfileError(422, "llm_profile_ref is required for openhands")
        base = ACP_PROFILE_DEFAULTS if kind == "acp" else {}
        profile = {
            "mcp_server_refs": None,
            "secret_refs": None,
            **base,
            **{k: v for k, v in body.items() if k not in ("id", "revision")},
            "schema_version": PROFILE_SCHEMA_VERSION,
            "id": existing["id"] if existing else str(uuid.uuid4()),
            "name": name,
            "revision": existing["revision"] + 1 if existing else 0,
        }
        return profile

    def list_profiles(self) -> dict[str, Any]:
        doc = self._profiles()
        return {
            "profiles": [
                {
                    "id": p["id"],
                    "name": p["name"],
                    "agent_kind": p["agent_kind"],
                    "revision": p["revision"],
                    "llm_profile_ref": p.get("llm_profile_ref"),
                    "mcp_server_refs": p.get("mcp_server_refs"),
                }
                for p in doc["profiles"].values()
            ],
            "active_agent_profile_id": doc["active"],
        }

    def get_profile(self, name: str) -> dict[str, Any]:
        profile = self._profiles()["profiles"].get(name)
        if profile is None:
            raise ProfileError(404, f"agent profile {name!r} not found")
        return {"name": name, "profile": profile}

    def save_profile(self, name: str, body: dict[str, Any]) -> dict[str, str]:
        doc = self._profiles()
        existing = doc["profiles"].get(name)
        doc["profiles"][name] = self._new_profile(name, body, existing)
        self._put(PROFILES_KEY, doc)
        return {"name": name, "message": "saved"}

    def delete_profile(self, name: str) -> dict[str, str]:
        doc = self._profiles()
        profile = doc["profiles"].pop(name, None)
        if profile is None:
            raise ProfileError(404, f"agent profile {name!r} not found")
        if doc["active"] == profile["id"]:
            doc["active"] = None
        self._put(PROFILES_KEY, doc)
        return {"name": name, "message": "deleted"}

    def rename_profile(self, name: str, new_name: str) -> dict[str, str]:
        doc = self._profiles()
        if name not in doc["profiles"]:
            raise ProfileError(404, f"agent profile {name!r} not found")
        if new_name in doc["profiles"]:
            raise ProfileError(409, f"agent profile {new_name!r} already exists")
        profile = doc["profiles"].pop(name)
        profile["name"] = new_name
        doc["profiles"][new_name] = profile
        self._put(PROFILES_KEY, doc)
        return {"name": new_name, "message": "renamed"}

    def activate_profile(self, profile_id: str) -> dict[str, Any]:
        doc = self._profiles()
        if not any(p["id"] == profile_id for p in doc["profiles"].values()):
            raise ProfileError(404, f"agent profile {profile_id!r} not found")
        doc["active"] = profile_id
        self._put(PROFILES_KEY, doc)
        return {
            "id": profile_id,
            "message": "activated",
            "agent_settings_applied": False,
        }

    def ensure_profiles(self, names: list[str]) -> None:
        """Make sure a profile of each name exists, copied from the seeded one.
        Run at every start, so a profile deleted in the UI comes back."""
        seeded = self._seed.get("agent_profile")
        if not seeded:
            return
        doc = self._profiles()
        missing = [n for n in names if n not in doc["profiles"]]
        for name in missing:
            doc["profiles"][name] = self._new_profile(name, seeded, None)
        if missing:
            self._put(PROFILES_KEY, doc)

    # --- launch -------------------------------------------------------------

    def profile_name(self, profile_id: str | None) -> str | None:
        """Name of the named (or active) profile, if it exists."""
        doc = self._profiles()
        wanted = profile_id or doc["active"]
        return next(
            (p["name"] for p in doc["profiles"].values() if p["id"] == wanted), None
        )

    def resolve_agent(self, profile_id: str | None) -> dict[str, Any]:
        """agent_settings for a new conversation: the stored settings, with
        the named (or active) ACP profile's fields laid over them.

        An OpenHands-kind profile resolves through an LLM profile, which this
        server does not store; its conversations launch from agent_settings,
        which is also what the frontend falls back to.
        """
        agent = copy.deepcopy(self.settings()["agent_settings"])
        doc = self._profiles()
        wanted = profile_id or doc["active"]
        profile = next((p for p in doc["profiles"].values() if p["id"] == wanted), None)
        if profile and profile["agent_kind"] == "acp":
            agent["agent_kind"] = "acp"
            for field in ACP_PROFILE_FIELDS:
                if profile.get(field) is not None:
                    agent[field] = profile[field]
        return agent
