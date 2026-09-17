"""Versioned Hermes adapter for M365 native original attachments.

The adapter owns only turn-scoped staging state.  It never changes Hermes
messages and never exposes a staged capability to the model.
"""

from __future__ import annotations

import base64
import copy
import hashlib
import hmac
import http.client
import json
import mimetypes
import os
import re
import stat as stat_module
import threading
import unicodedata
from collections import OrderedDict
from typing import Any
from urllib.parse import urlsplit


_STAGE_PATH = "/hermes/v1/attachments/stage"
_RELEASE_PATH = "/hermes/v1/attachments/release"
_TURN_PATH = "/hermes/v1/attachments/turn"
_STAGE_SCHEMA = "m365-hermes-native-attachment-stage/v2"
_CONTEXT_SCHEMA = "m365-hermes-native-attachment-context/v1"
_CONTEXT_FIELD = "m365_native_attachment_context"
_MAX_NATIVE_ATTACHMENTS = 2
_MAX_FILE_BYTES = 512 << 20
_MAX_TURNS = 256
_MAX_OUTCOMES = _MAX_TURNS * 2
_MAX_ENDED_TURNS = _MAX_TURNS * 2
_MAX_PENDING_ENDS = _MAX_TURNS * 4
_CHUNK_SIZE = 128 * 1024
_CAPABILITY = re.compile(r"^[A-Za-z0-9_-]{43}$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_ALLOWED_FILE_KEYS = frozenset(
    {"local_path", "attachment_id", "source_message_id", "expected_sha256"}
)
_PRIVATE_HOST_IDENTITY = "_m365_native_host_identity"
_ATTACHMENT_KEY_DOMAIN = b"m365-hermes-native-attachments/v1"
_STAGE_AUTH_DOMAIN = b"m365-hermes-native-attachments/stage/v1"
_RELEASE_AUTH_DOMAIN = b"m365-hermes-native-attachments/release/v1"
_CONTEXT_SIGNATURE_DOMAIN = b"m365-hermes-native-attachments/context/v1"
_BINDING_DOMAIN = b"m365-hermes-native-attachments/binding/v1"
_TURN_AUTH_DOMAIN = b"m365-hermes-native-attachments/turn/v1"
_STAGE_ID_DOMAIN = b"m365-hermes-native-attachments/stage-id/v1"
_GATEWAY_BASE_URL_ENV = "M365_HERMES_GATEWAY_BASE_URL"
_AUTH_HEADER = "X-M365-Hermes-Attachment-Auth"
_SESSION_HEADER = "X-M365-Hermes-Session-Id"
_TURN_HEADER = "X-M365-Hermes-Turn-Id"
_BINDING_HEADER = "X-M365-Hermes-Turn-Binding"
_EXPECTED_SIZE_HEADER = "X-M365-Expected-Size"
_STAGE_ID_HEADER = "X-M365-Hermes-Stage-Id"
_TURN_ACTION_HEADER = "X-M365-Hermes-Turn-Action"

_TOOL_FAILURES = frozenset(
    {
        "invalid_attachment_count",
        "attachment_slot_unavailable",
        "path_denied",
        "file_missing",
        "not_regular_file",
        "empty_file",
        "file_too_large",
        "hash_mismatch",
        "local_file_changed",
        "stage_transport_failed",
    }
)
_HIDDEN_FAILURES = frozenset(
    {
        "native_attachment_state_lost",
        "native_attachment_binding_invalid",
        "native_attachment_context_malformed",
        "native_attachment_capability_invalid_or_expired",
        "native_attachment_slot_conflict",
        "native_attachment_integrity_failed",
        "native_attachments_not_allowed",
    }
)
_ALL_FAILURES = _TOOL_FAILURES | _HIDDEN_FAILURES

TurnKey = tuple[str, str]

_sessions: OrderedDict[TurnKey, dict[str, Any]] = OrderedDict()
_outcomes: OrderedDict[TurnKey, dict[str, Any]] = OrderedDict()
# A bounded exact-turn tombstone prevents a delayed callback from reviving a
# turn after the host has delivered on_session_end.  This is turn lifecycle
# state, not a session-wide failure/poison marker.
_ended: OrderedDict[TurnKey, None] = OrderedDict()
# A failed end is a retryable local cleanup operation. Keep its exact turn
# binding until a later request observes a successful idempotent end.
_pending_ends: OrderedDict[TurnKey, tuple[str, str]] = OrderedDict()
_lock = threading.RLock()
_operation_locks: dict[TurnKey, threading.Lock] = {}


class _AttachmentFailure(Exception):
    def __init__(self, reason: str, stage_refs: list[str] | None = None):
        self.reason = reason if reason in _ALL_FAILURES else "stage_transport_failed"
        self.stage_refs = list(stage_refs or [])


