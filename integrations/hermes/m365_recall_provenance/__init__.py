"""Trusted provenance for ephemeral Hermes recall source material."""

from __future__ import annotations

import hashlib
import hmac
import json
import math
import os
import threading
from collections import OrderedDict
from typing import Any


_SCHEMA = "m365-hermes-recall-provenance/v1"
_FIELD = "m365_recall_provenance"
_CONTROL_SCHEMA = "m365-hermes-execution-control-provenance/v2"
_CONTROL_FIELD = "m365_execution_control_provenance"
_CONTROL_CONTEXT_DOMAIN = "m365-hermes-execution-control-context/v2"
_EMPTY_RECOVERY_ASSISTANT = "(empty)"
_EMPTY_RECOVERY_USER_NUDGE = (
    "You just executed tool calls but returned an empty response. "
    "Please process the tool results above and continue with the task."
)
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


def _canonical_float(value: float) -> str:
    """Match serde_json's finite-f64 compact representation."""

    if not math.isfinite(value):
        raise ValueError("non-finite JSON number")
    encoded = repr(value).lower()
    if "e" not in encoded:
        return encoded

    mantissa, raw_exponent = encoded.split("e", 1)
    exponent = int(raw_exponent)
    if -5 <= exponent <= 15:
        negative = mantissa.startswith("-")
        if negative:
            mantissa = mantissa[1:]
        digits = mantissa.replace(".", "")
        decimal_at = 1 + exponent
        if decimal_at <= 0:
            encoded = "0." + ("0" * -decimal_at) + digits
        elif decimal_at >= len(digits):
            encoded = digits + ("0" * (decimal_at - len(digits))) + ".0"
        else:
            encoded = digits[:decimal_at] + "." + digits[decimal_at:]
        return ("-" if negative else "") + encoded

    sign = "+" if exponent >= 0 else "-"
    return f"{mantissa}e{sign}{abs(exponent)}"


def _canonical_json(value: Any) -> str:
    """Compact canonical JSON shared with the Gateway's serde_json Value form."""

    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        if value < -(1 << 63) or value > (1 << 64) - 1:
            raise ValueError("JSON integer outside Gateway number domain")
        return str(value)
    if isinstance(value, float):
        return _canonical_float(value)
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False, allow_nan=False)
    if isinstance(value, list):
        return "[" + ",".join(_canonical_json(item) for item in value) + "]"
    if isinstance(value, dict):
        if not all(isinstance(key, str) for key in value):
            raise TypeError("JSON object keys must be strings")
        return "{" + ",".join(
            _canonical_json(key) + ":" + _canonical_json(value[key])
            for key in sorted(value)
        ) + "}"
    raise TypeError(f"unsupported JSON value: {type(value).__name__}")


def _json_bytes(value: Any) -> bytes:
    return _canonical_json(value).encode("utf-8")


def _json_sha256(value: Any) -> str:
    return hashlib.sha256(_json_bytes(value)).hexdigest()


def _normalized_message(message: Any) -> dict[str, Any] | None:
    if not isinstance(message, dict):
        return None
    tool_calls = message.get("tool_calls")
    if not isinstance(tool_calls, list):
        tool_calls = []
    return {
        "content": message.get("content"),
        "name": str(message.get("name") or ""),
        "role": str(message.get("role") or ""),
        "tool_call_id": str(message.get("tool_call_id") or ""),
        "tool_calls": tool_calls,
        "tool_result_is_error": bool(message.get("tool_result_is_error", False)),
    }


def _messages_sha256(messages: list[Any]) -> str | None:
    normalized = [_normalized_message(message) for message in messages]
    if any(message is None for message in normalized):
        return None
    try:
        return _json_sha256(normalized)
    except (TypeError, ValueError, OverflowError):
        return None


def _context_sha256(
    session_key: str,
    messages_sha256: str,
) -> str:
    return hashlib.sha256(
        "\0".join((_CONTROL_CONTEXT_DOMAIN, session_key, messages_sha256)).encode("utf-8")
    ).hexdigest()


def _stock_gateway_session_key() -> str:
    """Read Hermes' task-local gateway conversation key from the stock host seam.

    Hermes v0.21.0 binds ``HERMES_SESSION_KEY`` in ``gateway.session_context``
    before the agent turn and propagates that ContextVar into its worker thread.
    Reading it here gives the plugin the same stable conversation subject that
    M365 already uses for implicit Hermes checkpoints, without modifying Hermes
    core or trusting a caller-provided request field.
    """

    try:
        from gateway.session_context import get_session_env

        return str(get_session_env("HERMES_SESSION_KEY", "") or "").strip()
    except Exception:
        return ""


