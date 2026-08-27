"""Trusted provenance for ephemeral Hermes recall source material."""

from __future__ import annotations

import hashlib
import hmac
import os
import threading
from collections import OrderedDict
from typing import Any


_SCHEMA = "m365-hermes-recall-provenance/v1"
_FIELD = "m365_recall_provenance"
_MAX_ACTIVE_TURNS = 256
_turns: OrderedDict[tuple[str, str], str] = OrderedDict()
_lock = threading.Lock()


def _key(session_id: str, turn_id: str) -> tuple[str, str] | None:
    if not session_id or not turn_id:
        return None
    return session_id, turn_id


def on_pre_llm_call(
    session_id: str = "",
    turn_id: str = "",
    user_message: Any = None,
    **_: Any,
) -> None:
    key = _key(session_id, turn_id)
    if key is None or not isinstance(user_message, str) or not user_message:
        return
    with _lock:
        _turns[key] = user_message
        _turns.move_to_end(key)
        while len(_turns) > _MAX_ACTIVE_TURNS:
            _turns.popitem(last=False)


def _forget(session_id: str = "", turn_id: str = "", **_: Any) -> None:
    with _lock:
        if turn_id:
            _turns.pop((session_id, turn_id), None)
        elif session_id:
            for key in [key for key in _turns if key[0] == session_id]:
                _turns.pop(key, None)


def _signature_payload(metadata: dict[str, Any]) -> bytes:
    return "\n".join(
        str(metadata[field])
        for field in (
            "schema",
            "message_index",
            "message_sha256",
            "clean_prefix_utf8_bytes",
            "clean_prefix_sha256",
            "source_start_utf8",
            "source_end_utf8",
            "source_sha256",
        )
    ).encode("utf-8")


def _metadata(message_index: int, clean: str, content: str) -> dict[str, Any] | None:
    prefix = clean + "\n\n"
    if not content.startswith(prefix):
        return None
    suffix = content[len(prefix) :]
    if not suffix.startswith("<memory-context>\n"):
        return None
    closing = "\n</memory-context>"
    closing_at = suffix.find(closing)
    if closing_at < 0:
        return None
    source = suffix[: closing_at + len(closing)]
    source_start = len(prefix.encode("utf-8"))
    source_end = source_start + len(source.encode("utf-8"))
    digest = lambda value: hashlib.sha256(value.encode("utf-8")).hexdigest()
    return {
        "schema": _SCHEMA,
        "message_index": message_index,
        "message_sha256": digest(content),
        "clean_prefix_utf8_bytes": len(clean.encode("utf-8")),
        "clean_prefix_sha256": digest(clean),
        "source_start_utf8": source_start,
        "source_end_utf8": source_end,
        "source_sha256": digest(source),
    }


def on_llm_request(**kwargs: Any) -> dict[str, Any] | None:
    request = kwargs.get("request")
    if not isinstance(request, dict):
        return None
    secret = os.environ.get("M365_HERMES_RECALL_PROVENANCE_SECRET", "").strip()
    provider = os.environ.get("M365_HERMES_PROVIDER", "").strip()
    if (
        not secret
        or not provider
        or kwargs.get("provider") != provider
        or kwargs.get("api_mode") != "chat_completions"
    ):
        return None
    key = _key(str(kwargs.get("session_id") or ""), str(kwargs.get("turn_id") or ""))
    if key is None:
        return None
    with _lock:
        clean = _turns.get(key)
    if clean is None:
        return None
    messages = request.get("messages")
    if not isinstance(messages, list):
        return None
    indexed = [
        (index, message)
        for index, message in enumerate(messages)
        if isinstance(message, dict)
        and str(message.get("role") or "").strip().lower() == "user"
    ]
    if not indexed:
        return None
    message_index, message = indexed[-1]
    content = message.get("content")
    if not isinstance(content, str):
        return None
    metadata = _metadata(message_index, clean, content)
    if metadata is None:
        return None
    metadata["signature"] = "sha256=" + hmac.new(
        secret.encode("utf-8"), _signature_payload(metadata), hashlib.sha256
    ).hexdigest()
    updated = dict(request)
    extra_body = dict(request.get("extra_body") or {})
    extra_body[_FIELD] = metadata
    updated["extra_body"] = extra_body
    return {
        "request": updated,
        "source": "m365-recall-provenance",
        "reason": "authenticated recalled source range",
    }


def register(ctx: Any) -> None:
    ctx.register_hook("pre_llm_call", on_pre_llm_call)
    ctx.register_hook("post_llm_call", _forget)
    ctx.register_hook("on_session_end", _forget)
    ctx.register_middleware("llm_request", on_llm_request)