class _OpenedFile:
    def __init__(self, path: Any, handle: Any, identity: tuple[int, int, int, int, int]):
        self.path = path
        self.handle = handle
        self.identity = identity
        self.size = identity[2]


def _has_control(value: str) -> bool:
    return any(unicodedata.category(character) == "Cc" for character in value)


def _text_identity(value: Any, limit: int = 512) -> str | None:
    if not isinstance(value, str) or not value.strip() or len(value) > limit:
        return None
    if _has_control(value):
        return None
    return value.strip()


def _turn_key(session_id: Any, turn_id: Any) -> TurnKey | None:
    session = _text_identity(session_id)
    turn = _text_identity(turn_id)
    if session is None or turn is None:
        return None
    return session, turn


def _is_m365(provider: Any, api_mode: Any) -> bool:
    configured = os.environ.get("M365_HERMES_PROVIDER", "").strip()
    return bool(configured and provider == configured and api_mode == "chat_completions")


def _attach_key() -> bytes:
    secret = os.environ.get("M365_HERMES_RECALL_PROVENANCE_SECRET", "").strip()
    if not secret:
        raise _AttachmentFailure("stage_transport_failed")
    return hmac.new(secret.encode("utf-8"), _ATTACHMENT_KEY_DOMAIN, hashlib.sha256).digest()


def _hmac_hex(key: bytes, message: bytes) -> str:
    return hmac.new(key, message, hashlib.sha256).hexdigest()


def _lines(*parts: Any) -> bytes:
    return b"\n".join(
        part if isinstance(part, bytes) else str(part).encode("utf-8") for part in parts
    )


def _turn_binding(session_key: str, turn_id: str) -> str:
    return _hmac_hex(_attach_key(), _lines(_BINDING_DOMAIN, session_key, turn_id))


def _stage_id(opened: _OpenedFile, session_key: str, turn_id: str) -> str:
    digest = hmac.new(
        _attach_key(),
        _lines(_STAGE_ID_DOMAIN, session_key, turn_id, *opened.identity),
        hashlib.sha256,
    ).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


def _context_canonical(context: dict[str, Any]) -> bytes:
    attachments = context.get("attachments", [])
    lines = [
        _CONTEXT_SIGNATURE_DOMAIN,
        context.get("schema", ""),
        context.get("session_key", ""),
        context.get("turn_id", ""),
        context.get("error", "") or "",
        len(attachments),
    ]
    for reference in attachments:
        lines.extend(
            [
                reference.get("stage_ref", ""),
                reference.get("size", ""),
                reference.get("sha256", ""),
                reference.get("original_filename", ""),
                reference.get("extension", ""),
                reference.get("mime_type", ""),
                reference.get("attachment_id", ""),
                reference.get("source_message_id", ""),
            ]
        )
    return _lines(*lines)


def _sign_context(context: dict[str, Any]) -> str:
    return "sha256=" + _hmac_hex(_attach_key(), _context_canonical(context))


def _context_wire(
    session_key: str,
    turn_id: str,
    references: list[dict[str, Any]],
    error: str | None = None,
) -> dict[str, Any]:
    context: dict[str, Any] = {
        "schema": _CONTEXT_SCHEMA,
        "session_key": session_key,
        "turn_id": turn_id,
        "attachments": copy.deepcopy(references),
    }
    if error is not None:
        context["error"] = error
    context["signature"] = _sign_context(context)
    return context


def _copy_request(request: Any) -> dict[str, Any]:
    if not isinstance(request, dict):
        raise _AttachmentFailure("native_attachment_context_malformed")
    try:
        return copy.deepcopy(request)
    except Exception as exc:
        del exc
        return dict(request)


def _request_with_context(
    request: Any,
    session_key: str,
    turn_id: str,
    references: list[dict[str, Any]],
    error: str | None = None,
) -> dict[str, Any]:
    updated = _copy_request(request)
    raw_extra = updated.get("extra_body")
    if raw_extra is None:
        extra: dict[str, Any] = {}
    elif isinstance(raw_extra, dict):
        extra = copy.deepcopy(raw_extra)
    else:
        raise _AttachmentFailure("native_attachment_context_malformed")
    extra[_CONTEXT_FIELD] = _context_wire(session_key, turn_id, references, error)
    updated["extra_body"] = extra
    # The Gateway binds the signed context to the OpenAI-wire session_key. Hermes
    # does not add that field for custom providers, so put the same authority in
    # extra_body; the OpenAI client merges extra_body into the JSON wire body.
    extra["session_key"] = session_key
    updated["extra_body"] = extra
    return updated


