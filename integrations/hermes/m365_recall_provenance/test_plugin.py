import hashlib
import hmac
import importlib.util
import json
import os
import unittest
from pathlib import Path
from unittest.mock import patch


PLUGIN_PATH = Path(__file__).with_name("__init__.py")
SPEC = importlib.util.spec_from_file_location("m365_recall_provenance", PLUGIN_PATH)
plugin = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(plugin)


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
        plugin._forget("session", "turn")
        self.execution_identity = "execution-session"
        self.environment = patch.dict(
            os.environ,
            {
                "M365_HERMES_RECALL_PROVENANCE_SECRET": "test-secret",
                "M365_HERMES_PROVIDER": "m365",
            },
            clear=False,
        )
        self.environment.start()

    def tearDown(self):
        self.environment.stop()
        plugin._forget("session", "turn")

    @staticmethod
    def execution_control_from(result):
        if result is None:
            return None
        return result["request"].get("extra_body", {}).get(plugin._CONTROL_FIELD)

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
        )
        return content, result

    def test_registers_only_the_stock_hook_and_middleware_seams(self):
        context = FakeContext()
        plugin.register(context)
        self.assertEqual(
            set(context.hooks), {"pre_llm_call", "post_llm_call", "on_session_end"}
        )
        self.assertEqual(set(context.middleware), {"llm_request"})

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
        }
        missing_common = dict(common)
        missing_common["session_id"] = ""
        missing = plugin.on_llm_request(request={"messages": messages}, **missing_common)
        self.assertIsNone(self.execution_control_from(missing))
        conflict = plugin.on_llm_request(
            request={
                "messages": messages,
                "extra_body": {"session_key": "different-session"},
            },
            **common,
        )
        self.assertIsNone(self.execution_control_from(conflict))
        if conflict is not None:
            self.assertEqual(
                conflict["request"]["extra_body"]["session_key"], "different-session"
            )
            self.assertNotIn(plugin._FIELD, conflict["request"]["extra_body"])
        canonical = plugin.on_llm_request(
            request={
                "messages": messages,
                "extra_body": {"session_key": "  session  "},
            },
            **common,
        )
        self.assertIsNotNone(self.execution_control_from(canonical))
        self.assertEqual(
            canonical["request"]["extra_body"]["session_key"], "session"
        )

        for invalid in (None, 7, [], {}):
            with self.subTest(invalid_session_id=invalid):
                invalid_common = dict(common)
                invalid_common["session_id"] = invalid
                result = plugin.on_llm_request(
                    request={"messages": messages},
                    **invalid_common,
                )
                self.assertIsNone(self.execution_control_from(result))

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
                if result is not None:
                    self.assertEqual(
                        result["request"]["extra_body"]["session_key"], invalid
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
                )
                self.assertIsNone(result)

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
                )
                self.assertIsNotNone(result)
                self.assertNotIn(plugin._FIELD, result["request"]["extra_body"])


if __name__ == "__main__":
    unittest.main()
