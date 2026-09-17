from __future__ import annotations

import hashlib
import importlib.util
import json
import os
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import patch


PLUGIN_PATH = Path(__file__).with_name("__init__.py")
SPEC = importlib.util.spec_from_file_location("m365_native_attachments", PLUGIN_PATH)
plugin = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(plugin)

RECALL_PATH = PLUGIN_PATH.parents[1] / "m365_recall_provenance" / "__init__.py"
RECALL_SPEC = importlib.util.spec_from_file_location(
    "m365_recall_provenance_for_native_attachment_test", RECALL_PATH
)
recall = importlib.util.module_from_spec(RECALL_SPEC)
assert RECALL_SPEC.loader is not None
RECALL_SPEC.loader.exec_module(recall)


class FakeResponse:
    status = 200

    def __init__(self, body: bytes):
        self.body = body

    def read(self, _limit: int = -1) -> bytes:
        return self.body


class FakeConnection:
    def __init__(self, response: FakeResponse):
        self.response = response
        self.headers: list[tuple[str, str]] = []
        self.body = bytearray()

    def putrequest(self, _method: str, _target: str) -> None:
        pass

    def putheader(self, name: str, value: str) -> None:
        self.headers.append((name, value))

    def endheaders(self) -> None:
        pass

    def send(self, chunk: bytes) -> None:
        self.body.extend(chunk)

    def request(self, _method: str, _target: str, **_kwargs: object) -> None:
        pass

    def getresponse(self) -> FakeResponse:
        return self.response

    def close(self) -> None:
        pass


class FakeContext:
    def __init__(self):
        self.hooks = {}
        self.middleware = {}
        self.tools = {}

    def register_hook(self, name, callback):
        self.hooks[name] = callback

    def register_middleware(self, name, callback):
        self.middleware[name] = callback

    def register_tool(self, **kwargs):
        self.tools[kwargs["name"]] = kwargs


class NativeAttachmentTurnRouteTests(unittest.TestCase):
    def test_bind_route_reaches_gateway(self):
        connection = FakeConnection(FakeResponse(b'{"ok":true}'))
        with patch.dict(
            os.environ,
            {"M365_HERMES_RECALL_PROVENANCE_SECRET": "test-secret"},
            clear=False,
        ), patch.object(plugin, "_connection", return_value=connection) as factory:
            self.assertTrue(
                plugin._turn_route(
                    "https://m365.example/hermes/v1",
                    "session",
                    "turn",
                    "bind",
                )
            )
        factory.assert_called_once_with("m365.example", None, True)