def _request_without_context(request: Any) -> Any:
    if not isinstance(request, dict) or not isinstance(request.get("extra_body"), dict):
        return request
    updated = _copy_request(request)
    extra = updated["extra_body"]
    extra.pop(_CONTEXT_FIELD, None)
    updated["extra_body"] = extra
    return updated


def _has_unresolved_native_context(request: Any) -> bool:
    if not isinstance(request, dict) or not isinstance(request.get("extra_body"), dict):
        return False
    if _CONTEXT_FIELD not in request["extra_body"]:
        return False
    context = request["extra_body"][_CONTEXT_FIELD]
    return not (
        isinstance(context, dict)
        and isinstance(context.get("error"), str)
        and context["error"] in _TOOL_FAILURES
    )


def _failure_context(session: str, turn: str, reason: str) -> dict[str, Any]:
    normalized = reason if reason in _ALL_FAILURES else "native_attachment_context_malformed"
    try:
        return _context_wire(session, turn, [], normalized)
    except Exception:
        return {
            "schema": _CONTEXT_SCHEMA,
            "session_key": session,
            "turn_id": turn,
            "attachments": [],
            "error": normalized,
            "signature": "",
        }


def _safe_failure_request(request: Any, session_id: Any, turn_id: Any, reason: str) -> dict[str, Any]:
    session = _text_identity(session_id) or "unknown-session"
    turn = _text_identity(turn_id) or "unknown-turn"
    marker = _failure_context(session, turn, reason)
    try:
        updated = _copy_request(request)
    except Exception:
        updated = dict(request) if isinstance(request, dict) else {}
    raw_extra = updated.get("extra_body")
    if isinstance(raw_extra, dict):
        try:
            extra = copy.deepcopy(raw_extra)
        except Exception:
            extra = {}
    else:
        extra = {}
    extra[_CONTEXT_FIELD] = marker
    updated["extra_body"] = extra
    return {
        "request": updated,
        "source": "m365-hermes-native-attachments",
        "reason": "native attachment transport failed closed",
    }


def _state_for(key: TurnKey) -> tuple[dict[str, Any], list[tuple[str, str, str, list[str]]]]:
    state = _sessions.get(key)
    if state is None:
        state = {"refs": [], "route": "", "session_key": key[0]}
        _sessions[key] = state
    _sessions.move_to_end(key)
    evicted: list[tuple[str, str, str, list[str]]] = []
    while len(_sessions) > _MAX_TURNS:
        evicted_key, evicted_state = _sessions.popitem(last=False)
        refs = [
            reference["stage_ref"]
            for reference in evicted_state.get("refs", [])
            if isinstance(reference, dict) and isinstance(reference.get("stage_ref"), str)
        ]
        route = evicted_state.get("route", "")
        session_key = evicted_state.get("session_key", evicted_key[0])
        if refs and isinstance(route, str) and isinstance(session_key, str):
            evicted.append((route, session_key, evicted_key[1], refs))
            _outcome_for(evicted_key, {"ok": True, "state_lost": True})
        else:
            _outcomes.pop(evicted_key, None)
    return state, evicted


def _outcome_for(key: TurnKey, outcome: dict[str, Any]) -> None:
    _outcomes[key] = outcome
    _outcomes.move_to_end(key)
    while len(_outcomes) > _MAX_OUTCOMES:
        _outcomes.popitem(last=False)


def _gateway_target_identity(base_url: Any) -> tuple[str, int, str] | None:
    configured = os.environ.get(_GATEWAY_BASE_URL_ENV, "").strip()
    if not isinstance(base_url, str) or not base_url.strip() or not configured:
        return None
    try:
        current = _request_target(base_url.strip(), "stage")
        expected = _request_target(configured, "stage")
    except _AttachmentFailure:
        return None
    if not current[3] or not expected[3]:
        return None
    current_identity = (current[0].lower(), current[1] or 443, current[2])
    expected_identity = (expected[0].lower(), expected[1] or 443, expected[2])
    return current_identity if current_identity == expected_identity else None


def _remember_route(key: TurnKey, base_url: Any, request: Any = None) -> None:
    session_key = key[0]
    if isinstance(request, dict) and isinstance(request.get("extra_body"), dict):
        wire_session = request["extra_body"].get("session_key")
        if isinstance(wire_session, str) and wire_session.strip():
            session_key = wire_session.strip()
    route = base_url.strip() if _gateway_target_identity(base_url) is not None else ""
    with _lock:
        if key in _ended:
            return
        state, evicted = _state_for(key)
        state["route"] = route
        state["session_key"] = session_key
    for route, evicted_session_key, turn_id, refs in evicted:
        _release_route(route, evicted_session_key, turn_id, refs)