def _control_signature_payload(metadata: dict[str, Any]) -> bytes:
    parts = [
        metadata["schema"],
        metadata["messages_sha256"],
        metadata["context_sha256"],
        str(metadata["api_call_count"]),
        str(len(metadata["controls"])),
    ]
    for control in metadata["controls"]:
        for field in (
            "call_index",
            "tool_call_id_sha256",
            "tool_call_sha256",
            "tool_result_index",
            "tool_result_content_sha256",
            "tool_result_is_error",
            "assistant_index",
            "assistant_content_sha256",
            "user_index",
            "user_content_sha256",
        ):
            value = control[field]
            if isinstance(value, bool):
                parts.append("true" if value else "false")
            else:
                parts.append(str(value))
    return "\n".join(parts).encode("utf-8")

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


def _current_turn_user_matches(
    messages: list[Any],
    clean: str,
    before_index: int,
    trusted_recovery_users: set[int],
) -> bool:
    for index in range(before_index - 1, -1, -1):
        message = messages[index]
        if not isinstance(message, dict) or str(message.get("role") or "") != "user":
            continue
        if index in trusted_recovery_users:
            continue
        content = message.get("content")
        if not isinstance(content, str):
            return False
        return content == clean or content.startswith(clean + "\n\n")
    return False


def _matching_tool_call(
    messages: list[Any], tool_result_index: int, tool_call_id: str
) -> tuple[int, dict[str, Any]] | None:
    for index in range(tool_result_index - 1, -1, -1):
        message = messages[index]
        if not isinstance(message, dict):
            return None
        role = str(message.get("role") or "")
        if role == "user":
            return None
        if role != "assistant":
            continue
        calls = message.get("tool_calls")
        if not isinstance(calls, list):
            continue
        for call in calls:
            if isinstance(call, dict) and str(call.get("id") or "") == tool_call_id:
                # Between the assistant call batch and this result, only tool
                # results from that same batch may appear.
                allowed = {
                    str(candidate.get("id") or "")
                    for candidate in calls
                    if isinstance(candidate, dict)
                }
                between = messages[index + 1 : tool_result_index + 1]
                if all(
                    isinstance(candidate, dict)
                    and str(candidate.get("role") or "") == "tool"
                    and str(candidate.get("tool_call_id") or "") in allowed
                    for candidate in between
                ):
                    return index, call
                return None
    return None