class NativeAttachmentPluginTests(unittest.TestCase):
    def setUp(self):
        self.environment = patch.dict(
            os.environ,
            {
                "M365_HERMES_RECALL_PROVENANCE_SECRET": "test-secret",
                "M365_HERMES_PROVIDER": "m365",
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
        with recall._lock:
            recall._turns.clear()

    def tearDown(self):
        self.environment.stop()
        self.turn_route.stop()
        with plugin._lock:
            plugin._sessions.clear()
            plugin._outcomes.clear()
            plugin._ended.clear()
            plugin._pending_ends.clear()
        with recall._lock:
            recall._turns.clear()

    def _route(self, session="session", turn="turn", session_key=None):
        if session_key is None:
            session_key = session
        plugin._remember_route(
            (session, turn),
            "https://m365.example/hermes/v1",
            {"extra_body": {"session_key": session_key}},
        )

    def _stage_fake(self, paths: dict[str, bytes] | None = None, refs=None):
        paths = paths or {}
        refs = iter(refs or ["A" * 43, "B" * 43, "C" * 43])

        def stage(opened, _base_url, _session_key, _turn_id):
            stage_ref = next(refs)
            return {
                "stage_ref": stage_ref,
                "size": opened.size,
                "sha256": hashlib.sha256(
                    paths.get(str(opened.path), b"fixture")
                ).hexdigest(),
            }

        return stage

    def _attach(self, path: Path, session="session", turn="turn"):
        return plugin.m365_native_attach(
            {"files": [{"local_path": str(path)}]},
            session_id=session,
            turn_id=turn,
        )

    def _llm(self, session="session", turn="turn", provider="m365", request=None):
        with patch.object(plugin, "_turn_route", return_value=True):
            return plugin.on_llm_request(
                request
                or {
                    "messages": [{"role": "user", "content": "sentinel"}],
                    "extra_body": {"session_key": session},
                },
                session_id=session,
                turn_id=turn,
                provider=provider,
                api_mode="chat_completions",
                base_url="https://m365.example/hermes/v1",
            )

    def test_tool_accepts_one_or_two_and_rejects_zero_or_three(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            paths = []
            contents = {}
            for index in range(3):
                path = Path(root) / f"fixture-{index}.txt"
                content = f"sentinel-{index}".encode()
                path.write_bytes(content)
                paths.append(path)
                contents[str(path)] = content
            with patch.object(
                plugin, "_stage_file", side_effect=self._stage_fake(contents)
            ), patch.object(plugin, "_release_route"):
                for count in (1, 2):
                    plugin._sessions.clear()
                    plugin._outcomes.clear()
                    self._route()
                    result = json.loads(
                        plugin.m365_native_attach(
                            {"files": [{"local_path": str(path)} for path in paths[:count]]},
                            session_id="session",
                            turn_id="turn",
                        )
                    )
                    self.assertTrue(result["ok"])
                    self.assertEqual(len(result["attachments"]), count)
                for count in (0, 3):
                    plugin._sessions.clear()
                    plugin._outcomes.clear()
                    self._route()
                    result = json.loads(
                        plugin.m365_native_attach(
                            {"files": [{"local_path": str(path)} for path in paths[:count]]},
                            session_id="session",
                            turn_id="turn",
                        )
                    )
                    self.assertFalse(result["ok"])
                    self.assertEqual(result["error"]["code"], "invalid_attachment_count")

    def test_allowed_root_escape_missing_directory_empty_and_hash_fail_closed(self):
        with tempfile.TemporaryDirectory() as root, tempfile.TemporaryDirectory() as outside:
            inside = Path(root) / "inside.txt"
            inside.write_text("inside")
            empty = Path(root) / "empty.txt"
            empty.touch()
            outside_path = Path(outside) / "outside.txt"
            outside_path.write_text("outside")
            symlink = Path(root) / "link.txt"
            try:
                symlink.symlink_to(inside)
            except OSError:
                symlink = None
            with patch.dict(os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}), patch.object(
                plugin, "_release_route"
            ):
                cases = [
                    (outside_path, "path_denied"),
                    (Path(root) / "missing.txt", "file_missing"),
                    (Path(root), "not_regular_file"),
                    (empty, "empty_file"),
                ]
                if symlink is not None:
                    cases.append((symlink, "path_denied"))
                for path, expected in cases:
                    self._route()
                    result = json.loads(self._attach(path))
                    self.assertEqual(result["error"]["code"], expected)
                    plugin._sessions.clear()
                    plugin._outcomes.clear()
                self._route()
                with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(inside): b"inside"})):
                    result = json.loads(
                        plugin.m365_native_attach(
                            {
                                "files": [
                                    {"local_path": str(inside), "expected_sha256": "0" * 64}
                                ]
                            },
                            session_id="session",
                            turn_id="turn",
                        )
                    )
                self.assertEqual(result["error"]["code"], "hash_mismatch")

    def test_same_session_different_turns_are_isolated_and_failure_does_not_poison_next_turn(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "turn.txt"
            path.write_bytes(b"turn")
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"turn"})), patch.object(
                plugin, "_release_route"
            ):
                self._route("session", "turn-a")
                self.assertTrue(json.loads(self._attach(path, "session", "turn-a"))["ok"])
                turn_b = self._llm("session", "turn-b")
                self.assertNotIn(plugin._CONTEXT_FIELD, turn_b["request"].get("extra_body", {}))
                plugin._sessions.clear()
                plugin._outcomes.clear()
                self._route("session", "turn-a")
                failed = json.loads(
                    plugin.m365_native_attach(
                        {"files": [{"local_path": str(path), "expected_sha256": "f" * 64}]},
                        session_id="session",
                        turn_id="turn-a",
                    )
                )
                self.assertFalse(failed["ok"])
                after_failure = self._llm("session", "turn-a")
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    after_failure["request"].get("extra_body", {}),
                )
                self._route("session", "turn-b")
                recovered = json.loads(self._attach(path, "session", "turn-b"))
                self.assertTrue(recovered["ok"])

    def test_replace_and_corrected_attach_recover_without_hidden_poison(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first = Path(root) / "first.txt"
            second = Path(root) / "second.txt"
            first.write_bytes(b"first")
            second.write_bytes(b"second")
            contents = {str(first): b"first", str(second): b"second"}
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self._stage_fake(contents, refs=[chr(65 + i) * 43 for i in range(8)]),
            ), patch.object(
                plugin, "_release_route"
            ):
                self._route("session", "turn")
                self.assertTrue(json.loads(self._attach(first))["ok"])
                self.assertTrue(json.loads(self._attach(second))["ok"])
                context = self._llm()["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual([item["original_filename"] for item in context["attachments"]], ["second.txt"])
                plugin._sessions.clear()
                plugin._outcomes.clear()
                self._route("session", "turn")
                failed = json.loads(
                    plugin.m365_native_attach(
                        {"files": [{"local_path": str(first), "expected_sha256": "f" * 64}]},
                        session_id="session",
                        turn_id="turn",
                    )
                )
                self.assertEqual(failed["error"]["code"], "hash_mismatch")
                after_failure = self._llm()
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    after_failure["request"].get("extra_body", {}),
                )
                self.assertTrue(json.loads(self._attach(first))["ok"])
                self.assertIn(plugin._CONTEXT_FIELD, self._llm()["request"]["extra_body"])

    def test_same_turn_attach_operations_do_not_overlap(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first_path = Path(root) / "first.txt"
            second_path = Path(root) / "second.txt"
            first_path.write_bytes(b"first")
            second_path.write_bytes(b"second")
            first_entered = threading.Event()
            release_first = threading.Event()
            second_done = threading.Event()
            results = {}
            thread_errors = []

            def stage(opened, _base_url, _session_key, _turn_id):
                if opened.path == first_path.resolve():
                    first_entered.set()
                    if not release_first.wait(2):
                        raise RuntimeError("first stage did not receive release")
                    return {
                        "stage_ref": "A" * 43,
                        "size": opened.size,
                        "sha256": hashlib.sha256(b"first").hexdigest(),
                    }
                return {
                    "stage_ref": "B" * 43,
                    "size": opened.size,
                    "sha256": hashlib.sha256(b"second").hexdigest(),
                }

            def invoke(name, path, done=None):
                try:
                    results[name] = json.loads(self._attach(path))
                except BaseException as error:  # pragma: no cover - test thread relay
                    thread_errors.append(error)
                finally:
                    if done is not None:
                        done.set()

            with patch.object(plugin, "_stage_file", side_effect=stage), patch.object(
                plugin, "_release_route"
            ):
                self._route()
                first_thread = threading.Thread(
                    target=invoke, args=("first", first_path)
                )
                first_thread.start()
                self.assertTrue(first_entered.wait(2))

                second_thread = threading.Thread(
                    target=invoke, args=("second", second_path, second_done)
                )
                second_thread.start()
                try:
                    self.assertTrue(second_done.wait(2))
                    self.assertFalse(results["second"]["ok"])
                    self.assertEqual(
                        results["second"]["error"]["code"], "stage_transport_failed"
                    )
                finally:
                    release_first.set()
                    first_thread.join(2)
                    second_thread.join(2)

            self.assertFalse(first_thread.is_alive())
            self.assertFalse(second_thread.is_alive())
            self.assertEqual(thread_errors, [])
            self.assertTrue(results["first"]["ok"])
            context = self._llm()["request"]["extra_body"][plugin._CONTEXT_FIELD]
            self.assertEqual(
                [reference["stage_ref"] for reference in context["attachments"]],
                ["A" * 43],
            )

    def test_replacing_with_same_stage_ref_keeps_active_capability(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "repeat.txt"
            path.write_bytes(b"repeat")
            released = []
            stage = self._stage_fake(
                {str(path): b"repeat"}, refs=["A" * 43, "A" * 43]
            )
            with patch.object(plugin, "_stage_file", side_effect=stage), patch.object(
                plugin,
                "_release_route",
                side_effect=lambda _base, _session, _turn, refs: released.append(refs),
            ):
                self._route()
                self.assertTrue(json.loads(self._attach(path))["ok"])
                self.assertTrue(json.loads(self._attach(path))["ok"])

            self.assertNotIn(["A" * 43], released)
            context = self._llm()["request"]["extra_body"][plugin._CONTEXT_FIELD]
            self.assertEqual(
                [reference["stage_ref"] for reference in context["attachments"]],
                ["A" * 43],
            )

    def test_duplicate_stage_ref_is_a_tool_failure_and_rolls_back(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first = Path(root) / "first.txt"
            second = Path(root) / "second.txt"
            first.write_bytes(b"first")
            second.write_bytes(b"second")
            released = []
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self._stage_fake(
                    {str(first): b"first", str(second): b"second"}, refs=["A" * 43, "A" * 43]
                ),
            ), patch.object(
                plugin,
                "_release_route",
                side_effect=lambda _base, _session, _turn, refs: released.append(refs),
            ):
                self._route()
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
                plugin._CONTEXT_FIELD, self._llm()["request"].get("extra_body", {})
            )

    def test_partial_stage_failure_rolls_back_and_clears_active_set(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            first = Path(root) / "first.txt"
            second = Path(root) / "second.txt"
            first.write_bytes(b"a")
            second.write_bytes(b"b")
            calls = 0
            released = []

            def stage(opened, base, session, turn):
                nonlocal calls
                calls += 1
                if calls == 2:
                    raise plugin._AttachmentFailure("stage_transport_failed")
                return {"stage_ref": "A" * 43, "size": opened.size, "sha256": hashlib.sha256(b"a").hexdigest()}

            with patch.object(plugin, "_stage_file", side_effect=stage), patch.object(
                plugin, "_release_route", side_effect=lambda _base, _session, _turn, refs: released.append(refs)
            ):
                self._route()
                result = json.loads(
                    plugin.m365_native_attach(
                        {"files": [{"local_path": str(first)}, {"local_path": str(second)}]},
                        session_id="session",
                        turn_id="turn",
                    )
                )
                self.assertEqual(result["error"]["code"], "stage_transport_failed")
                self.assertIn(["A" * 43], released)
                after_failure = self._llm()
                self.assertNotIn(
                    plugin._CONTEXT_FIELD,
                    after_failure["request"].get("extra_body", {}),
                )

    def test_tool_result_and_context_never_expose_path_or_stage_ref(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "safe.txt"
            path.write_bytes(b"safe")
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"safe"})), patch.object(
                plugin, "_release_route"
            ):
                self._route()
                result = json.loads(self._attach(path))
                self.assertIn("turn_binding", result)
                self.assertNotIn("stage_ref", json.dumps(result))
                self.assertNotIn(str(path), json.dumps(result))
                self.assertNotIn("test-secret", json.dumps(result))
                context = self._llm()["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertIn("stage_ref", context["attachments"][0])

    def test_native_context_also_sets_gateway_wire_session_key(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "wire-session.txt"
            path.write_bytes(b"wire-session")
            with patch.object(
                plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"wire-session"})
            ), patch.object(plugin, "_release_route"):
                self._route("session", "turn")
                self.assertTrue(json.loads(self._attach(path))["ok"])
                result = self._llm(request={"messages": [{"role": "user", "content": "read"}]})
                self.assertEqual(result["request"]["extra_body"]["session_key"], "session")
                context = result["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(context["session_key"], "session")

    def test_signed_context_binds_unicode_fields_and_state_loss_fails_closed(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "報告.xlsx"
            path.write_bytes("sentinel-附件".encode())
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): path.read_bytes()})), patch.object(
                plugin, "_release_route"
            ):
                self._route("session-附件", "turn-😀", "session-附件")
                result = json.loads(self._attach(path, "session-附件", "turn-😀"))
                self.assertTrue(result["ok"])
                llm = self._llm("session-附件", "turn-😀")
                context = llm["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(context["session_key"], "session-附件")
                self.assertEqual(context["turn_id"], "turn-😀")
                self.assertEqual(context["signature"], plugin._sign_context(context))
                with plugin._lock:
                    plugin._sessions.clear()
                lost = self._llm("session-附件", "turn-😀")
                lost_context = lost["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(lost_context["error"], "native_attachment_state_lost")

    def test_success_context_with_hidden_state_and_outcome_loss_fails_closed(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "evicted.txt"
            path.write_bytes(b"evicted")
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self._stage_fake({str(path): b"evicted"}),
            ), patch.object(plugin, "_release_route"):
                self._route()
                self.assertTrue(json.loads(self._attach(path))["ok"])
                successful = self._llm()
                with plugin._lock:
                    plugin._sessions.clear()
                    plugin._outcomes.clear()

            lost = self._llm(request=successful["request"])

            lost_context = lost["request"]["extra_body"][plugin._CONTEXT_FIELD]
            self.assertEqual(lost_context["error"], "native_attachment_state_lost")
            repeated = self._llm(request=lost["request"])
            repeated_context = repeated["request"]["extra_body"][plugin._CONTEXT_FIELD]
            self.assertEqual(repeated_context["error"], "native_attachment_state_lost")

    def test_unicode_context_canonicalization_matches_the_shared_rust_fixture(self):
        fixture = json.loads(
            (PLUGIN_PATH.with_name("canonical_context_unicode_fixture.json")).read_text()
        )
        self.assertEqual(
            plugin._context_canonical(fixture["context"]).decode("utf-8"),
            fixture["canonical"],
        )

    def test_single_fd_stream_hash_and_raw_secret_not_on_wire(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "single-pass.txt"
            content = b"single-pass" * 10
            path.write_bytes(content)
            response = FakeResponse(
                json.dumps(
                    {
                        "schema": plugin._STAGE_SCHEMA,
                        "capability": "B" * 43,
                        "size": len(content),
                        "sha256": hashlib.sha256(content).hexdigest(),
                    }
                ).encode()
            )
            connection = FakeConnection(response)
            with patch.dict(os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}), patch.object(
                plugin, "_connection", return_value=connection
            ), patch.object(plugin.os, "open", wraps=plugin.os.open) as opened:
                source = plugin._open_allowed_file(str(path))
                plugin._stage_file(source, "https://m365.example/hermes/v1", "session", "turn")
                source.handle.close()
                self.assertEqual(opened.call_count, 1)
            self.assertEqual(bytes(connection.body), content)
            values = [value for _name, value in connection.headers]
            self.assertNotIn("test-secret", values)
            self.assertNotEqual(values[2], "test-secret")

    def test_injected_file_ceiling_is_enforced_without_a_large_fixture(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "ceiling.txt"
            path.write_bytes(b"12345678")
            with patch.object(plugin, "_MAX_FILE_BYTES", 7), patch.object(
                plugin, "_release_route"
            ):
                self._route()
                result = json.loads(self._attach(path))
            self.assertEqual(result["error"]["code"], "file_too_large")

    def test_single_fd_final_identity_change_is_typed_local_file_changed(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "changes.txt"
            content = b"original-content"
            path.write_bytes(content)
            response = FakeResponse(
                json.dumps(
                    {
                        "schema": plugin._STAGE_SCHEMA,
                        "capability": "C" * 43,
                        "size": len(content),
                        "sha256": hashlib.sha256(content).hexdigest(),
                    }
                ).encode()
            )

            class MutatingConnection(FakeConnection):
                mutated = False

                def send(self, chunk: bytes) -> None:
                    super().send(chunk)
                    if not self.mutated:
                        self.mutated = True
                        path.write_bytes(b"changed-after-open")

            connection = MutatingConnection(response)
            with patch.dict(os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}), patch.object(
                plugin, "_connection", return_value=connection
            ):
                source = plugin._open_allowed_file(str(path))
                try:
                    with self.assertRaises(plugin._AttachmentFailure) as raised:
                        plugin._stage_file(
                            source, "https://m365.example/hermes/v1", "session", "turn"
                        )
                finally:
                    source.handle.close()
            self.assertEqual(raised.exception.reason, "local_file_changed")
            self.assertEqual(raised.exception.stage_refs, ["C" * 43])

    def test_local_file_changed_stage_ref_is_rolled_back_by_attach(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "changes.txt"
            content = b"original-content"
            path.write_bytes(content)
            response = FakeResponse(
                json.dumps(
                    {
                        "schema": plugin._STAGE_SCHEMA,
                        "capability": "C" * 43,
                        "size": len(content),
                        "sha256": hashlib.sha256(content).hexdigest(),
                    }
                ).encode()
            )

            class MutatingConnection(FakeConnection):
                mutated = False

                def send(self, chunk: bytes) -> None:
                    super().send(chunk)
                    if not self.mutated:
                        self.mutated = True
                        path.write_bytes(b"changed-after-open")

            connection = MutatingConnection(response)
            released = []
            with patch.object(plugin, "_connection", return_value=connection), patch.object(
                plugin,
                "_release_route",
                side_effect=lambda _base, _session, _turn, refs: released.append(refs),
            ):
                self._route()
                result = json.loads(self._attach(path))
            self.assertEqual(result["error"]["code"], "local_file_changed")
            self.assertIn(["C" * 43], released)

    def test_failed_outcome_always_clears_context_even_if_a_stale_ref_is_present(self):
        with plugin._lock:
            plugin._sessions[("session", "turn")] = {
                "refs": [{"stage_ref": "A" * 43}],
                "route": "https://m365.example/hermes/v1",
                "session_key": "session",
            }
            plugin._outcomes[("session", "turn")] = {
                "ok": False,
                "error": "stage_transport_failed",
            }
        result = self._llm()
        self.assertNotIn(plugin._CONTEXT_FIELD, result["request"].get("extra_body", {}))

    def test_http_is_only_available_through_explicit_test_seam(self):
        with self.assertRaises(plugin._AttachmentFailure):
            plugin._request_target("http://127.0.0.1:8080/hermes/v1", "stage")
        self.assertEqual(
            plugin._request_target(
                "http://127.0.0.1:8080/hermes/v1", "stage", test_only=True
            )[3],
            False,
        )

    def test_stage_route_is_pinned_to_the_configured_gateway_authority(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "pinned.txt"
            path.write_bytes(b"pinned")
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"pinned"})), patch.object(
                plugin, "_release_route"
            ):
                self._route()
                self.assertTrue(json.loads(self._attach(path))["ok"])
                with patch.dict(
                    os.environ,
                    {"M365_HERMES_GATEWAY_BASE_URL": "https://another.example/hermes/v1"},
                ):
                    result = self._llm()
                context = result["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(context["error"], "native_attachment_binding_invalid")

    def test_evicted_success_state_fails_closed_instead_of_inheriting_or_dropping(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "evicted.txt"
            path.write_bytes(b"evicted")
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"evicted"})), patch.object(
                plugin, "_release_route"
            ), patch.object(plugin, "_MAX_TURNS", 1), patch.object(
                plugin, "_MAX_OUTCOMES", 2
            ):
                self._route("session", "turn-a")
                self.assertTrue(json.loads(self._attach(path, "session", "turn-a"))["ok"])
                self._route("session", "turn-b")
                lost = self._llm("session", "turn-a")
                context = lost["request"]["extra_body"][plugin._CONTEXT_FIELD]
                self.assertEqual(context["error"], "native_attachment_state_lost")

    def test_unknown_stage_outcome_is_a_tool_failure_without_a_model_attachment(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "unknown.txt"
            path.write_bytes(b"unknown")
            with patch.object(
                plugin,
                "_stage_file",
                side_effect=plugin._AttachmentFailure("stage_transport_failed"),
            ), patch.object(plugin, "_release_route"):
                self._route()
                result = json.loads(self._attach(path))
                self.assertEqual(result["error"]["code"], "stage_transport_failed")
                self.assertNotIn(plugin._CONTEXT_FIELD, self._llm()["request"].get("extra_body", {}))

    def test_session_end_is_turn_scoped_and_non_m365_is_not_injected(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "turn.txt"
            path.write_bytes(b"turn")
            with patch.object(plugin, "_stage_file", side_effect=self._stage_fake({str(path): b"turn"})), patch.object(
                plugin, "_release_route"
            ):
                for turn in ("turn-a", "turn-b"):
                    self._route("session", turn)
                    self.assertTrue(json.loads(self._attach(path, "session", turn))["ok"])
                with patch.object(plugin, "_turn_route", return_value=True):
                    plugin.on_session_end(session_id="session", turn_id="turn-a")
                self.assertIsNotNone(self._llm("session", "turn-b"))
                ended = self._llm("session", "turn-a")
                self.assertEqual(
                    ended["request"]["extra_body"][plugin._CONTEXT_FIELD]["error"],
                    "native_attachment_binding_invalid",
                )
                self.assertIsNone(self._llm("session", "turn-c", provider="openai"))

    def test_delayed_attach_cannot_revive_a_turn_after_session_end(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "delayed.txt"
            path.write_bytes(b"delayed")
            released = []

            def stage(opened, *_args):
                plugin.on_session_end(session_id="session", turn_id="turn")
                return {
                    "stage_ref": "D" * 43,
                    "size": opened.size,
                    "sha256": hashlib.sha256(b"delayed").hexdigest(),
                }

            with patch.object(plugin, "_stage_file", side_effect=stage), patch.object(
                plugin,
                "_release_route",
                side_effect=lambda _base, _session, _turn, refs: released.append(refs),
            ):
                self._route()
                result = json.loads(self._attach(path))
            self.assertEqual(result["error"]["code"], "stage_transport_failed")
            self.assertIn(["D" * 43], released)
            with plugin._lock:
                self.assertNotIn(("session", "turn"), plugin._sessions)
                self.assertNotIn(("session", "turn"), plugin._outcomes)

    def test_delayed_llm_callback_fails_closed_after_session_end(self):
        with patch.object(plugin, "_turn_route", return_value=True):
            self._route()
            plugin.on_session_end(session_id="session", turn_id="turn")
            result = self._llm()

        context = result["request"]["extra_body"][plugin._CONTEXT_FIELD]
        self.assertEqual(context["error"], "native_attachment_binding_invalid")

    def test_failed_turn_end_is_retried_before_the_next_turn_binds(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(
            os.environ, {"M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": root}
        ):
            path = Path(root) / "retry-end.txt"
            path.write_bytes(b"retry-end")
            calls = []
            end_attempts = 0

            def route(_base, _session, turn, action):
                nonlocal end_attempts
                calls.append((turn, action))
                if action == "end" and turn == "turn-a":
                    end_attempts += 1
                    return end_attempts > 1
                return True

            with patch.object(
                plugin,
                "_stage_file",
                side_effect=self._stage_fake({str(path): b"retry-end"}),
            ), patch.object(plugin, "_release_route"), patch.object(
                plugin, "_turn_route", side_effect=route
            ):
                self._route("session", "turn-a")
                self.assertTrue(
                    json.loads(self._attach(path, "session", "turn-a"))["ok"]
                )
                plugin.on_session_end(session_id="session", turn_id="turn-a")
                self._route("session", "turn-b")
                self.assertTrue(
                    json.loads(self._attach(path, "session", "turn-b"))["ok"]
                )
            self.assertEqual(
                calls,
                [
                    ("turn-a", "bind"),
                    ("turn-a", "end"),
                    ("turn-a", "end"),
                    ("turn-b", "bind"),
                ],
            )
            with plugin._lock:
                self.assertFalse(plugin._pending_ends)

    def test_tool_request_carries_exact_private_host_identity(self):
        result = plugin.on_tool_request(
            tool_name="m365_native_attach",
            args={"files": []},
            session_id="session",
            turn_id="turn",
            tool_call_id="call",
        )
        self.assertEqual(
            result["args"][plugin._PRIVATE_HOST_IDENTITY],
            {"session_id": "session", "turn_id": "turn", "tool_call_id": "call"},
        )

    def test_cross_plugin_extra_body_order_preserves_messages_and_recall_fields(self):
        with plugin._lock:
            plugin._sessions[("session", "turn")] = {
                "refs": [
                    {
                        "stage_ref": "A" * 43,
                        "size": 3,
                        "sha256": hashlib.sha256(b"abc").hexdigest(),
                        "original_filename": "報告.xlsx",
                        "extension": "xlsx",
                        "mime_type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
                        "attachment_id": "附件-1",
                        "source_message_id": "郵件-1",
                    }
                ],
                "route": "https://m365.example/hermes/v1",
                "session_key": "session",
            }
            plugin._outcomes[("session", "turn")] = {"ok": True}
        request = {
            "messages": [
                {
                    "role": "user",
                    "content": "read\n\n<memory-context>\nrecalled\n</memory-context>",
                }
            ],
            "extra_body": {"session_key": "session", "recall": {"signature": "valid"}},
        }
        original = json.loads(json.dumps(request))
        for callback_order in ("native_first", "native_last"):
            current = json.loads(json.dumps(request))
            recall.on_pre_llm_call(
                session_id="session", turn_id="turn", user_message="read"
            )

            def apply(callback, value):
                with patch.object(plugin, "_turn_route", return_value=True):
                    result = callback(
                        request=value,
                        session_id="session",
                        turn_id="turn",
                        api_request_id="turn:api:1",
                        api_call_count=1,
                        provider="m365",
                        api_mode="chat_completions",
                        base_url="https://m365.example/hermes/v1",
                    )
                return result["request"] if result is not None else value

            if callback_order == "native_first":
                current = apply(plugin.on_llm_request, current)
                current = apply(recall.on_llm_request, current)
            else:
                current = apply(recall.on_llm_request, current)
                current = apply(plugin.on_llm_request, current)
            self.assertEqual(current["messages"], original["messages"])
            self.assertEqual(current["extra_body"]["session_key"], "session")
            self.assertIn(plugin._CONTEXT_FIELD, current["extra_body"])
            self.assertIn("m365_recall_provenance", current["extra_body"])
            self.assertTrue(
                current["extra_body"][plugin._CONTEXT_FIELD]["signature"].startswith(
                    "sha256="
                )
            )
            self.assertTrue(
                current["extra_body"]["m365_recall_provenance"]["signature"].startswith(
                    "sha256="
                )
            )

    def test_registers_required_middleware_dependency_and_no_post_api_cleanup(self):
        context = FakeContext()
        plugin.register(context)
        self.assertIn("m365_native_attach", context.tools)
        self.assertEqual(set(context.hooks), {"on_session_end"})
        self.assertEqual(set(context.middleware), {"tool_request", "llm_request"})
        self.assertEqual(context.tools["m365_native_attach"]["schema"]["properties"]["files"]["maxItems"], 2)
        self.assertIn("M365_HERMES_GATEWAY_BASE_URL", context.tools["m365_native_attach"]["requires_env"])
        self.assertFalse(hasattr(plugin, "on_post_api_request"))


if __name__ == "__main__":
    unittest.main()