def _remember_pending_end(key: TurnKey, route: str, session_key: str) -> None:
    if not route or not session_key:
        return
    with _lock:
        _pending_ends[key] = (route, session_key)
        _pending_ends.move_to_end(key)
        while len(_pending_ends) > _MAX_PENDING_ENDS:
            _pending_ends.popitem(last=False)


def _forget_pending_end(key: TurnKey) -> None:
    with _lock:
        _pending_ends.pop(key, None)


def _retry_pending_ends(route: str, session_key: str) -> bool:
    with _lock:
        pending = [
            (key, value)
            for key, value in _pending_ends.items()
            if value == (route, session_key)
        ]
    all_cleared = True
    for key, (pending_route, pending_session_key) in pending:
        if _turn_route(pending_route, pending_session_key, key[1], "end"):
            _forget_pending_end(key)
        else:
            all_cleared = False
    return all_cleared


def _release_route(base_url: str, session_key: str, turn_id: str, stage_refs: list[str]) -> None:
    if not base_url or not stage_refs:
        return
    try:
        host, port, target, secure = _request_target(base_url, "release")
        if not secure:
            raise _AttachmentFailure("stage_transport_failed")
        binding = _turn_binding(session_key, turn_id)
        payload = json.dumps({"stage_refs": stage_refs}, separators=(",", ":")).encode("utf-8")
        auth = _hmac_hex(
            _attach_key(), _lines(_RELEASE_AUTH_DOMAIN, session_key, turn_id, binding, *stage_refs)
        )
        connection = _connection(host, port, secure)
        try:
            connection.request(
                "POST",
                target,
                body=payload,
                headers={
                    "Content-Type": "application/json",
                    "Content-Length": str(len(payload)),
                    _AUTH_HEADER: auth,
                    _SESSION_HEADER: session_key,
                    _TURN_HEADER: turn_id,
                    _BINDING_HEADER: binding,
                },
            )
            response = connection.getresponse()
            response.read(64 * 1024)
        finally:
            connection.close()
    except Exception:
        return


def _turn_route(base_url: str, session_key: str, turn_id: str, action: str) -> bool:
    if not base_url or action not in {"bind", "end"}:
        return False
    try:
        host, port, target, secure = _request_target(base_url, "turn")
        if not secure:
            raise _AttachmentFailure("stage_transport_failed")
        binding = _turn_binding(session_key, turn_id)
        auth = _hmac_hex(
            _attach_key(),
            _lines(_TURN_AUTH_DOMAIN, session_key, turn_id, binding, action),
        )
        connection = _connection(host, port, secure)
        try:
            connection.request(
                "POST",
                target,
                body=b"",
                headers={
                    "Content-Length": "0",
                    _AUTH_HEADER: auth,
                    _SESSION_HEADER: session_key,
                    _TURN_HEADER: turn_id,
                    _BINDING_HEADER: binding,
                    _TURN_ACTION_HEADER: action,
                },
            )
            response = connection.getresponse()
            response.read(64 * 1024)
            return response.status == 200
        finally:
            connection.close()
    except Exception:
        return False


def _clear_turn(key: TurnKey, outcome: dict[str, Any] | None = None) -> None:
    with _lock:
        state = _sessions.get(key)
        refs = list(state.get("refs", [])) if state else []
        route = state.get("route", "") if state else ""
        session_key = state.get("session_key", key[0]) if state else key[0]
        if state is not None:
            state["refs"] = []
        if outcome is not None:
            _outcome_for(key, outcome)
    _release_route(route, session_key, key[1], [ref["stage_ref"] for ref in refs])


def _tool_error(reason: str) -> str:
    return json.dumps(
        {"ok": False, "error": {"code": reason}}, separators=(",", ":")
    )


def _safe_tool_success(key: TurnKey, session_key: str, references: list[dict[str, Any]]) -> str:
    return json.dumps(
        {
            "ok": True,
            "turn_binding": {"session_id": key[0], "turn_id": key[1]},
            "attachments": [
                {
                    "original_filename": reference["original_filename"],
                    "extension": reference["extension"],
                    "mime_type": reference["mime_type"],
                    "size": reference["size"],
                    "sha256": reference["sha256"],
                    "attachment_id": reference["attachment_id"],
                    "source_message_id": reference["source_message_id"],
                }
                for reference in references
            ],
        },
        ensure_ascii=False,
        separators=(",", ":"),
    )


def on_tool_request(
    tool_name: Any = "",
    args: Any = None,
    session_id: Any = "",
    turn_id: Any = "",
    tool_call_id: Any = "",
    **_: Any,
) -> dict[str, Any] | None:
    """Pass exact host identity to the handler without changing Hermes core."""

    if tool_name != "m365_native_attach":
        return None
    updated = copy.deepcopy(args) if isinstance(args, dict) else {}
    updated[_PRIVATE_HOST_IDENTITY] = {
        "session_id": session_id,
        "turn_id": turn_id,
        "tool_call_id": tool_call_id,
    }
    return {"args": updated}


