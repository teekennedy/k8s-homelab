"""Settings and agent profiles: one document each, one user.

Wire shapes are the frontend's: `api/cloud/settings-service.api.js` (GET
returns agent/conversation settings plus app preferences flattened beside
them; POST sends `*_diff` objects plus preferences) and the agent server's own
`agent_profiles_router.py`, which the cloud client calls unchanged at
`/api/agent-profiles`.
"""

import copy
import json
import re
import shlex
import uuid
from pathlib import Path
from typing import Any

from .db import Database

SETTINGS_KEY = "settings"
PROFILES_KEY = "agent_profiles"
# The declared model last applied per ACP server; see apply_declared_models.
APPLIED_MODELS_KEY = "applied_models"
LLM_PROFILES_KEY = "llm_profiles"

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
        # Per ACP server: the `command` and `session_mode` conversations
        # launch with and the default `model`, all declared in values.yaml
        # rather than owned by the UI.
        self._servers: dict[str, dict[str, Any]] = self._seed.get("acp_servers") or {}

    def _command(self, agent: dict[str, Any]) -> list[str] | None:
        """The declared launch command for an ACP agent's server, if it has
        one. Where it does, nothing stored is used in its place, and nothing
        is stored: the form then shows the preset's own command, which is the
        only text it recognises the preset by."""
        if agent.get("agent_kind") != "acp":
            return None
        return (self._servers.get(agent.get("acp_server") or "") or {}).get("command")

    def _declared_server(self, agent: dict[str, Any]) -> None:
        """Put an agent back on the ACP server whose declared command it
        carries. The form names its preset from the command text alone, so
        it shows a declared command as "Custom" and saves it as that server,
        which would cost the profile its model list and its declared
        settings."""
        command = agent.get("acp_command")
        if isinstance(command, str):
            command = shlex.split(command)
        command = [*(command or []), *(agent.get("acp_args") or [])]
        for server, cfg in self._servers.items():
            if command and command == cfg.get("command"):
                agent["acp_server"] = server

    def _with_command(self, agent: dict[str, Any]) -> dict[str, Any]:
        command = self._command(agent)
        if command:
            agent = {**agent, "acp_command": command, "acp_args": []}
        mode = (self._servers.get(agent.get("acp_server") or "") or {}).get(
            "session_mode"
        )
        if mode and agent.get("agent_kind") == "acp":
            agent = {**agent, "acp_session_mode": mode}
        return agent

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
        # Cloud Canvas gates native launches on the settings readiness flag.
        # Subscription credentials are resolved by the sandbox, not stored here.
        resolved = self.resolve_agent(None)
        if resolved.get("agent_kind") == "openhands":
            api_key_set = api_key_set or (
                (resolved.get("llm") or {}).get("auth_type") == "subscription"
            )
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
        self._declared_server(doc["agent_settings"])
        if self._command(doc["agent_settings"]):
            # The form sends back the declared command it was shown.
            doc["agent_settings"].pop("acp_command", None)
            doc["agent_settings"].pop("acp_args", None)
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
        profile = self._new_profile(name, body, existing)
        if profile["agent_kind"] == "openhands":
            self.llm_config(profile["llm_profile_ref"])
        self._declared_server(profile)
        if self._command(profile):
            profile["acp_command"] = profile["acp_args"] = None
        doc["profiles"][name] = profile
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

    def ensure_declared_profiles(self) -> None:
        """Seed additional agents and LLMs without replacing UI edits."""
        llms = self._llm_profiles()
        for name, config in (self._seed.get("llm_profiles") or {}).items():
            if name not in llms["profiles"]:
                llms["profiles"][name] = self._subscription_config(config)
        self._put(LLM_PROFILES_KEY, llms)
        agents = self._profiles()
        for name, body in (self._seed.get("agent_profiles") or {}).items():
            if name not in agents["profiles"]:
                agents["profiles"][name] = self._new_profile(name, body, None)
        self._put(PROFILES_KEY, agents)

    def _llm_profiles(self) -> dict[str, Any]:
        return self._get(LLM_PROFILES_KEY, {"profiles": {}, "active": None})

    @staticmethod
    def _subscription_config(config: dict[str, Any]) -> dict[str, Any]:
        """Subscription profiles contain model settings, never credentials."""
        if not isinstance(config, dict):
            raise ProfileError(422, "llm must be an object")
        model = config.get("model")
        if not isinstance(model, str) or not model.strip():
            raise ProfileError(422, "model is required")
        if config.get("api_key") or config.get("provider_connection_id"):
            raise ProfileError(422, "subscription credentials belong in the sandbox")
        return {
            "model": model,
            "auth_type": "subscription",
            "subscription_vendor": "openai",
            "stream": True,
            **{
                k: config[k]
                for k in ("reasoning_effort", "max_input_tokens")
                if config.get(k) is not None
            },
        }

    def llm_config(self, name: str) -> dict[str, Any]:
        config = self._llm_profiles()["profiles"].get(name)
        if config is None:
            raise ProfileError(404, f"LLM profile {name!r} not found")
        return copy.deepcopy(config)

    def list_llm_profiles(self) -> dict[str, Any]:
        doc = self._llm_profiles()
        return {
            "profiles": [
                # Cloud Canvas uses this as backend credential readiness and
                # does not inspect subscription auth in profile details.
                # The credential itself is resolved inside the sandbox.
                {"name": name, "model": cfg["model"], "api_key_set": True}
                for name, cfg in doc["profiles"].items()
            ],
            "active_profile": doc["active"],
        }

    def save_llm_profile(self, name: str, config: dict[str, Any]) -> dict[str, str]:
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", name):
            raise ProfileError(422, "invalid LLM profile name")
        doc = self._llm_profiles()
        doc["profiles"][name] = self._subscription_config(config)
        self._put(LLM_PROFILES_KEY, doc)
        return {"name": name, "message": "saved"}

    def mutate_llm_profile(self, name: str, new_name: str | None) -> dict[str, str]:
        self.llm_config(name)
        if new_name is not None:
            if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", new_name):
                raise ProfileError(422, "invalid LLM profile name")
            if new_name in self._llm_profiles()["profiles"]:
                raise ProfileError(409, "LLM profile already exists")
        agents = self._profiles()
        refs = [
            p for p in agents["profiles"].values() if p.get("llm_profile_ref") == name
        ]
        if refs and new_name is None:
            raise ProfileError(409, "LLM profile is referenced by an agent profile")
        doc = self._llm_profiles()
        config = doc["profiles"].pop(name)
        if new_name is not None:
            doc["profiles"][new_name] = config
            for profile in refs:
                profile["llm_profile_ref"] = new_name
                profile["revision"] += 1
            self._put(PROFILES_KEY, agents)
        if doc["active"] == name:
            doc["active"] = new_name
        self._put(LLM_PROFILES_KEY, doc)
        return {
            "name": new_name or name,
            "message": "renamed" if new_name else "deleted",
        }

    def activate_llm_profile(self, name: str) -> dict[str, Any]:
        config = self.llm_config(name)
        doc = self._llm_profiles()
        doc["active"] = name
        self._put(LLM_PROFILES_KEY, doc)
        return {"name": name, "message": "activated", "model": config["model"]}

    def apply_declared_models(self) -> None:
        """Set the declared default model on the stored settings and on every
        profile of that ACP server — once per declared value. Until the value
        in values.yaml changes again, the model picker's choice stands."""
        applied = self._get(APPLIED_MODELS_KEY, {})
        changed = {
            server: cfg["model"]
            for server, cfg in self._servers.items()
            if cfg.get("model") and applied.get(server) != cfg["model"]
        }
        if not changed:
            return
        settings = self.settings()
        agent = settings["agent_settings"]
        if agent.get("agent_kind") == "acp" and agent.get("acp_server") in changed:
            agent["acp_model"] = changed[agent["acp_server"]]
            self._put(SETTINGS_KEY, settings)
        doc = self._profiles()
        for profile in doc["profiles"].values():
            model = changed.get(profile.get("acp_server") or "")
            if profile["agent_kind"] == "acp" and model:
                profile["acp_model"] = model
                profile["revision"] += 1
        self._put(PROFILES_KEY, doc)
        self._put(APPLIED_MODELS_KEY, {**applied, **changed})

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

        OpenHands profiles resolve the active LLM selection or their own ref.
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
        elif profile and profile["agent_kind"] == "openhands":
            agent = {k: v for k, v in agent.items() if not k.startswith("acp_")}
            agent["agent_kind"] = "openhands"
            name = self._llm_profiles()["active"] or profile["llm_profile_ref"]
            agent["llm"] = self.llm_config(name)
        return self._with_command(agent)