def _execution_control_metadata(
    messages: list[Any],
    *,
    clean: str,
    session_key: str,
    session_id: str,
    turn_id: str,
    api_request_id: str,
    api_call_count: int,
) -> dict[str, Any] | None:
    if (
        not clean
        or not session_key
        or not session_id
        or not turn_id
        or api_call_count < 2
        or api_request_id != f"{turn_id}:api:{api_call_count}"
    ):
        return None
    messages_sha256 = _messages_sha256(messages)
    if messages_sha256 is None:
        return None
    digest = lambda value: hashlib.sha256(value.encode("utf-8")).hexdigest()
    controls: list[dict[str, Any]] = []
    trusted_recovery_users: set[int] = set()
    for user_index in range(3, len(messages)):
        tool_result_index = user_index - 2
        tool = messages[tool_result_index]
        assistant = messages[user_index - 1]
        user = messages[user_index]
        if not all(isinstance(message, dict) for message in (tool, assistant, user)):
            continue
        tool_call_id = str(tool.get("tool_call_id") or "")
        if (
            str(tool.get("role") or "") != "tool"
            or not tool_call_id
            or str(assistant.get("role") or "") != "assistant"
            or assistant.get("tool_calls") not in (None, [])
            or assistant.get("content") != _EMPTY_RECOVERY_ASSISTANT
            or str(user.get("role") or "") != "user"
            or user.get("tool_calls") not in (None, [])
            or user.get("content") != _EMPTY_RECOVERY_USER_NUDGE
            or not _current_turn_user_matches(
                messages, clean, user_index, trusted_recovery_users
            )
        ):
            continue
        matched = _matching_tool_call(messages, tool_result_index, tool_call_id)
        if matched is None:
            continue
        call_index, call = matched
        try:
            tool_call_sha256 = _json_sha256(call)
            tool_result_content_sha256 = _json_sha256(tool.get("content"))
        except (TypeError, ValueError, OverflowError):
            continue
        controls.append(
            {
                "call_index": call_index,
                "tool_call_id_sha256": digest(tool_call_id),
                "tool_call_sha256": tool_call_sha256,
                "tool_result_index": tool_result_index,
                "tool_result_content_sha256": tool_result_content_sha256,
                "tool_result_is_error": bool(tool.get("tool_result_is_error", False)),
                "assistant_index": user_index - 1,
                "assistant_content_sha256": digest(_EMPTY_RECOVERY_ASSISTANT),
                "user_index": user_index,
                "user_content_sha256": digest(_EMPTY_RECOVERY_USER_NUDGE),
            }
        )
        trusted_recovery_users.add(user_index)
    if not controls:
        return None
    return {
        "schema": _CONTROL_SCHEMA,
        "messages_sha256": messages_sha256,
        "context_sha256": _context_sha256(session_key, messages_sha256),
        "api_call_count": api_call_count,
        "controls": controls,
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
    messages = request.get("messages")
    if not isinstance(messages, list):
        return None
    updated = dict(request)
    extra_body = dict(request.get("extra_body") or {})
    changed = False

    # The stable Hermes gateway conversation key is host-owned task-local
    # state.  Project it onto the existing M365 ``session_key`` checkpoint
    # seam, but never overwrite a conflicting request value.  A conflict
    # makes execution-control provenance ineligible rather than letting the
    # signer retarget a request chosen by the caller/provider config.
    stock_session_key = _stock_gateway_session_key()
    wire_session_key_present = "session_key" in extra_body
    raw_wire_session_key = extra_body.get("session_key")
    if wire_session_key_present and not isinstance(raw_wire_session_key, str):
        wire_session_key = ""
        session_binding_ok = False
    else:
        wire_session_key = (
            raw_wire_session_key.strip()
            if isinstance(raw_wire_session_key, str)
            else ""
        )
        session_binding_ok = bool(stock_session_key) and (
            not wire_session_key or wire_session_key == stock_session_key
        )
    if session_binding_ok and raw_wire_session_key != stock_session_key:
        extra_body["session_key"] = stock_session_key
        wire_session_key = stock_session_key
        changed = True

    key = _key(str(kwargs.get("session_id") or ""), str(kwargs.get("turn_id") or ""))
    if key is not None:
        with _lock:
            clean = _turns.get(key)
        if clean is not None:
            indexed = [
                (index, message)
                for index, message in enumerate(messages)
                if isinstance(message, dict)
                and str(message.get("role") or "").strip().lower() == "user"
            ]
            if indexed:
                message_index, message = indexed[-1]
                content = message.get("content")
                if isinstance(content, str):
                    metadata = _metadata(message_index, clean, content)
                    if metadata is not None:
                        metadata["signature"] = "sha256=" + hmac.new(
                            secret.encode("utf-8"),
                            _signature_payload(metadata),
                            hashlib.sha256,
                        ).hexdigest()
                        extra_body[_FIELD] = metadata
                        changed = True

    session_id = str(kwargs.get("session_id") or "")
    turn_id = str(kwargs.get("turn_id") or "")
    api_request_id = str(kwargs.get("api_request_id") or "")
    try:
        api_call_count = int(kwargs.get("api_call_count") or 0)
    except (TypeError, ValueError):
        api_call_count = 0
    key = _key(session_id, turn_id)
    clean = None
    if key is not None:
        with _lock:
            clean = _turns.get(key)
    control = (
        _execution_control_metadata(
            messages,
            clean=clean,
            session_key=stock_session_key,
            session_id=session_id,
            turn_id=turn_id,
            api_request_id=api_request_id,
            api_call_count=api_call_count,
        )
        if clean is not None and session_binding_ok
        else None
    )
    if control is not None:
        control["signature"] = "sha256=" + hmac.new(
            secret.encode("utf-8"),
            _control_signature_payload(control),
            hashlib.sha256,
        ).hexdigest()
        extra_body[_CONTROL_FIELD] = control
        changed = True

    if not changed:
        return None
    updated["extra_body"] = extra_body
    return {
        "request": updated,
        "source": "m365-hermes-provenance",
        "reason": "authenticated Hermes request provenance",
    }


def register(ctx: Any) -> None:
    ctx.register_hook("pre_llm_call", on_pre_llm_call)
    ctx.register_hook("post_llm_call", _forget)
    ctx.register_hook("on_session_end", _forget)
    ctx.register_middleware("llm_request", on_llm_request)