def on_llm_request(
    request: Any,
    provider: Any = "",
    api_mode: Any = "",
    session_id: Any = "",
    turn_id: Any = "",
    base_url: Any = "",
    **_: Any,
) -> dict[str, Any] | None:
    if not _is_m365(provider, api_mode):
        return None
    key = _turn_key(session_id, turn_id)
    if key is None:
        return _safe_failure_request(
            request, session_id, turn_id, "native_attachment_binding_invalid"
        )
    try:
        with _lock:
            if key in _ended:
                return _safe_failure_request(
                    request, session_id, turn_id, "native_attachment_binding_invalid"
                )
        _remember_route(key, base_url, request)
        with _lock:
            outcome = copy.deepcopy(_outcomes.get(key))
            state = copy.deepcopy(_sessions.get(key))
        unresolved_context = _has_unresolved_native_context(request)
        if state and state.get("route"):
            pending_end_ok = _retry_pending_ends(
                state["route"], state.get("session_key", key[0])
            )
            if not _turn_route(state["route"], state.get("session_key", key[0]), key[1], "bind"):
                if (
                    not pending_end_ok
                    or state.get("refs")
                    or (outcome is not None and outcome.get("ok"))
                ):
                    return _safe_failure_request(
                        request, session_id, turn_id, "native_attachment_binding_invalid"
                    )
        if outcome is not None and not outcome.get("ok"):
            return {
                "request": _request_without_context(request),
                "source": "m365-hermes-native-attachments",
                "reason": "last native attachment tool outcome was a safe failure",
            }
        if not outcome:
            if unresolved_context or (state and state.get("refs")):
                session_key = state.get("session_key", key[0])
                return {
                    "request": _request_with_context(
                        request, session_key, key[1], [], "native_attachment_state_lost"
                    ),
                    "source": "m365-hermes-native-attachments",
                    "reason": "native attachment outcome state loss failed closed",
                }
            return {
                "request": _request_without_context(request),
                "source": "m365-hermes-native-attachments",
                "reason": "no active native attachment context",
            }
        if not state or not state.get("refs"):
            session_key = state.get("session_key", key[0]) if state else key[0]
            return {
                "request": _request_with_context(
                    request, session_key, key[1], [], "native_attachment_state_lost"
                ),
                "source": "m365-hermes-native-attachments",
                    "reason": "native attachment state loss failed closed",
                }
        if not state.get("route"):
            session_key = state.get("session_key", key[0])
            return {
                "request": _request_with_context(
                    request, session_key, key[1], [], "native_attachment_binding_invalid"
                ),
                "source": "m365-hermes-native-attachments",
                "reason": "current M365 Gateway authority is not pinned",
            }
        session_key = state.get("session_key", key[0])
        references = [copy.deepcopy(reference) for reference in state["refs"]]
        return {
            "request": _request_with_context(request, session_key, key[1], references),
            "source": "m365-hermes-native-attachments",
            "reason": "signed M365 native attachment context",
        }
    except _AttachmentFailure as failure:
        return _safe_failure_request(request, session_id, turn_id, failure.reason)
    except Exception:
        return _safe_failure_request(
            request, session_id, turn_id, "native_attachment_context_malformed"
        )


def _allowed_roots() -> list[Any]:
    roots = []
    for raw in os.environ.get("M365_HERMES_ATTACHMENT_ALLOWED_ROOTS", "").split(os.pathsep):
        if not raw.strip():
            continue
        try:
            root = __import__("pathlib").Path(raw.strip()).resolve(strict=True)
            if root.is_dir():
                roots.append(root)
        except (OSError, RuntimeError, ValueError):
            continue
    return roots


def _identity_from_stat(value: Any) -> tuple[int, int, int, int, int]:
    return (
        value.st_dev,
        value.st_ino,
        value.st_size,
        value.st_mtime_ns,
        value.st_ctime_ns,
    )


