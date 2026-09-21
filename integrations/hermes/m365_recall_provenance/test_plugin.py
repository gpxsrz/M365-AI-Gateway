import contextlib
import hashlib
import hmac
import importlib.util
import io
import json
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch


PLUGIN_PATH = Path(__file__).with_name("__init__.py")
SPEC = importlib.util.spec_from_file_location("m365_recall_provenance", PLUGIN_PATH)
plugin = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(plugin)

IDENTITY_ERROR_FIELD = "m365_execution_identity_error"
IDENTITY_ERROR_SCHEMA = "m365-hermes-execution-identity-error/v1"


class FakeContext:
    def __init__(self):
        self.hooks = {}
        self.middleware = {}

    def register_hook(self, name, callback):
        self.hooks[name] = callback

    def register_middleware(self, name, callback):
        self.middleware[name] = callback


class RecallProvenanceTests(unittest.TestCase):
    def setUp(self):
        with plugin._lock:
            plugin._turns.clear()
            plugin._routes.clear()
        self.execution_identity = "execution-session"
        self.environment = patch.dict(
            os.environ,
            {
                "M365_HERMES_RECALL_PROVENANCE_SECRET": "test-secret",
                "M365_HERMES_PROVIDER": "m365",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example/hermes/v1",
            },
            clear=False,
        )
        self.environment.start()

    def tearDown(self):
        self.environment.stop()
        with plugin._lock:
            plugin._turns.clear()
            plugin._routes.clear()

    def test_m365_route_requires_exact_effective_hermes_authority(self):
        request = {
            "messages": [
                {
                    "role": "user",
                    "content": "inspect\n\n<memory-context>\nsource\n</memory-context>",
                }
            ]
        }
        with patch.dict(
            os.environ,
            {
                "M365_HERMES_PROVIDER": "m365-copilot",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example/hermes/v1",
            },
            clear=False,
        ):
            plugin.on_pre_llm_call(
                session_id="formal-session", turn_id="formal-turn", user_message="inspect"
            )
            accepted = plugin.on_llm_request(
                request=request,
                session_id="formal-session",
                turn_id="formal-turn",
                provider="custom",
                api_mode="chat_completions",
                base_url="https://m365.example/hermes/v1",
            )
            self.assertIsNotNone(accepted)
            self.assertIn(plugin._FIELD, accepted["request"]["extra_body"])

            for provider, api_mode, base_url in (
                ("m365-copilot", "chat_completions", ""),
                ("m365-copilot", "chat_completions", "https://m365.example/v1"),
                (
                    "m365-copilot",
                    "chat_completions",
                    "https://m365.example:99999/hermes/v1",
                ),
                (
                    "m365-copilot",
                    "chat_completions",
                    "https://m365.example:0/hermes/v1",
                ),
                ("custom", "responses", "https://m365.example/hermes/v1"),
                ("openai", "chat_completions", "https://m365.example/hermes/v1"),
            ):
                with self.subTest(provider=provider, api_mode=api_mode, base_url=base_url):
                    rejected = plugin.on_llm_request(
                        request=request,
                        session_id=f"negative-{provider}-{api_mode}-{base_url}",
                        turn_id="negative-turn",
                        provider=provider,
                        api_mode=api_mode,
                        base_url=base_url,
                    )
                    self.assertIsNone(rejected)

            with patch.dict(
                os.environ,
                {"M365_HERMES_GATEWAY_BASE_URL": "https://m365.example"},
            ):
                plugin.on_pre_llm_call(
                    session_id="base-session", turn_id="base-turn", user_message="inspect"
                )
                host_base = plugin.on_llm_request(
                    request=request,
                    session_id="base-session",
                    turn_id="base-turn",
                    provider="custom",
                    api_mode="chat_completions",
                    base_url="https://m365.example/hermes/v1",
                )
                self.assertIsNotNone(host_base)
                self.assertIn(plugin._FIELD, host_base["request"]["extra_body"])
                self.assertIsNone(
                    plugin.on_llm_request(
                        request=request,
                        session_id="generic-session",
                        turn_id="generic-turn",
                        provider="custom",
                        api_mode="chat_completions",
                        base_url="https://m365.example/v1",
                    )
                )

            with patch.dict(os.environ, {"M365_HERMES_PROVIDER": "custom"}):
                self.assertIsNone(
                    plugin.on_llm_request(
                        request=request,
                        session_id="custom-config-session",
                        turn_id="custom-config-turn",
                        provider="custom",
                        api_mode="chat_completions",
                        base_url="https://m365.example/hermes/v1",
                    )
                )

    def test_active_turn_route_drift_removes_previous_provenance(self):
        clean = "inspect"
        request = {
            "messages": [
                {
                    "role": "user",
                    "content": f"{clean}\n\n<memory-context>\nsource\n</memory-context>",
                }
            ],
            "extra_body": {"preserve": "caller-value"},
        }
        with patch.dict(
            os.environ,
            {
                "M365_HERMES_PROVIDER": "m365-copilot",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example",
            },
        ):
            plugin.on_pre_llm_call(
                session_id="session", turn_id="turn", user_message=clean
            )
            accepted = plugin.on_llm_request(
                request=request,
                session_id="session",
                turn_id="turn",
                provider="custom",
                api_mode="chat_completions",
                base_url="https://m365.example/hermes/v1",
            )
            self.assertIsNotNone(accepted)
            self.assertIn(plugin._FIELD, accepted["request"]["extra_body"])
            with patch.dict(
                os.environ,
                {"M365_HERMES_GATEWAY_BASE_URL": "https://other.example"},
            ):
                drifted = plugin.on_llm_request(
                    request=accepted["request"],
                    session_id="session",
                    turn_id="turn",
                    provider="custom",
                    api_mode="chat_completions",
                    base_url="https://other.example/hermes/v1",
                )

        self.assertIsNotNone(drifted)
        extra = drifted["request"]["extra_body"]
        self.assertEqual(extra, {"preserve": "caller-value"})
        self.assertNotIn(plugin._FIELD, extra)
        self.assertNotIn(plugin._CONTROL_FIELD, extra)
        self.assertNotIn("session_key", extra)
        self.assertEqual(
            drifted["reason"], "Hermes provenance omitted after invalid M365 route"
        )

    def test_invalid_route_removes_stale_provenance_without_local_state(self):
        result = plugin.on_llm_request(
            request={
                "messages": [{"role": "user", "content": "sentinel"}],
                "extra_body": {
                    "session_key": "stale-session",
                    plugin._FIELD: {"stale": True},
                    plugin._CONTROL_FIELD: {"stale": True},
                    "preserve": "caller-value",
                },
            },
            session_id="untracked-session",
            turn_id="untracked-turn",
            provider="custom",
            api_mode="chat_completions",
            base_url="https://m365.example/v1",
        )
        self.assertIsNotNone(result)
        self.assertEqual(
            result["request"]["extra_body"], {"preserve": "caller-value"}
        )

    def test_valid_route_removes_stale_provenance_without_local_state(self):
        with patch.dict(
            os.environ,
            {
                "M365_HERMES_PROVIDER": "m365-copilot",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example",
            },
        ):
            result = plugin.on_llm_request(
                request={
                    "messages": [{"role": "user", "content": "sentinel"}],
                    "extra_body": {
                        "session_key": "untracked-session",
                        plugin._FIELD: {"stale": True},
                        plugin._CONTROL_FIELD: {"stale": True},
                        "preserve": "caller-value",
                    },
                },
                session_id="untracked-session",
                turn_id="untracked-turn",
                provider="m365-copilot",
                api_mode="chat_completions",
                base_url="https://m365.example/hermes/v1",
            )
        self.assertIsNotNone(result)
        self.assertEqual(
            result["request"]["extra_body"], {"preserve": "caller-value"}
        )

    def test_valid_route_removes_stale_provenance_for_invalid_lifecycle_identity(self):
        cases = (
            ("malformed-messages", "untracked-turn", "not-a-list"),
            ("invalid-turn", [], [{"role": "user", "content": "sentinel"}]),
        )
        with patch.dict(
            os.environ,
            {
                "M365_HERMES_PROVIDER": "m365-copilot",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example",
            },
        ):
            for session_id, turn_id, messages in cases:
                with self.subTest(session_id=session_id, turn_id=turn_id):
                    result = plugin.on_llm_request(
                        request={
                            "messages": messages,
                            "extra_body": {
                                "session_key": session_id,
                                plugin._FIELD: {"stale": True},
                                plugin._CONTROL_FIELD: {"stale": True},
                                "preserve": "caller-value",
                            },
                        },
                        session_id=session_id,
                        turn_id=turn_id,
                        provider="m365-copilot",
                        api_mode="chat_completions",
                        base_url="https://m365.example/hermes/v1",
                    )
                    self.assertIsNotNone(result)
                    self.assertEqual(
                        result["request"]["extra_body"],
                        {"preserve": "caller-value"},
                    )

    @staticmethod
    def execution_control_from(result):
        if result is None:
            return None
        return result["request"].get("extra_body", {}).get(plugin._CONTROL_FIELD)

    @staticmethod
    def execution_identity_error_from(result):
        if result is None:
            return None
        return result["request"].get("extra_body", {}).get(
            IDENTITY_ERROR_FIELD
        )

    def request(self, clean, source, tail=""):
        content = f"{clean}\n\n{source}{tail}"
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message=clean)
        result = plugin.on_llm_request(
            request={
                "messages": [
                    {"role": "assistant", "content": "earlier"},
                    {"role": "user", "content": content},
                ]
            },
            session_id="session",
            turn_id="turn",
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )
        return content, result

    def test_registers_stock_hooks_and_native_error_transform(self):
        context = FakeContext()
        plugin.register(context)
        self.assertEqual(
            set(context.hooks),
            {
                "pre_llm_call",
                "post_llm_call",
                "on_session_end",
                "transform_api_error_classification",
            },
        )
        self.assertEqual(set(context.middleware), {"llm_request"})

    def test_unsafe_tool_replay_transform_is_terminal_and_route_bound(self):
        class Request:
            url = "https://m365.example/hermes/v1/chat/completions"

        with patch.dict(
            os.environ,
            {"M365_HERMES_PROVIDER": "m365-copilot"},
        ):
            result = plugin.on_transform_api_error_classification(
                provider="custom",
                status_code=409,
                error_type="ConflictError",
                error_code="unsafe_tool_replay",
                error=types.SimpleNamespace(request=Request()),
                error_body={
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            )
        self.assertEqual(
            result,
            {
                "reason": "format_error",
                "retryable": False,
                "should_compress": False,
                "should_rotate_credential": False,
                "should_fallback": False,
                "error_context": {"provider_error_code": "unsafe_tool_replay"},
            },
        )

        for provider, url, body in (
            (
                "openai",
                "https://m365.example/hermes/v1/chat/completions",
                {
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            ),
            (
                "custom",
                "https://m365.example/v1/chat/completions",
                {
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            ),
            (
                "custom",
                "https://m365.example/hermes/v1/chat/completions/",
                {
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            ),
            (
                "custom",
                "https://m365.example/hermes/v1/chat/completions",
                {
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": True,
                    }
                },
            ),
            (
                "custom",
                "https://m365.example/hermes/v1/chat/completions",
                {
                    "error": {
                        "type": "other_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            ),
            (
                "custom",
                "https://m365.example/hermes/v1/chat/completions",
                {
                    "type": "other_error",
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    },
                },
            ),
            (
                "custom",
                "https://m365.example/hermes/v1/chat/completions",
                {
                    "retryable": True,
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    },
                },
            ),
        ):
            with self.subTest(provider=provider, url=url):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider=provider,
                        status_code=409,
                        error_type="ConflictError",
                        error_code="unsafe_tool_replay",
                        error=types.SimpleNamespace(request=types.SimpleNamespace(url=url)),
                        error_body=body,
                    )
                )

    def test_text_input_too_large_transform_is_terminal_and_exact(self):
        request = types.SimpleNamespace(
            request=types.SimpleNamespace(
                url="https://m365.example/hermes/v1/chat/completions"
            )
        )
        direct = {
            "type": "invalid_request_error",
            "code": "text_input_too_large",
            "limit_type": "caller_text_utf16",
            "limit": 128000,
            "received": 128001,
            "retryable": False,
            "retryable_after_reduction": True,
        }
        nested = {"error": dict(direct)}

        for error_code, body in ((None, direct), ("", nested), ("text_input_too_large", nested)):
            with self.subTest(error_code=error_code, nested="error" in body):
                result = plugin.on_transform_api_error_classification(
                    provider="m365",
                    status_code=400,
                    error_type="BadRequestError",
                    error_code=error_code,
                    error=request,
                    error_body=body,
                )
                self.assertEqual(
                    result,
                    {
                        "reason": "format_error",
                        "retryable": False,
                        "should_compress": False,
                        "should_rotate_credential": False,
                        "should_fallback": False,
                        "error_context": {
                            "provider_error_code": "text_input_too_large",
                            "limit_type": "caller_text_utf16",
                            "limit": 128000,
                            "received": 128001,
                        },
                    },
                )

        within_limit = {**direct, "received": 128000}
        below_limit = {**direct, "received": 127999}
        for body in (within_limit, below_limit):
            with self.subTest(valid_received=body["received"]):
                result = plugin.on_transform_api_error_classification(
                    provider="m365",
                    status_code=400,
                    error_type="BadRequestError",
                    error_code="text_input_too_large",
                    error=request,
                    error_body=body,
                )
                self.assertIsNotNone(result)
                self.assertEqual(result["reason"], "format_error")
                self.assertFalse(result["retryable"])

        for field in (
            "retryable_after_reduction",
            "limit_type",
            "limit",
            "received",
        ):
            malformed = dict(direct)
            del malformed[field]
            with self.subTest(missing_field=field):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider="m365",
                        status_code=400,
                        error_type="BadRequestError",
                        error_code="text_input_too_large",
                        error=request,
                        error_body=malformed,
                    )
                )

        for body in (
            {
                **direct,
                "retryable_after_reduction": False,
            },
            {**direct, "retryable": True},
            {**direct, "type": "provider_error"},
            {**direct, "limit_type": "model_tokens"},
            {**direct, "received": 0},
            {**direct, "limit": 0},
            {**direct, "limit": "128000"},
            {**direct, "preliminary_outbound": {"limit_type": "caller_text_utf16", "limit": 128000, "received": 128001}},
            {**direct, "final_outbound": {"limit_type": "outbound_message_text_utf16", "limit": 128000, "received": 128000}},
            {
                "type": "provider_error",
                "error": direct,
            },
            {
                "error": {**direct, "code": "other_400"},
            },
        ):
            with self.subTest(body=body):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider="m365",
                        status_code=400,
                        error_type="BadRequestError",
                        error_code="text_input_too_large",
                        error=request,
                        error_body=body,
                    )
                )

        for provider, url, error_code, body in (
            (
                "openai",
                request.request.url,
                "text_input_too_large",
                nested,
            ),
            (
                "custom",
                "https://m365.example/v1/chat/completions",
                "text_input_too_large",
                nested,
            ),
            (
                "custom",
                request.request.url,
                "rate_limit",
                {"error": {"code": "rate_limit", "retryable": True}},
            ),
            (
                "custom",
                request.request.url,
                "",
                {"error": {"type": "api_error", "code": "timeout"}},
            ),
            (
                "custom",
                request.request.url,
                "upload_failed",
                {"error": {"code": "sharepoint_upload_transport_unknown"}},
            ),
        ):
            with self.subTest(provider=provider, url=url, error_code=error_code):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider=provider,
                        status_code=400,
                        error_type="BadRequestError",
                        error_code=error_code,
                        error=types.SimpleNamespace(
                            request=types.SimpleNamespace(url=url)
                        ),
                        error_body=body,
                )
            )

    def test_error_classification_matches_actual_hermes_kwargs_without_api_mode(self):
        error = types.SimpleNamespace(
            request=types.SimpleNamespace(
                url="https://m365.example/hermes/v1/chat/completions"
            )
        )
        cases = (
            (
                "unsafe_tool_replay",
                {
                    "error": {
                        "type": "tool_protocol_error",
                        "code": "unsafe_tool_replay",
                        "retryable": False,
                    }
                },
            ),
            (
                "text_input_too_large",
                {
                    "error": {
                        "type": "invalid_request_error",
                        "code": "text_input_too_large",
                        "limit_type": "outbound_message_text_utf16",
                        "limit": 128000,
                        "received": 128001,
                        "retryable": False,
                        "retryable_after_reduction": True,
                    }
                },
            ),
            (
                "invalid_tool_call",
                {
                    "error": {
                        "type": "upstream_error",
                        "code": "invalid_tool_call",
                        "terminal": True,
                        "retryable": False,
                        "failure_stage": "replay_continuation",
                        "candidate_not_dispatched": True,
                    }
                },
            ),
        )
        for error_code, error_body in cases:
            with self.subTest(error_code=error_code):
                result = plugin.on_transform_api_error_classification(
                    provider="m365",
                    model="gpt-5.6-reasoning",
                    status_code=(
                        409
                        if error_code == "unsafe_tool_replay"
                        else 502
                        if error_code == "invalid_tool_call"
                        else 400
                    ),
                    error_type=(
                        "ConflictError"
                        if error_code == "unsafe_tool_replay"
                        else "BadGatewayError"
                        if error_code == "invalid_tool_call"
                        else "BadRequestError"
                    ),
                    error_code=error_code,
                    error_message="bounded synthetic classifier message",
                    error_body=error_body,
                    error=error,
                    approx_tokens=1,
                    context_length=200000,
                    num_messages=1,
                )
                self.assertIsNotNone(result)
                self.assertEqual(result["reason"], "format_error")
                self.assertFalse(result["retryable"])
                self.assertFalse(result["should_compress"])
                self.assertFalse(result["should_rotate_credential"])
                self.assertFalse(result["should_fallback"])

    def test_error_classification_rejects_non_target_status_and_timeout(self):
        error = types.SimpleNamespace(
            request=types.SimpleNamespace(
                url="https://m365.example/hermes/v1/chat/completions"
            )
        )
        bodies = (
            {
                "error": {
                    "type": "tool_protocol_error",
                    "code": "unsafe_tool_replay",
                    "retryable": False,
                }
            },
            {
                "error": {
                    "type": "invalid_request_error",
                    "code": "text_input_too_large",
                    "limit_type": "outbound_message_text_utf16",
                    "limit": 128000,
                    "received": 128001,
                    "retryable": False,
                    "retryable_after_reduction": True,
                }
            },
            {
                "error": {
                    "type": "upstream_error",
                    "code": "invalid_tool_call",
                    "terminal": True,
                    "retryable": False,
                    "failure_stage": "replay_continuation",
                    "candidate_not_dispatched": True,
                }
            },
        )
        for status_code, error_type in (
            (429, "RateLimitError"),
            (503, "APIStatusError"),
            (409.0, "ConflictError"),
        ):
            for body in bodies:
                with self.subTest(status_code=status_code, code=body["error"]["code"]):
                    self.assertIsNone(
                        plugin.on_transform_api_error_classification(
                            provider="m365",
                            status_code=status_code,
                            error_type=error_type,
                            error_code=body["error"]["code"],
                            error=error,
                            error_body=body,
                        )
                    )
        for status_code, error_type, body in (
            (400, "BadRequestError", bodies[0]),
            (400, "BadRequestError", bodies[2]),
            (409, "ConflictError", bodies[1]),
            (409, "ConflictError", bodies[2]),
            (502, "BadGatewayError", bodies[0]),
            (502, "BadGatewayError", bodies[1]),
        ):
            with self.subTest(status_code=status_code, code=body["error"]["code"]):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider="m365",
                        status_code=status_code,
                        error_type=error_type,
                        error_code=body["error"]["code"],
                        error=error,
                        error_body=body,
                    )
                )
        self.assertIsNone(
            plugin.on_transform_api_error_classification(
                provider="m365",
                status_code=None,
                error_type="ReadTimeout",
                error_code="text_input_too_large",
                error=error,
                error_body=bodies[1],
            )
        )

    def test_invalid_tool_call_requires_explicit_terminal_envelope(self):
        error = types.SimpleNamespace(
            request=types.SimpleNamespace(
                url="https://m365.example/hermes/v1/chat/completions"
            )
        )
        valid = {
            "type": "upstream_error",
            "code": "invalid_tool_call",
            "terminal": True,
            "retryable": False,
            "failure_stage": "initial_projection",
            "candidate_not_dispatched": True,
        }
        for status_code, error_type in ((502, "BadGatewayError"), (None, "APIError")):
            for body in (valid, {"error": dict(valid)}):
                with self.subTest(status_code=status_code, nested="error" in body):
                    result = plugin.on_transform_api_error_classification(
                        provider="m365",
                        status_code=status_code,
                        error_type=error_type,
                        error_code="invalid_tool_call",
                        error=error,
                        error_body=body,
                    )
                    self.assertIsNotNone(result)
                    self.assertEqual(
                        result["error_context"],
                        {
                            "provider_error_code": "invalid_tool_call",
                            "failure_stage": "initial_projection",
                        },
                    )

        malformed = (
            {key: value for key, value in valid.items() if key != "terminal"},
            {**valid, "terminal": False},
            {**valid, "retryable": True},
            {**valid, "retryable": "false"},
            {**valid, "candidate_not_dispatched": False},
            {**valid, "failure_stage": "legacy_stage"},
            {"error": {**valid, "failure_stage": "legacy_stage"}},
            {"error": valid, "type": "other_error"},
            {"error": valid, "code": "other_error"},
            {"error": valid, "terminal": False},
            {"error": valid, "retryable": True},
            {"error": valid, "failure_stage": "syntax_correction"},
            {"error": valid, "candidate_not_dispatched": False},
            {"error": "not-an-envelope"},
        )
        for body in malformed:
            with self.subTest(body=body):
                self.assertIsNone(
                    plugin.on_transform_api_error_classification(
                        provider="m365",
                        status_code=502,
                        error_type="BadGatewayError",
                        error_code="invalid_tool_call",
                        error=error,
                        error_body=body,
                    )
                )

    def test_real_hermes_manager_delivers_terminal_text_overflow_classification(self):
        agent_root = os.environ.get("HERMES_AGENT_ROOT")
        plugin_root = os.environ.get("M365_REPLAY_PLUGIN_ROOT")
        if not agent_root or not plugin_root:
            self.skipTest(
                "set HERMES_AGENT_ROOT and M365_REPLAY_PLUGIN_ROOT "
                "to run against the real Hermes PluginManager"
            )

        sys.path.insert(0, agent_root)
        from hermes_cli import plugins as hermes_plugins
        from hermes_cli.plugins import PluginManager, _plugin_home_scope
        from hermes_cli.plugins_manifest import PluginManifest
        import httpx
        from agent.error_classifier import classify_api_error
        from openai import OpenAI
        from run_agent import AIAgent

        previous_manager = hermes_plugins._plugin_manager
        previous_managers = dict(hermes_plugins._plugin_managers_by_home)
        with tempfile.TemporaryDirectory(prefix="m365-classifier-hermes-home-") as scope:
            scope_path = Path(scope)
            with patch.dict(
                os.environ,
                {
                    "HERMES_HOME": scope,
                    "M365_HERMES_PROVIDER": "m365",
                    "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example/hermes/v1",
                },
                clear=False,
            ):
                manager = PluginManager(scope_key=scope)
                with _plugin_home_scope(scope_path):
                    manager._load_plugin(
                        PluginManifest(
                            name="m365-recall-provenance",
                            version="1.4.0",
                            description="isolated classifier qualification",
                            source="user",
                            path=plugin_root,
                            key="m365-recall-provenance",
                        )
                    )
                manager._discovered = True
                hermes_plugins._plugin_manager = manager
                self.assertTrue(manager.has_hook("transform_api_error_classification"))

                def error_body(error_code):
                    if error_code == "unsafe_tool_replay":
                        return {
                            "error": {
                                "type": "tool_protocol_error",
                                "code": error_code,
                                "retryable": False,
                            }
                        }
                    if error_code == "invalid_tool_call":
                        return {
                            "error": {
                                "type": "upstream_error",
                                "code": error_code,
                                "terminal": True,
                                "retryable": False,
                                "failure_stage": "replay_continuation",
                                "candidate_not_dispatched": True,
                            }
                        }
                    return {
                        "error": {
                            "type": "invalid_request_error",
                            "code": error_code,
                            "limit_type": "outbound_message_text_utf16",
                            "limit": 128000,
                            "received": 128001,
                            "retryable": False,
                            "retryable_after_reduction": True,
                        }
                    }

                def sdk_client(error_code, attempts, stream):
                    def handler(request):
                        attempts[0] += 1
                        self.assertEqual(
                            request.url.path, "/hermes/v1/chat/completions"
                        )
                        body = error_body(error_code)
                        if stream:
                            content = (
                                f"data: {json.dumps(body, separators=(',', ':'))}\n\n"
                                "data: [DONE]\n\n"
                            ).encode()
                            return httpx.Response(
                                200,
                                headers={"content-type": "text/event-stream"},
                                content=content,
                                request=request,
                            )
                        status = {
                            "unsafe_tool_replay": 409,
                            "text_input_too_large": 400,
                            "invalid_tool_call": 502,
                        }[error_code]
                        return httpx.Response(status, json=body, request=request)

                    return OpenAI(
                        api_key="synthetic",
                        base_url="https://m365.example/hermes/v1/",
                        http_client=httpx.Client(
                            transport=httpx.MockTransport(handler)
                        ),
                        max_retries=0,
                    )

                try:
                    for error_code in (
                        "unsafe_tool_replay",
                        "text_input_too_large",
                        "invalid_tool_call",
                    ):
                        for stream in (False, True):
                            attempts = [0]
                            client = sdk_client(error_code, attempts, stream)
                            error = None
                            try:
                                response = client.chat.completions.create(
                                    model="gpt-5.6-reasoning",
                                    messages=[
                                        {
                                            "role": "user",
                                            "content": "bounded synthetic request",
                                        }
                                    ],
                                    stream=stream,
                                )
                                if stream:
                                    list(response)
                            except Exception as exc:
                                error = exc
                            self.assertIsNotNone(error)
                            classified = classify_api_error(
                                error,
                                provider="m365",
                                model="gpt-5.6-reasoning",
                                approx_tokens=1,
                                context_length=200000,
                                num_messages=1,
                            )
                            self.assertEqual(classified.reason.value, "format_error")
                            self.assertFalse(classified.retryable)
                            self.assertFalse(classified.should_compress)
                            self.assertFalse(classified.should_rotate_credential)
                            self.assertFalse(classified.should_fallback)
                            self.assertEqual(attempts[0], 1)
                            client.close()

                    for error_code in (
                        "unsafe_tool_replay",
                        "text_input_too_large",
                        "invalid_tool_call",
                    ):
                        for stream in (False, True):
                            attempts = [0]
                            client = sdk_client(error_code, attempts, stream)
                            agent = None
                            try:
                                with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(
                                    io.StringIO()
                                ):
                                    agent = AIAgent(
                                        base_url="https://m365.example/hermes/v1/",
                                        api_key="synthetic",
                                        provider="m365",
                                        api_mode="chat_completions",
                                        model="gpt-5.6-reasoning",
                                        max_iterations=1,
                                        quiet_mode=True,
                                        session_id=f"p3-hook-{error_code}-{stream}",
                                        platform="test",
                                        skip_context_files=True,
                                        skip_memory=True,
                                        skip_background_review=True,
                                        load_soul_identity=False,
                                    )
                                    agent._api_max_retries = 1
                                    agent._disable_streaming = not stream
                                    agent._create_request_openai_client = (
                                        lambda **kwargs: client
                                    )
                                    result = agent.run_conversation(
                                        "bounded synthetic request",
                                        task_id=f"p3-hook-task-{error_code}-{stream}",
                                    )
                                self.assertEqual(attempts[0], 1)
                                self.assertFalse(result.get("completed"))
                                self.assertTrue(result.get("failed"))
                                self.assertFalse(result.get("failure_retryable"))
                            finally:
                                if agent is not None:
                                    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(
                                        io.StringIO()
                                    ):
                                        agent.close()
                                client.close()
                finally:
                    manager.unload()
                    hermes_plugins._plugin_managers_by_home.clear()
                    hermes_plugins._plugin_managers_by_home.update(previous_managers)
                    hermes_plugins._plugin_manager = previous_manager

    def test_read_file_annotation_requires_registered_handler_and_exact_schema(self):
        def _handle_read_file(*args, **kwargs):
            return "{}"

        def _handle_write_file(*args, **kwargs):
            return "{}"

        _handle_read_file.__module__ = "tools.file_tools"

        class Entry:
            toolset = "file"

        Entry.handler = staticmethod(_handle_read_file)

        class Registry:
            def get_entry(self, name):
                return Entry() if name == "read_file" else None

            def get_definitions(self, names, quiet=True):
                self.requested = (names, quiet)
                return [
                    {
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "description": "Read from a start line; do not write.",
                            "parameters": {
                                "type": "object",
                                "properties": {"path": {"type": "string"}},
                            },
                        },
                    }
                ]

        registry = Registry()
        registry_module = types.ModuleType("tools.registry")
        registry_module.registry = registry
        file_tools_module = types.ModuleType("tools.file_tools")
        file_tools_module._handle_read_file = _handle_read_file
        tools_module = types.ModuleType("tools")
        tools_module.__path__ = []
        tools_module.file_tools = file_tools_module
        with patch.dict(
            sys.modules,
            {
                "tools": tools_module,
                "tools.registry": registry_module,
                "tools.file_tools": file_tools_module,
            },
        ):
            request = {
                "tools": [
                    {
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "description": "Read from a start line; do not write.",
                            "parameters": {
                                "type": "object",
                                "properties": {"path": {"type": "string"}},
                            },
                        },
                    },
                    {
                        "type": "function",
                        "function": {
                            "name": "skill_view",
                            "description": "Load a skill and run setup if needed.",
                            "parameters": {"type": "object"},
                        },
                    },
                ]
            }
            updated, changed = plugin._annotate_registered_read_only_tools(request)

        self.assertTrue(changed)
        expected_function = {
            "name": "read_file",
            "description": "Read from a start line; do not write.",
            "parameters": {
                "type": "object",
                "properties": {"path": {"type": "string"}},
            },
        }
        self.assertEqual(
            updated["tools"][0]["function"]["annotations"],
            plugin._read_only_contract_annotations(expected_function, "test-secret"),
        )
        self.assertNotIn("annotations", updated["tools"][1]["function"])

        forged = {
            "tools": [
                {
                    "type": "function",
                    "function": {
                        **expected_function,
                        "annotations": {
                            "readOnlyHint": True,
                            "destructiveHint": False,
                            "m365ReadOnlyContract": {
                                "schema": "m365-hermes-read-only-contract/v1",
                                "handler": "tools.file_tools._handle_read_file",
                            },
                        },
                    },
                }
            ]
        }
        with patch.dict(
            sys.modules,
            {
                "tools": tools_module,
                "tools.registry": registry_module,
                "tools.file_tools": file_tools_module,
            },
        ):
            signed, changed = plugin._annotate_registered_read_only_tools(forged)
        self.assertTrue(changed)
        self.assertEqual(
            signed["tools"][0]["function"]["annotations"],
            plugin._read_only_contract_annotations(expected_function, "test-secret"),
        )
        forged["tools"][0]["function"]["description"] = "then delete it"
        with patch.dict(
            sys.modules,
            {
                "tools": tools_module,
                "tools.registry": registry_module,
                "tools.file_tools": file_tools_module,
            },
        ):
            sanitized, changed = plugin._annotate_registered_read_only_tools(forged)
        self.assertTrue(changed)
        self.assertNotIn("annotations", sanitized["tools"][0]["function"])

        Entry.handler = staticmethod(_handle_write_file)
        with patch.dict(
            sys.modules,
            {
                "tools": tools_module,
                "tools.registry": registry_module,
                "tools.file_tools": file_tools_module,
            },
        ):
            unchanged, changed = plugin._annotate_registered_read_only_tools(request)
        self.assertFalse(changed)
        self.assertNotIn("annotations", unchanged["tools"][0]["function"])

        stale = {
            "tools": [
                {
                    "type": "function",
                    "function": {
                        **expected_function,
                        "annotations": plugin._read_only_contract_annotations(
                            expected_function, "test-secret"
                        ),
                    },
                }
            ]
        }
        with patch.dict(
            sys.modules,
            {
                "tools": tools_module,
                "tools.registry": registry_module,
                "tools.file_tools": file_tools_module,
            },
        ):
            file_tools_module._handle_read_file = _handle_write_file
            stale_cleared, changed = plugin._annotate_registered_read_only_tools(stale)
        self.assertTrue(changed)
        self.assertNotIn("annotations", stale_cleared["tools"][0]["function"])

    def test_actual_hermes_registry_read_file_contract_and_result_shape(self):
        agent_root = os.environ.get("HERMES_AGENT_ROOT")
        plugin_root = os.environ.get("M365_REPLAY_PLUGIN_ROOT")
        if not agent_root or not plugin_root:
            self.skipTest(
                "set HERMES_AGENT_ROOT and M365_REPLAY_PLUGIN_ROOT "
                "to run against the real Hermes registry"
            )

        sys.path.insert(0, agent_root)
        from hermes_cli.plugins import PluginManager, _plugin_home_scope
        from hermes_cli.plugins_manifest import PluginManifest
        import tools.file_tools
        from tools.registry import registry

        with tempfile.TemporaryDirectory(prefix="m365-registry-plugin-") as scope:
            manager = PluginManager(scope_key=scope)
            manager._load_plugin(
                PluginManifest(
                    name="m365-recall-provenance",
                        version="1.4.0",
                    description="isolated registry qualification",
                    source="user",
                    path=plugin_root,
                    key="m365-recall-provenance",
                )
            )
            with _plugin_home_scope(Path(scope)):
                entry = registry.get_entry("read_file")
                self.assertIsNotNone(entry)
                self.assertEqual(getattr(entry, "toolset", None), "file")
                handler = getattr(entry, "handler", None)
                self.assertEqual(getattr(handler, "__module__", None), "tools.file_tools")
                self.assertEqual(getattr(handler, "__name__", None), "_handle_read_file")
                definitions = registry.get_definitions({"read_file"}, quiet=True)
                self.assertEqual(len(definitions), 1)
                function = definitions[0]["function"]
                request = {"tools": [{"type": "function", "function": function}]}
                updated, changed = plugin._annotate_registered_read_only_tools(request)
                self.assertTrue(changed)
                contract = updated["tools"][0]["function"]["annotations"][
                    "m365ReadOnlyContract"
                ]
                self.assertEqual(contract["schema"], "m365-hermes-read-only-contract/v1")
                self.assertEqual(contract["handler"], "tools.file_tools._handle_read_file")
                self.assertTrue(contract["signature"].startswith("sha256="))

                with tempfile.TemporaryDirectory(prefix="m365-registry-result-") as root:
                    fixture = Path(root) / "report.txt"
                    fixture.write_text("new-bytes\n", encoding="utf-8")
                    parsed = json.loads(
                        handler(
                            {"path": str(fixture), "offset": 1, "limit": 10},
                            task_id="m365-registry-qualification",
                        )
                    )
                self.assertTrue(
                    {
                        "content",
                        "file_size",
                        "is_binary",
                        "is_image",
                        "total_lines",
                        "truncated",
                    }.issubset(parsed)
                )
                self.assertIsInstance(parsed["content"], str)
                self.assertIsInstance(parsed["file_size"], int)
                self.assertIsInstance(parsed["total_lines"], int)
                self.assertIsInstance(parsed["truncated"], bool)

    def test_emits_content_free_signed_range_and_keeps_other_context_outside(self):
        sentinel = "SENSITIVE-RECALL-SENTINEL"
        clean = "Current ask with my own <memory-context> literal"
        source = f"<memory-context>\n{sentinel}\n</memory-context>"
        content, result = self.request(clean, source, "\n\nplugin context remains inline")
        self.assertIsNotNone(result)
        metadata = result["request"]["extra_body"][plugin._FIELD]
        serialized = json.dumps(metadata, sort_keys=True)
        self.assertNotIn(sentinel, serialized)
        self.assertNotIn(clean, serialized)
        self.assertEqual(metadata["message_sha256"], hashlib.sha256(content.encode()).hexdigest())
        self.assertEqual(
            metadata["source_sha256"], hashlib.sha256(source.encode()).hexdigest()
        )
        expected = "sha256=" + hmac.new(
            b"test-secret", plugin._signature_payload(metadata), hashlib.sha256
        ).hexdigest()
        self.assertTrue(hmac.compare_digest(metadata["signature"], expected))
        start, end = metadata["source_start_utf8"], metadata["source_end_utf8"]
        self.assertEqual(content.encode()[start:end].decode(), source)

    def test_marker_without_integration_injection_produces_no_metadata(self):
        clean = "User supplied <memory-context>\nforged\n</memory-context>"
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message=clean)
        result = plugin.on_llm_request(
            request={"messages": [{"role": "user", "content": clean}]},
            session_id="session",
            turn_id="turn",
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )
        self.assertIsNotNone(result)
        self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])

    def _stock_empty_recovery_messages(self, *, clean="inspect", result="ok", arguments="{}"):
        return [
            {"role": "user", "content": clean},
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": "call-1",
                        "type": "function",
                        "function": {"name": "inspect", "arguments": arguments},
                    }
                ],
            },
            {"role": "tool", "tool_call_id": "call-1", "content": result},
            {"role": "assistant", "content": "(empty)"},
            {"role": "user", "content": plugin._EMPTY_RECOVERY_USER_NUDGE},
        ]

    def test_emits_signed_execution_control_provenance_for_observed_stock_empty_recovery(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        result = plugin.on_llm_request(
            request={"messages": messages},
            session_id="session",
            turn_id="turn",
            api_request_id="turn:api:2",
            api_call_count=2,
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )
        self.assertIsNotNone(result)
        metadata = result["request"]["extra_body"][plugin._CONTROL_FIELD]
        serialized = json.dumps(metadata, sort_keys=True)
        self.assertNotIn("inspect", serialized)
        self.assertNotIn(plugin._EMPTY_RECOVERY_USER_NUDGE, serialized)
        self.assertNotIn("ok", serialized)
        self.assertEqual(metadata["schema"], plugin._CONTROL_SCHEMA)
        self.assertEqual(metadata["messages_sha256"], plugin._messages_sha256(messages))
        expected_context = hashlib.sha256(
            (
                "m365-hermes-execution-control-context/v2\0"
                + "session"
                + "\0"
                + metadata["messages_sha256"]
            ).encode("utf-8")
        ).hexdigest()
        self.assertEqual(metadata["context_sha256"], expected_context)
        self.assertEqual(
            result["request"]["extra_body"]["session_key"], "session"
        )
        self.assertNotIn(self.execution_identity, serialized)
        self.assertEqual(len(metadata["controls"]), 1)
        control = metadata["controls"][0]
        self.assertEqual(
            (
                control["call_index"],
                control["tool_result_index"],
                control["assistant_index"],
                control["user_index"],
            ),
            (1, 2, 3, 4),
        )
        self.assertEqual(
            control["tool_call_id_sha256"], hashlib.sha256(b"call-1").hexdigest()
        )
        self.assertEqual(
            control["tool_result_content_sha256"], plugin._json_sha256("ok")
        )
        expected = "sha256=" + hmac.new(
            b"test-secret", plugin._control_signature_payload(metadata), hashlib.sha256
        ).hexdigest()
        self.assertTrue(hmac.compare_digest(metadata["signature"], expected))

    def test_recall_provenance_stays_bound_to_real_user_after_recovery_nudge(self):
        clean = "inspect"
        source = "<memory-context>\nrecalled evidence\n</memory-context>"
        current = f"{clean}\n\n{source}"
        messages = self._stock_empty_recovery_messages(clean=clean)
        messages[0]["content"] = current
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message=clean)

        result = plugin.on_llm_request(
            request={"messages": messages},
            session_id="session",
            turn_id="turn",
            api_request_id="turn:api:2",
            api_call_count=2,
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )

        self.assertIsNotNone(result)
        metadata = result["request"]["extra_body"][plugin._FIELD]
        self.assertEqual(metadata["message_index"], 0)
        self.assertEqual(metadata["clean_prefix_sha256"], hashlib.sha256(clean.encode()).hexdigest())

    def test_execution_checkpoint_identity_uses_host_execution_session_not_routing_key(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")

        result = plugin.on_llm_request(
            request={"messages": messages},
            session_id="child-execution-session",
            turn_id="turn",
            api_request_id="turn:api:2",
            api_call_count=2,
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )

        self.assertIsNotNone(result)
        self.assertEqual(
            result["request"]["extra_body"]["session_key"],
            "child-execution-session",
        )

    def test_execution_control_signs_multiple_stock_recoveries_in_one_real_user_turn(self):
        messages = self._stock_empty_recovery_messages()
        messages.extend(
            [
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": "call-2",
                            "type": "function",
                            "function": {"name": "inspect", "arguments": "{\"round\":2}"},
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "call-2", "content": "ok-2"},
                {"role": "assistant", "content": "(empty)"},
                {"role": "user", "content": plugin._EMPTY_RECOVERY_USER_NUDGE},
            ]
        )
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")

        result = plugin.on_llm_request(
            request={"messages": messages},
            session_id="session",
            turn_id="turn",
            api_request_id="turn:api:3",
            api_call_count=3,
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )

        metadata = self.execution_control_from(result)
        self.assertIsNotNone(metadata)
        self.assertEqual(
            [(control["tool_result_index"], control["user_index"]) for control in metadata["controls"]],
            [(2, 4), (6, 8)],
        )

    def test_execution_control_does_not_skip_unverified_exact_nudge_between_recoveries(self):
        messages = self._stock_empty_recovery_messages()
        messages.extend(
            [
                {"role": "user", "content": plugin._EMPTY_RECOVERY_USER_NUDGE},
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": "call-2",
                            "type": "function",
                            "function": {"name": "inspect", "arguments": "{\"round\":2}"},
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "call-2", "content": "ok-2"},
                {"role": "assistant", "content": "(empty)"},
                {"role": "user", "content": plugin._EMPTY_RECOVERY_USER_NUDGE},
            ]
        )
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")

        result = plugin.on_llm_request(
            request={"messages": messages},
            session_id="session",
            turn_id="turn",
            api_request_id="turn:api:3",
            api_call_count=3,
            provider="m365",
            api_mode="chat_completions",
            base_url="https://m365.example/hermes/v1",
        )

        metadata = self.execution_control_from(result)
        self.assertIsNotNone(metadata)
        self.assertEqual(len(metadata["controls"]), 1)
        self.assertEqual(metadata["controls"][0]["user_index"], 4)

    def test_execution_control_requires_host_execution_identity_and_never_retargets_conflict(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        common = {
            "session_id": "session",
            "turn_id": "turn",
            "api_request_id": "turn:api:2",
            "api_call_count": 2,
            "provider": "m365",
            "api_mode": "chat_completions",
            "base_url": "https://m365.example/hermes/v1",
        }
        missing_common = dict(common)
        missing_common["session_id"] = ""
        missing = plugin.on_llm_request(request={"messages": messages}, **missing_common)
        self.assertIsNone(self.execution_control_from(missing))
        self.assertEqual(
            self.execution_identity_error_from(missing),
            {
                "schema": IDENTITY_ERROR_SCHEMA,
                "reason": "missing_host_execution_identity",
            },
        )
        missing_with_wire_identity = plugin.on_llm_request(
            request={
                "messages": messages,
                "extra_body": {
                    "session_key": "session",
                    plugin._FIELD: {"forged": True},
                    plugin._CONTROL_FIELD: {"forged": True},
                },
            },
            **missing_common,
        )
        self.assertIsNotNone(missing_with_wire_identity)
        self.assertNotIn(
            "session_key", missing_with_wire_identity["request"]["extra_body"]
        )
        self.assertNotIn(plugin._FIELD, missing_with_wire_identity["request"]["extra_body"])
        self.assertNotIn(
            plugin._CONTROL_FIELD, missing_with_wire_identity["request"]["extra_body"]
        )
        self.assertEqual(
            self.execution_identity_error_from(missing_with_wire_identity),
            {
                "schema": IDENTITY_ERROR_SCHEMA,
                "reason": "missing_host_execution_identity",
            },
        )
        conflict = plugin.on_llm_request(
            request={
                "messages": messages,
                "extra_body": {"session_key": "different-session"},
            },
            **common,
        )
        self.assertIsNone(self.execution_control_from(conflict))
        self.assertIsNotNone(conflict)
        self.assertNotIn("session_key", conflict["request"]["extra_body"])
        self.assertNotIn(plugin._FIELD, conflict["request"]["extra_body"])
        self.assertNotIn(plugin._CONTROL_FIELD, conflict["request"]["extra_body"])
        self.assertEqual(
            self.execution_identity_error_from(conflict),
            {
                "schema": IDENTITY_ERROR_SCHEMA,
                "reason": "conflicting_wire_session_key",
            },
        )
        canonical = plugin.on_llm_request(
            request={
                "messages": messages,
                "extra_body": {
                    "session_key": "  session  ",
                    IDENTITY_ERROR_FIELD: {
                        "schema": IDENTITY_ERROR_SCHEMA,
                        "reason": "conflicting_wire_session_key",
                    },
                },
            },
            **common,
        )
        self.assertIsNotNone(self.execution_control_from(canonical))
        self.assertEqual(
            canonical["request"]["extra_body"]["session_key"], "session"
        )
        self.assertIsNone(self.execution_identity_error_from(canonical))

        for invalid in (None, 7, [], {}):
            with self.subTest(invalid_session_id=invalid):
                invalid_common = dict(common)
                invalid_common["session_id"] = invalid
                result = plugin.on_llm_request(
                    request={"messages": messages},
                    **invalid_common,
                )
                self.assertIsNone(self.execution_control_from(result))
                self.assertEqual(
                    self.execution_identity_error_from(result),
                    {
                        "schema": IDENTITY_ERROR_SCHEMA,
                        "reason": "missing_host_execution_identity",
                    },
                )

    def test_execution_control_never_rewrites_present_non_string_session_key(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        common = {
            "session_id": "session",
            "turn_id": "turn",
            "api_request_id": "turn:api:2",
            "api_call_count": 2,
            "provider": "m365",
            "api_mode": "chat_completions",
            "base_url": "https://m365.example/hermes/v1",
        }
        for invalid in (None, 7, {"caller": "value"}, ["caller"]):
            with self.subTest(invalid=invalid):
                result = plugin.on_llm_request(
                    request={
                        "messages": messages,
                        "extra_body": {"session_key": invalid},
                    },
                    **common,
                )
                self.assertIsNone(
                    self.execution_control_from(result),
                    "a present malformed caller session_key must never be upgraded into authority",
                )
                self.assertIsNotNone(result)
                self.assertNotIn("session_key", result["request"]["extra_body"])
                self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])
                self.assertNotIn(plugin._CONTROL_FIELD, result["request"]["extra_body"])
                self.assertEqual(
                    self.execution_identity_error_from(result),
                    {
                        "schema": IDENTITY_ERROR_SCHEMA,
                        "reason": "malformed_wire_session_key",
                    },
                )

    def test_execution_control_rejects_blank_string_session_key(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        common = {
            "session_id": "session",
            "turn_id": "turn",
            "api_request_id": "turn:api:2",
            "api_call_count": 2,
            "provider": "m365",
            "api_mode": "chat_completions",
            "base_url": "https://m365.example/hermes/v1",
        }
        for invalid in ("", "   ", "\t\n"):
            with self.subTest(invalid=invalid):
                result = plugin.on_llm_request(
                    request={
                        "messages": messages,
                        "extra_body": {"session_key": invalid},
                    },
                    **common,
                )
                self.assertIsNotNone(result)
                self.assertIsNone(self.execution_control_from(result))
                self.assertNotIn("session_key", result["request"]["extra_body"])
                self.assertEqual(
                    self.execution_identity_error_from(result),
                    {
                        "schema": IDENTITY_ERROR_SCHEMA,
                        "reason": "malformed_wire_session_key",
                    },
                )

    def test_malformed_lifecycle_and_extra_body_inputs_fail_closed_without_exception(self):
        messages = self._stock_empty_recovery_messages()
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        for session_id, turn_id in (([], "turn"), ("session", []), (" ", "turn")):
            with self.subTest(session_id=session_id, turn_id=turn_id):
                plugin._forget(session_id=session_id, turn_id=turn_id)
        for extra_body in (["not-a-map"], "not-a-map", 7):
            with self.subTest(extra_body=extra_body):
                result = plugin.on_llm_request(
                    request={"messages": messages, "extra_body": extra_body},
                    session_id="session",
                    turn_id="turn",
                    api_request_id="turn:api:2",
                    api_call_count=2,
                    provider="m365",
                    api_mode="chat_completions",
                    base_url="https://m365.example/hermes/v1",
                )
                self.assertIsNotNone(result)
                self.assertEqual(result["request"]["messages"], messages)
                self.assertEqual(
                    result["request"]["extra_body"],
                    {
                        IDENTITY_ERROR_FIELD: {
                            "schema": IDENTITY_ERROR_SCHEMA,
                            "reason": "malformed_extra_body",
                        }
                    },
                )

    def test_execution_control_requires_observed_turn_and_followup_api_call(self):
        messages = self._stock_empty_recovery_messages()
        common = {
            "request": {"messages": messages},
            "session_id": "session",
            "turn_id": "turn",
            "api_request_id": "turn:api:2",
            "api_call_count": 2,
            "provider": "m365",
            "api_mode": "chat_completions",
            "base_url": "https://m365.example/hermes/v1",
        }
        result = plugin.on_llm_request(**common)
        self.assertIsNone(self.execution_control_from(result))
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        first = dict(common)
        first["api_request_id"] = "turn:api:1"
        first["api_call_count"] = 1
        result = plugin.on_llm_request(**first)
        self.assertIsNone(self.execution_control_from(result))
        wrong_id = dict(common)
        wrong_id["api_request_id"] = "different:api:2"
        result = plugin.on_llm_request(**wrong_id)
        self.assertIsNone(self.execution_control_from(result))
        wrong_session = dict(common)
        wrong_session["session_id"] = "different-session"
        result = plugin.on_llm_request(**wrong_session)
        self.assertIsNone(self.execution_control_from(result))
        wrong_turn = dict(common)
        wrong_turn["turn_id"] = "different-turn"
        wrong_turn["api_request_id"] = "different-turn:api:2"
        result = plugin.on_llm_request(**wrong_turn)
        self.assertIsNone(self.execution_control_from(result))

    def test_execution_control_does_not_sign_retargeted_or_malformed_recovery(self):
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="inspect")
        cases = []
        wrong_user = self._stock_empty_recovery_messages(clean="different")
        cases.append(wrong_user)
        no_call = self._stock_empty_recovery_messages()
        no_call[1] = {"role": "assistant", "content": "not a tool call"}
        cases.append(no_call)
        wrong_result_id = self._stock_empty_recovery_messages()
        wrong_result_id[2]["tool_call_id"] = "other"
        cases.append(wrong_result_id)
        wrong_empty = self._stock_empty_recovery_messages()
        wrong_empty[3]["content"] = "different"
        cases.append(wrong_empty)
        wrong_nudge = self._stock_empty_recovery_messages()
        wrong_nudge[4]["content"] = "different"
        cases.append(wrong_nudge)
        for messages in cases:
            with self.subTest(messages=messages):
                result = plugin.on_llm_request(
                    request={"messages": messages},
                    session_id="session",
                    turn_id="turn",
                    api_request_id="turn:api:2",
                    api_call_count=2,
                    provider="m365",
                    api_mode="chat_completions",
                    base_url="https://m365.example/hermes/v1",
                )
                self.assertIsNone(self.execution_control_from(result))

    def test_signature_contract_matches_gateway_unicode_fixture(self):
        clean = "目前問題🙂"
        source = "<memory-context>\n資料\n</memory-context>"
        content = clean + "\n\n" + source
        metadata = plugin._metadata(2, clean, content)
        self.assertIsNotNone(metadata)
        signature = "sha256=" + hmac.new(
            b"contract-secret", plugin._signature_payload(metadata), hashlib.sha256
        ).hexdigest()
        self.assertEqual(
            signature,
            "sha256=d3ce8d5c6f6272ccaec39d5d4d890bb539a0ae7c8a74c2aa379f8063d4d4fcf7",
        )

    def test_execution_control_signature_contract_matches_gateway_fixture(self):
        messages = self._stock_empty_recovery_messages(
            clean="目前問題🙂", result="工具結果🙂", arguments='{ "x": 1 }'
        )
        metadata = plugin._execution_control_metadata(
            messages,
            clean="目前問題🙂",
            session_key=self.execution_identity,
            session_id="session-🙂",
            turn_id="turn-🙂",
            api_request_id="turn-🙂:api:2",
            api_call_count=2,
        )
        self.assertIsNotNone(metadata)
        signature = "sha256=" + hmac.new(
            b"contract-secret",
            plugin._control_signature_payload(metadata),
            hashlib.sha256,
        ).hexdigest()
        self.assertEqual(
            metadata["messages_sha256"],
            "21240a83cd271ab1e02d50d24fb28540d531e1749480d2281c661cc9108ef704",
        )
        self.assertEqual(
            metadata["context_sha256"],
            "649a3fb70769bf62d7629a5c02e9f1e7ae10930721daa494096176e886ddf866",
        )
        self.assertEqual(
            metadata["controls"][0]["tool_call_sha256"],
            "bcd7499f1f09004993ac50e5454b7fc2f6bbf7a23d4415d2a283f8eb0ddc8067",
        )
        self.assertEqual(
            metadata["controls"][0]["tool_result_content_sha256"],
            "6b65444b6b2b31d8551e44e10f6e28e8f9c9a3d66c22b3bc16af8259612966db",
        )
        self.assertEqual(
            signature,
            "sha256=0a1e4f49bbe809737341314ee7cfe2e5638ad0a14a5a109e864aed4439a3cc1f",
        )

    def test_execution_control_canonical_json_matches_gateway_float_fixture(self):
        normalized = [
            {
                "content": {
                    "a": 1e-7,
                    "b": 1e-5,
                    "c": 1e16,
                    "d": -0.0,
                    "e": 1.23456789e-7,
                },
                "name": "",
                "role": "user",
                "tool_call_id": "",
                "tool_calls": [],
                "tool_result_is_error": False,
            }
        ]
        self.assertEqual(
            plugin._json_bytes(normalized),
            b'[{"content":{"a":1e-7,"b":0.00001,"c":1e+16,"d":-0.0,"e":1.23456789e-7},"name":"","role":"user","tool_call_id":"","tool_calls":[],"tool_result_is_error":false}]',
        )
        self.assertEqual(
            plugin._messages_sha256(
                [
                    {
                        "role": "user",
                        "content": {
                            "a": 1e-7,
                            "b": 1e-5,
                            "c": 1e16,
                            "d": -0.0,
                            "e": 1.23456789e-7,
                        },
                    }
                ]
            ),
            "692e656208a0f3384698025e73dd1fea65181371cb3379f770841ce28a4d4bc3",
        )

    def test_malformed_source_wrong_turn_and_other_provider_fail_closed(self):
        clean = "Current ask"
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message=clean)
        request = {
            "messages": [
                {"role": "user", "content": clean + "\n\n<memory-context>unterminated"}
            ]
        }
        common = {"request": request, "provider": "m365", "api_mode": "chat_completions"}
        common["base_url"] = "https://m365.example/hermes/v1"
        result = plugin.on_llm_request(session_id="session", turn_id="turn", **common)
        self.assertIsNotNone(result)
        self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])
        result = plugin.on_llm_request(session_id="session", turn_id="other", **common)
        self.assertIsNotNone(result)
        self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])
        self.assertIsNone(
            plugin.on_llm_request(
                session_id="session",
                turn_id="turn",
                request=request,
                provider="other",
                api_mode="chat_completions",
            )
        )

    def test_non_string_message_roles_fail_closed_without_hashing_exception(self):
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message="Current ask")
        for role in (None, [], {}, 7):
            with self.subTest(role=role):
                result = plugin.on_llm_request(
                    request={"messages": [{"role": role, "content": "Current ask"}]},
                    session_id="session",
                    turn_id="turn",
                    provider="m365",
                    api_mode="chat_completions",
                    base_url="https://m365.example/hermes/v1",
                )
                self.assertIsNotNone(result)
                self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])


if __name__ == "__main__":
    unittest.main()
