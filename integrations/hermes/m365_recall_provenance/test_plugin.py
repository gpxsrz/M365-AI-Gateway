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
        self.assertIsNone(result)

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

    def test_malformed_source_wrong_turn_and_other_provider_fail_closed(self):
        clean = "Current ask"
        plugin.on_pre_llm_call(session_id="session", turn_id="turn", user_message=clean)
        request = {
            "messages": [
                {"role": "user", "content": clean + "\n\n<memory-context>unterminated"}
            ]
        }
        common = {"request": request, "provider": "m365", "api_mode": "chat_completions"}
        self.assertIsNone(
            plugin.on_llm_request(session_id="session", turn_id="turn", **common)
        )
        self.assertIsNone(
            plugin.on_llm_request(session_id="session", turn_id="other", **common)
        )
        self.assertIsNone(
            plugin.on_llm_request(
                session_id="session",
                turn_id="turn",
                request=request,
                provider="other",
                api_mode="chat_completions",
            )
        )


if __name__ == "__main__":
    unittest.main()