def _open_allowed_file(raw_path: Any) -> _OpenedFile:
    from pathlib import Path

    if (
        not isinstance(raw_path, str)
        or not raw_path.strip()
        or len(raw_path) > 4096
        or _has_control(raw_path)
        or any(character in "\r\n\t" for character in raw_path)
    ):
        raise _AttachmentFailure("path_denied")
    original = Path(raw_path)
    try:
        original_stat = original.lstat()
        resolved = original.resolve(strict=True)
        resolved_stat = resolved.stat()
    except FileNotFoundError:
        raise _AttachmentFailure("file_missing") from None
    except (OSError, RuntimeError, ValueError):
        raise _AttachmentFailure("path_denied") from None
    if stat_module.S_ISLNK(original_stat.st_mode):
        raise _AttachmentFailure("path_denied")
    if not stat_module.S_ISREG(resolved_stat.st_mode):
        raise _AttachmentFailure("not_regular_file")
    roots = _allowed_roots()
    if not roots or not any(root == resolved or root in resolved.parents for root in roots):
        raise _AttachmentFailure("path_denied")
    if resolved_stat.st_size == 0:
        raise _AttachmentFailure("empty_file")
    if resolved_stat.st_size > _MAX_FILE_BYTES:
        raise _AttachmentFailure("file_too_large")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(resolved, flags)
        handle = os.fdopen(descriptor, "rb")
    except OSError:
        raise _AttachmentFailure("local_file_changed") from None
    opened = _OpenedFile(resolved, handle, _identity_from_stat(resolved_stat))
    try:
        current = _identity_from_stat(os.fstat(handle.fileno()))
    except OSError:
        handle.close()
        raise _AttachmentFailure("local_file_changed") from None
    if current != opened.identity:
        handle.close()
        raise _AttachmentFailure("local_file_changed")
    return opened


def _mime_type(filename: str) -> str:
    return mimetypes.guess_type(filename, strict=False)[0] or "application/octet-stream"


def _extension(filename: str) -> str:
    leaf = filename.rsplit("/", 1)[-1]
    if "." not in leaf or leaf.startswith(".") or leaf.endswith("."):
        return ""
    return leaf.rsplit(".", 1)[1]


def _request_target(base_url: str, endpoint: str, *, test_only: bool = False) -> tuple[str, int | None, str, bool]:
    try:
        parsed = urlsplit(base_url)
        if (
            parsed.scheme not in {"http", "https"}
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.query
            or parsed.fragment
            or (parsed.scheme != "https" and not test_only)
        ):
            raise ValueError
        path = parsed.path.rstrip("/")
        if path.endswith("/chat/completions"):
            path = path[: -len("/chat/completions")]
        if path.endswith("/hermes/v1"):
            root = path
        elif path.endswith("/v1"):
            root = path[: -len("/v1")] + "/hermes/v1"
        else:
            root = path + "/hermes/v1"
        return parsed.hostname, parsed.port, root.rstrip("/") + "/attachments/" + endpoint, parsed.scheme == "https"
    except (TypeError, ValueError):
        raise _AttachmentFailure("stage_transport_failed") from None


def _connection(host: str, port: int | None, secure: bool) -> http.client.HTTPConnection:
    if not secure:
        raise _AttachmentFailure("stage_transport_failed")
    return http.client.HTTPSConnection(host, port=port, timeout=300)


def _stage_file(opened: _OpenedFile, base_url: str, session_key: str, turn_id: str) -> dict[str, Any]:
    host, port, target, secure = _request_target(base_url, "stage")
    binding = _turn_binding(session_key, turn_id)
    stage_id = _stage_id(opened, session_key, turn_id)
    connection = _connection(host, port, secure)
    digest = hashlib.sha256()
    sent = 0
    try:
        auth = _hmac_hex(
            _attach_key(),
            _lines(
                _STAGE_AUTH_DOMAIN,
                session_key,
                turn_id,
                binding,
                opened.size,
                stage_id,
            ),
        )
        connection.putrequest("POST", target)
        connection.putheader("Content-Type", _mime_type(opened.path.name)[:128])
        connection.putheader("Content-Length", str(opened.size))
        connection.putheader(_AUTH_HEADER, auth)
        connection.putheader(_SESSION_HEADER, session_key)
        connection.putheader(_TURN_HEADER, turn_id)
        connection.putheader(_BINDING_HEADER, binding)
        connection.putheader(_EXPECTED_SIZE_HEADER, str(opened.size))
        connection.putheader(_STAGE_ID_HEADER, stage_id)
        connection.endheaders()
        while sent < opened.size:
            chunk = opened.handle.read(min(_CHUNK_SIZE, opened.size - sent))
            if not chunk:
                break
            connection.send(chunk)
            sent += len(chunk)
            digest.update(chunk)
        try:
            after = _identity_from_stat(os.fstat(opened.handle.fileno()))
        except OSError:
            raise _AttachmentFailure("local_file_changed") from None
        local_file_changed = sent != opened.size or after != opened.identity
        local_sha256 = digest.hexdigest()
        response = connection.getresponse()
        payload = response.read(64 * 1024 + 1)
    except _AttachmentFailure:
        raise
    except (OSError, ValueError, http.client.HTTPException):
        raise _AttachmentFailure("stage_transport_failed") from None
    finally:
        connection.close()
    if response.status != 200 or len(payload) > 64 * 1024:
        raise _AttachmentFailure("stage_transport_failed")
    try:
        value = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise _AttachmentFailure("stage_transport_failed") from None
    capability = value.get("capability") if isinstance(value, dict) else None
    if (
        not isinstance(value, dict)
        or value.get("schema") != _STAGE_SCHEMA
        or not isinstance(capability, str)
        or not _CAPABILITY.fullmatch(capability)
        or value.get("size") != opened.size
        or not isinstance(value.get("sha256"), str)
        or value["sha256"] != local_sha256
    ):
        raise _AttachmentFailure("stage_transport_failed")
    if local_file_changed:
        raise _AttachmentFailure("local_file_changed", [capability])
    return {"stage_ref": capability, "size": opened.size, "sha256": local_sha256}


