"""Focused Final Architecture Contract regressions.

The first version of this file was run before convergence and produced the
recorded RED evidence. These same semantic checks remain as green guards for
the converged plugin seam.
"""

from __future__ import annotations

import hashlib
import importlib
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


plugin = importlib.import_module("integrations.hermes.m365_native_attachments")


class FinalContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = patch.dict(
            os.environ,
            {
                "M365_HERMES_PROVIDER": "m365",
                "M365_HERMES_RECALL_PROVENANCE_SECRET": "final-contract-secret",
                "M365_HERMES_GATEWAY_BASE_URL": "https://m365.example/hermes/v1",
                "M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": "",
            },
            clear=False,
        )
        self.environment.start()
        self.turn_route = patch.object(plugin, "_turn_route", return_value=True)
        self.turn_route.start()
        with plugin._lock:
            plugin._sessions.clear()
            plugin._outcomes.clear()
            plugin._ended.clear()
            plugin._pending_ends.clear()

    def tearDown(self) -> None:
        with plugin._lock:
            plugin._sessions.clear()
            plugin._outcomes.clear()
            plugin._ended.clear()
            plugin._pending_ends.clear()
        self.turn_route.stop()
        self.environment.stop()

    @staticmethod
    def route(session: str, turn: str, session_key: str | None = None) -> None:
        plugin._remember_route(
            (session, turn),
            "https://m365.example/hermes/v1",
            {"extra_body": {"session_key": session_key or session}},
        )

    @staticmethod
    def llm(session: str, turn: str, provider: str = "m365") -> dict | None:
        with patch.object(plugin, "_turn_route", return_value=True):
            return plugin.on_llm_request(
                {
                    "messages": [{"role": "user", "content": "sentinel"}],
                    "extra_body": {"session_key": session, "existing": "preserve"},
                },
                session_id=session,
                turn_id=turn,
                provider=provider,
                api_mode="chat_completions",
                base_url="https://m365.example/hermes/v1",
            )

    @staticmethod
    def stage_fake(contents: dict[str, bytes], refs: list[str] | None = None):
        capabilities = iter(refs or [chr(65 + index) * 43 for index in range(8)])

        def stage(opened, _base_url, _session_key, _turn_id):
            data = contents[opened.path.name]
            return {
                "stage_ref": next(capabilities),
                "size": len(data),
                "sha256": hashlib.sha256(data).hexdigest(),
            }

        return stage

    @staticmethod
    def attach(path: Path, session: str, turn: str, **item: str) -> dict:
        return json.loads(
            plugin.m365_native_attach(
                {"files": [{"local_path": str(path), **item}]},
                session_id=session,
                turn_id=turn,
            )
        )

    def test_turn_isolation_failure_recovery_and_replace(self) -> None:
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first = Path(root) / "first.txt"
            second = Path(root) / "second.txt"
            first.write_bytes(b"first")
            second.write_bytes(b"second")
            contents = {"first.txt": b"first", "second.txt": b"second"}
            with patch.object(
                plugin, "_stage_file", side_effect=self.stage_fake(contents)
            ), patch.object(plugin, "_release_route"):
                self.route("session", "turn-a")
                self.assertTrue(self.attach(first, "session", "turn-a")["ok"])
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    self.llm("session", "turn-b")["request"]["extra_body"],
                )

                failed = self.attach(
                    first, "session", "turn-a", expected_sha256="f" * 64
                )
                self.assertEqual(failed["error"]["code"], "hash_mismatch")
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    self.llm("session", "turn-a")["request"]["extra_body"],
                )

                self.route("session", "turn-b")
                self.assertTrue(self.attach(second, "session", "turn-b")["ok"])
                self.route("session", "turn-a")
                self.assertTrue(self.attach(second, "session", "turn-a")["ok"])
                context = self.llm("session", "turn-a")["request"]["extra_body"][
                    plugin._CONTEXT_FIELD
                ]
                self.assertEqual(
                    [reference["original_filename"] for reference in context["attachments"]],
                    ["second.txt"],
                )

    def test_count_replace_and_partial_stage_failure_roll_back(self) -> None:
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first = Path(root) / "first.txt"
            second = Path(root) / "second.txt"
            first.write_bytes(b"a")
            second.write_bytes(b"b")
            contents = {"first.txt": b"a", "second.txt": b"b"}
            released: list[list[str]] = []
            calls = 0

            def stage(opened, *_args):
                nonlocal calls
                calls += 1
                if calls == 2:
                    raise plugin._AttachmentFailure("stage_transport_failed")
                return self.stage_fake(contents)(opened, *_args)

            with patch.object(plugin, "_stage_file", side_effect=stage), patch.object(
                plugin,
                "_release_route",
                side_effect=lambda _base, _session, _turn, refs: released.append(refs),
            ):
                self.route("session", "turn")
                result = json.loads(
                    plugin.m365_native_attach(
                        {
                            "files": [
                                {"local_path": str(first)},
                                {"local_path": str(second)},
                            ]
                        },
                        session_id="session",
                        turn_id="turn",
                    )
                )
                self.assertEqual(result["error"]["code"], "stage_transport_failed")
                self.assertIn(["A" * 43], released)
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    self.llm("session", "turn")["request"]["extra_body"],
                )

            self.route("session", "turn")
            for count in (0, 3):
                result = json.loads(
                    plugin.m365_native_attach(
                        {"files": [{"local_path": str(first)}] * count},
                        session_id="session",
                        turn_id="turn",
                    )
                )
                self.assertEqual(result["error"]["code"], "invalid_attachment_count")

    def test_state_loss_and_callback_failure_are_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "state.txt"
            path.write_bytes(b"state")
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self.stage_fake({"state.txt": b"state"}),
            ), patch.object(plugin, "_release_route"):
                self.route("session", "turn")
                self.assertTrue(self.attach(path, "session", "turn")["ok"])
                with plugin._lock:
                    plugin._sessions.clear()
                result = self.llm("session", "turn")
                context = result["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(context["error"], "native_attachment_state_lost")

                with patch.object(
                    plugin,
                    "_request_with_context",
                    side_effect=RuntimeError("synthetic callback failure"),
                ):
                    with patch.object(plugin, "_turn_route", return_value=True):
                        failed_closed = plugin.on_llm_request(
                            {"messages": [{"role": "user", "content": "keep"}]},
                            session_id="session",
                            turn_id="turn",
                            provider="m365",
                            api_mode="chat_completions",
                            base_url="https://m365.example/hermes/v1",
                        )
                self.assertEqual(
                    failed_closed["request"]["messages"],
                    [{"role": "user", "content": "keep"}],
                )
                self.assertIn(
                    plugin._CONTEXT_FIELD,
                    failed_closed["request"]["extra_body"],
                )

    def test_tool_result_context_and_wire_auth_are_bounded(self) -> None:
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "報告.xlsx"
            path.write_bytes("sentinel-附件".encode())
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self.stage_fake({"報告.xlsx": path.read_bytes()}),
            ), patch.object(plugin, "_release_route"):
                self.route("session-附件", "turn-😀", "session-附件")
                result = self.attach(path, "session-附件", "turn-😀")
                result_text = json.dumps(result, ensure_ascii=False)
                self.assertTrue(result["ok"])
                self.assertIn("turn_binding", result)
                self.assertNotIn("stage_ref", result_text)
                self.assertNotIn(str(path), result_text)
                self.assertNotIn("final-contract-secret", result_text)
                context = self.llm("session-附件", "turn-😀")["request"][
                    "extra_body"
                ][plugin._CONTEXT_FIELD]
                self.assertEqual(context["turn_id"], "turn-😀")
                self.assertEqual(context["signature"], plugin._sign_context(context))

    def test_https_and_single_fd_stream_hash_contract(self) -> None:
        with self.assertRaises(plugin._AttachmentFailure):
            plugin._request_target("http://127.0.0.1:8080/hermes/v1", "stage")

        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "single-pass.txt"
            content = b"single-pass" * 10
            path.write_bytes(content)
            payload = json.dumps(
                {
                    "schema": plugin._STAGE_SCHEMA,
                    "capability": "B" * 43,
                    "size": len(content),
                    "sha256": hashlib.sha256(content).hexdigest(),
                }
            ).encode()

            class Response:
                status = 200

                def read(self, _limit=-1):
                    return payload

            class Connection:
                def __init__(self):
                    self.body = bytearray()
                    self.headers = []

                def putrequest(self, *_args):
                    pass

                def putheader(self, name, value):
                    self.headers.append((name, value))

                def endheaders(self):
                    pass

                def send(self, chunk):
                    self.body.extend(chunk)

                def getresponse(self):
                    return Response()

                def close(self):
                    pass

            connection = Connection()
            with patch.dict(
                os.environ,
                {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root},
            ), patch.object(plugin, "_connection", return_value=connection), patch.object(
                plugin.os, "open", wraps=plugin.os.open
            ) as opened:
                source = plugin._open_allowed_file(str(path))
                plugin._stage_file(
                    source, "https://m365.example/hermes/v1", "session", "turn"
                )
                source.handle.close()
            self.assertEqual(opened.call_count, 1)
            self.assertEqual(bytes(connection.body), content)
            self.assertNotIn(
                "final-contract-secret",
                [value for _name, value in connection.headers],
            )


if __name__ == "__main__":
    unittest.main()