def _try_acquire_operation(key: TurnKey) -> threading.Lock | None:
    with _lock:
        operation_lock = _operation_locks.setdefault(key, threading.Lock())
        if not operation_lock.acquire(blocking=False):
            return None
        return operation_lock


def _release_operation(key: TurnKey, operation_lock: threading.Lock) -> None:
    with _lock:
        operation_lock.release()
        if _operation_locks.get(key) is operation_lock:
            _operation_locks.pop(key, None)


def _attach_once(args: Any, key: TurnKey) -> str:
    with _lock:
        if key in _ended:
            return _tool_error("stage_transport_failed")
    files = args.get("files") if isinstance(args, dict) else None
    if not isinstance(files, list) or not 1 <= len(files) <= _MAX_NATIVE_ATTACHMENTS:
        _clear_turn(key, {"ok": False, "error": "invalid_attachment_count"})
        return _tool_error("invalid_attachment_count")
    with _lock:
        state = copy.deepcopy(_sessions.get(key))
    if not state or not state.get("route"):
        _clear_turn(key, {"ok": False, "error": "stage_transport_failed"})
        return _tool_error("stage_transport_failed")
    if not _retry_pending_ends(state["route"], state["session_key"]):
        _clear_turn(key, {"ok": False, "error": "stage_transport_failed"})
        return _tool_error("stage_transport_failed")
    if not _turn_route(state["route"], state["session_key"], key[1], "bind"):
        _clear_turn(key, {"ok": False, "error": "stage_transport_failed"})
        return _tool_error("stage_transport_failed")
    staged: list[dict[str, Any]] = []
    opened_files: list[_OpenedFile] = []
    try:
        for item in files:
            if not isinstance(item, dict) or not set(item).issubset(_ALLOWED_FILE_KEYS):
                raise _AttachmentFailure("stage_transport_failed")
            opened = _open_allowed_file(item.get("local_path"))
            opened_files.append(opened)
            filename = opened.path.name
            if len(filename) > 512 or not filename or _has_control(filename):
                raise _AttachmentFailure("stage_transport_failed")
            extension = _extension(filename)
            if len(extension) > 64:
                raise _AttachmentFailure("stage_transport_failed")
            attachment_id = item.get("attachment_id", "")
            source_message_id = item.get("source_message_id", "")
            for value in (attachment_id, source_message_id):
                if value is not None and (
                    not isinstance(value, str) or len(value) > 512 or _has_control(value)
                ):
                    raise _AttachmentFailure("stage_transport_failed")
            expected = item.get("expected_sha256")
            if expected is not None and (
                not isinstance(expected, str) or not _SHA256.fullmatch(expected)
            ):
                raise _AttachmentFailure("hash_mismatch")
            reference = _stage_file(opened, state["route"], state["session_key"], key[1])
            staged.append(
                {
                    **reference,
                    "original_filename": filename,
                    "extension": extension,
                    "mime_type": _mime_type(filename),
                    "attachment_id": attachment_id or "",
                    "source_message_id": source_message_id or "",
                }
            )
            if expected is not None and expected != reference["sha256"]:
                raise _AttachmentFailure("hash_mismatch")
            if any(item["stage_ref"] == reference["stage_ref"] for item in staged[:-1]):
                raise _AttachmentFailure("stage_transport_failed", [reference["stage_ref"]])
    except _AttachmentFailure as failure:
        rollback_refs = [item["stage_ref"] for item in staged]
        rollback_refs.extend(failure.stage_refs)
        _release_route(
            state["route"], state["session_key"], key[1], list(dict.fromkeys(rollback_refs))
        )
        for opened in opened_files:
            opened.handle.close()
        _clear_turn(key, {"ok": False, "error": failure.reason})
        return _tool_error(failure.reason)
    except Exception:
        _release_route(
            state["route"], state["session_key"], key[1], [item["stage_ref"] for item in staged]
        )
        for opened in opened_files:
            opened.handle.close()
        _clear_turn(key, {"ok": False, "error": "stage_transport_failed"})
        return _tool_error("stage_transport_failed")
    finally:
        for opened in opened_files:
            if not opened.handle.closed:
                opened.handle.close()
    old_refs: list[dict[str, Any]] = []
    lifecycle_invalid = False
    with _lock:
        current = _sessions.get(key)
        if (
            key in _ended
            or current is None
            or current.get("route") != state["route"]
            or current.get("session_key") != state["session_key"]
        ):
            lifecycle_invalid = True
        else:
            old_refs = list(current.get("refs", []))
            current["refs"] = staged
            _outcome_for(key, {"ok": True})
    if lifecycle_invalid:
        _release_route(
            state["route"], state["session_key"], key[1],
            [item["stage_ref"] for item in staged],
        )
        return _tool_error("stage_transport_failed")
    new_refs = {item["stage_ref"] for item in staged}
    obsolete_refs = [
        reference["stage_ref"]
        for reference in old_refs
        if reference["stage_ref"] not in new_refs
    ]
    _release_route(state["route"], state["session_key"], key[1], obsolete_refs)
    return _safe_tool_success(key, state["session_key"], staged)


def _attach(args: Any, key: TurnKey) -> str:
    operation_lock = _try_acquire_operation(key)
    if operation_lock is None:
        return _tool_error("stage_transport_failed")
    try:
        return _attach_once(args, key)
    finally:
        _release_operation(key, operation_lock)


def m365_native_attach(args: dict[str, Any], session_id: str = "", turn_id: str = "", **_: Any) -> str:
    args = copy.deepcopy(args) if isinstance(args, dict) else {}
    identity = args.pop(_PRIVATE_HOST_IDENTITY, None)
    if isinstance(identity, dict):
        session_id = identity.get("session_id", session_id)
        turn_id = identity.get("turn_id", turn_id)
    key = _turn_key(session_id, turn_id)
    if key is None:
        return _tool_error("stage_transport_failed")
    try:
        return _attach(args, key)
    except Exception:
        with _lock:
            ended = key in _ended
        if not ended:
            _clear_turn(key, {"ok": False, "error": "stage_transport_failed"})
        return _tool_error("stage_transport_failed")


def on_session_end(session_id: Any = "", turn_id: Any = "", **_: Any) -> None:
    key = _turn_key(session_id, turn_id)
    if key is None:
        return
    with _lock:
        _ended[key] = None
        _ended.move_to_end(key)
        while len(_ended) > _MAX_ENDED_TURNS:
            _ended.popitem(last=False)
        state = copy.deepcopy(_sessions.get(key))
    route = state.get("route", "") if state else ""
    session_key = state.get("session_key", key[0]) if state else key[0]
    _clear_turn(key)
    if route:
        _remember_pending_end(key, route, session_key)
        if _turn_route(route, session_key, key[1], "end"):
            _forget_pending_end(key)
    with _lock:
        _sessions.pop(key, None)
        _outcomes.pop(key, None)


def on_session_reset(session_id: Any = "", turn_id: Any = "", **kwargs: Any) -> None:
    on_session_end(session_id, turn_id, **kwargs)


def on_session_finalize(session_id: Any = "", turn_id: Any = "", **kwargs: Any) -> None:
    on_session_end(session_id, turn_id, **kwargs)


_TOOL_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "required": ["files"],
    "properties": {
        "files": {
            "type": "array",
            "minItems": 1,
            "maxItems": 2,
            "items": {
                "type": "object",
                "additionalProperties": False,
                "required": ["local_path"],
                "properties": {
                    "local_path": {"type": "string", "maxLength": 4096},
                    "attachment_id": {"type": "string", "maxLength": 512},
                    "source_message_id": {"type": "string", "maxLength": 512},
                    "expected_sha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"},
                },
            },
        }
    },
}


def register(ctx: Any) -> None:
    ctx.register_tool(
        name="m365_native_attach",
        toolset="m365",
        schema=_TOOL_SCHEMA,
        handler=m365_native_attach,
        requires_env=[
            "M365_HERMES_RECALL_PROVENANCE_SECRET",
            "M365_HERMES_PROVIDER",
            "M365_HERMES_GATEWAY_BASE_URL",
            "M365_HERMES_ATTACHMENT_ALLOWED_ROOTS",
        ],
        description="Stage one or two original local attachments for the M365 native model.",
    )
    ctx.register_middleware("tool_request", on_tool_request)
    ctx.register_middleware("llm_request", on_llm_request)
    ctx.register_hook("on_session_end", on_session_end)
