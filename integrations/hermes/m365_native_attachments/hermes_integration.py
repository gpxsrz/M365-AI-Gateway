"""Real Hermes/ Gateway regression for the M365 readback middleware seam.

This module is intentionally not named ``test*.py``: the ordinary plugin unit
suite remains runnable without a Hermes checkout and a Gateway binary. CI calls
this module explicitly and therefore fails when any integration prerequisite is
missing instead of converting the required test into a skip.
"""

from __future__ import annotations

import copy
import hashlib
import http.client
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


HERMES_FIXTURE_COMMIT = "641f7c810449d9af5c21b0a5ee33b29b192b4117"
_FIXTURE_SECRET = "fixture-" + hashlib.sha256(
    b"m365-native-attachments-hermes-integration"
).hexdigest()
_MODEL = "gpt-5.6-reasoning"
_PROVIDER = "m365-copilot"
_API_MODE = "chat_completions"


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(128 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


class _Capture:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._chat: list[dict[str, Any]] = []
        self.stage_requests = 0
        self.release_requests = 0
        self.turn_requests = 0

    def record(self, path: str, body: bytes) -> None:
        with self._lock:
            if path.endswith("/attachments/stage"):
                self.stage_requests += 1
                return
            if path.endswith("/attachments/release"):
                self.release_requests += 1
                return
            if path.endswith("/attachments/turn"):
                self.turn_requests += 1
                return
            if not path.endswith("/chat/completions"):
                return
            try:
                payload = json.loads(body.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError):
                return
            tools = payload.get("tools") if isinstance(payload, dict) else None
            tool_names = []
            read_only = False
            if isinstance(tools, list):
                for tool in tools:
                    function = tool.get("function") if isinstance(tool, dict) else None
                    if not isinstance(function, dict):
                        continue
                    name = function.get("name")
                    if isinstance(name, str):
                        tool_names.append(name)
                    annotations = function.get("annotations")
                    if isinstance(annotations, dict) and annotations.get("readOnlyHint") is True:
                        read_only = True
            self._chat.append(
                {
                    "path": path,
                    "keys": tuple(sorted(payload)) if isinstance(payload, dict) else (),
                    "message_count": len(payload.get("messages", []))
                    if isinstance(payload, dict) and isinstance(payload.get("messages"), list)
                    else 0,
                    "tool_names": tuple(sorted(tool_names)),
                    "session_key": isinstance(payload, dict)
                    and isinstance(payload.get("session_key"), str),
                    "recall": isinstance(payload, dict)
                    and isinstance(payload.get("m365_recall_provenance"), dict),
                    "native": isinstance(payload, dict)
                    and isinstance(payload.get("m365_native_attachment_context"), dict),
                    "read_only": read_only,
                    "body_sha256": hashlib.sha256(body).hexdigest(),
                }
            )

    def last_chat(self) -> dict[str, Any]:
        with self._lock:
            if not self._chat:
                raise AssertionError("the OpenAI SDK did not send a chat request")
            return dict(self._chat[-1])


class _ForwardingHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    backend_port: int
    capture: _Capture

    def log_message(self, _format: str, *_args: Any) -> None:
        return

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        self._forward()

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        self._forward()

    def _forward(self) -> None:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        body = self.rfile.read(length) if length else b""
        self.capture.record(self.path, body)
        headers = {
            key: value
            for key, value in self.headers.items()
            if key.lower() not in {"host", "connection", "content-length", "transfer-encoding"}
        }
        headers["Connection"] = "close"
        try:
            connection = http.client.HTTPConnection("127.0.0.1", self.backend_port, timeout=15)
            try:
                connection.request(self.command, self.path, body=body, headers=headers)
                response = connection.getresponse()
                response_body = response.read()
                response_status = response.status
                response_reason = response.reason
                response_headers = response.getheaders()
            finally:
                connection.close()
            self.send_response(response_status, response_reason)
            for key, value in response_headers:
                if key.lower() in {"connection", "content-length", "transfer-encoding"}:
                    continue
                self.send_header(key, value)
            self.send_header("Connection", "close")
            self.send_header("Content-Length", str(len(response_body)))
            self.end_headers()
            self.wfile.write(response_body)
        except (OSError, http.client.HTTPException):
            self.send_error(502, "loopback forwarding failed")
        finally:
            self.close_connection = True


class _TlsServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address: tuple[str, int], handler: type[_ForwardingHandler], cert: Path, key: Path):
        super().__init__(address, handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certfile=str(cert), keyfile=str(key))
        self._tls_context = context

    def get_request(self):  # type: ignore[no-untyped-def]
        connection, address = super().get_request()
        try:
            return self._tls_context.wrap_socket(connection, server_side=True), address
        except BaseException:
            connection.close()
            raise


class _GatewayHarness:
    def __init__(self, root: Path, binary: Path, secret: str) -> None:
        self.root = root
        self.binary = binary
        self.secret = secret
        self.backend_port = _free_port()
        self.tls_port = _free_port()
        self.capture = _Capture()
        self.process: subprocess.Popen[bytes] | None = None
        self.server: _TlsServer | None = None
        self.server_thread: threading.Thread | None = None
        self.cert = root / "loopback-cert.pem"
        self.key = root / "loopback-key.pem"
        raw_key = "m365_" + hashlib.sha256(b"integration-api-key").hexdigest()
        self.api_key = raw_key

    @property
    def base_url(self) -> str:
        return f"https://127.0.0.1:{self.tls_port}/hermes/v1"

    def start(self) -> None:
        data_dir = self.root / "gateway-data"
        data_dir.mkdir()
        key_hash = hashlib.sha256(self.api_key.encode("utf-8")).hexdigest()
        (data_dir / "api-keys.json").write_text(
            json.dumps(
                {
                    "keys": [
                        {
                            "id": "integration-key",
                            "name": "integration fixture",
                            "prefix": self.api_key[:12],
                            "hash": key_hash,
                            "createdAt": "2026-09-22T00:00:00Z",
                            "revoked": False,
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        (data_dir / "api-keys.json").chmod(0o600)
        self._make_certificate()
        env = os.environ.copy()
        env.update(
            {
                "M365_LISTEN": f"127.0.0.1:{self.backend_port}",
                "M365_DATA_DIR": str(data_dir),
                "M365_ADMIN_PASSWORD": "integration-admin-fixture",
                "M365_HERMES_RECALL_PROVENANCE_SECRET": self.secret,
                "RUST_LOG": "error",
            }
        )
        self.process = subprocess.Popen(
            [str(self.binary)],
            cwd=str(self.root),
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self._wait_for_backend()
        handler = type(
            "LoopbackForwardingHandler",
            (_ForwardingHandler,),
            {"backend_port": self.backend_port, "capture": self.capture},
        )
        self.server = _TlsServer(("127.0.0.1", self.tls_port), handler, self.cert, self.key)
        self.server_thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.server_thread.start()

    def _make_certificate(self) -> None:
        if shutil.which("openssl") is None:
            raise RuntimeError("openssl is required for the HTTPS-only native attachment route")
        subprocess.run(
            [
                "openssl",
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-keyout",
                str(self.key),
                "-out",
                str(self.cert),
                "-days",
                "1",
                "-subj",
                "/CN=127.0.0.1",
                "-addext",
                "subjectAltName=IP:127.0.0.1",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

    def _wait_for_backend(self) -> None:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if self.process is not None and self.process.poll() is not None:
                raise RuntimeError(f"Gateway fixture exited with status {self.process.returncode}")
            try:
                connection = http.client.HTTPConnection("127.0.0.1", self.backend_port, timeout=1)
                connection.request(
                    "GET",
                    "/v1/models",
                    headers={"Authorization": f"Bearer {self.api_key}"},
                )
                response = connection.getresponse()
                response.read()
                connection.close()
                if response.status == 200:
                    return
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.1)
        raise RuntimeError("Gateway fixture did not become healthy")

    def stop(self) -> None:
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
        if self.server_thread is not None:
            self.server_thread.join(timeout=5)
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)


class HermesNativeAttachmentIntegrationTests(unittest.TestCase):
    """Exercise the deployed shape of the two repo-owned M365 middleware plugins."""

    @classmethod
    def setUpClass(cls) -> None:
        required = {
            "HERMES_AGENT_ROOT": os.environ.get("HERMES_AGENT_ROOT", ""),
            "M365_GATEWAY_BINARY": os.environ.get("M365_GATEWAY_BINARY", ""),
            "M365_NATIVE_ATTACHMENTS_PLUGIN_ROOT": os.environ.get(
                "M365_NATIVE_ATTACHMENTS_PLUGIN_ROOT", ""
            ),
            "M365_RECALL_PROVENANCE_PLUGIN_ROOT": os.environ.get(
                "M365_RECALL_PROVENANCE_PLUGIN_ROOT", ""
            ),
        }
        missing = sorted(name for name, value in required.items() if not value)
        if missing:
            raise RuntimeError("missing Hermes integration prerequisites: " + ", ".join(missing))
        cls.agent_root = Path(required["HERMES_AGENT_ROOT"]).resolve()
        cls.native_root = Path(required["M365_NATIVE_ATTACHMENTS_PLUGIN_ROOT"]).resolve()
        cls.recall_root = Path(required["M365_RECALL_PROVENANCE_PLUGIN_ROOT"]).resolve()
        cls.gateway_binary = Path(required["M365_GATEWAY_BINARY"]).resolve()
        for path in (cls.agent_root, cls.native_root, cls.recall_root, cls.gateway_binary):
            if not path.exists():
                raise RuntimeError(f"integration prerequisite does not exist: {path}")
        expected_commit = os.environ.get("HERMES_EXPECTED_COMMIT", HERMES_FIXTURE_COMMIT)
        actual_commit = subprocess.run(
            ["git", "-C", str(cls.agent_root), "rev-parse", "HEAD"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        if actual_commit != expected_commit:
            raise RuntimeError("Hermes fixture commit does not match the pinned integration commit")

        try:
            import httpx  # noqa: F401
            import openai  # noqa: F401
        except ImportError as exc:
            raise RuntimeError("Hermes integration requires the fixture OpenAI/httpx dependencies") from exc

        cls.temp = tempfile.TemporaryDirectory(prefix="m365-hermes-native-integration-")
        cls.root = Path(cls.temp.name)
        cls.home = cls.root / "hermes-home"
        cls.bundle = cls.root / "bundled-plugins"
        cls.home.mkdir()
        cls.bundle.mkdir()
        shutil.copytree(cls.recall_root, cls.bundle / "m365-recall-provenance")
        shutil.copytree(cls.native_root, cls.bundle / "m365-native-attachments")
        (cls.home / "config.yaml").write_text(
            "plugins:\n  enabled:\n    - m365-recall-provenance\n    - m365-native-attachments\n",
            encoding="utf-8",
        )
        cls._old_env = {
            key: os.environ.get(key)
            for key in (
                "HERMES_HOME",
                "HERMES_BUNDLED_PLUGINS",
                "M365_HERMES_RECALL_PROVENANCE_SECRET",
                "M365_HERMES_PROVIDER",
                "M365_HERMES_GATEWAY_BASE_URL",
                "M365_HERMES_ATTACHMENT_ALLOWED_ROOTS",
                "HERMES_TERMINAL_CWD",
                "SSL_CERT_FILE",
            )
        }
        cls.gateway = _GatewayHarness(cls.root, cls.gateway_binary, _FIXTURE_SECRET)
        cls.addClassCleanup(cls._cleanup_resources)
        cls.gateway.start()
        os.environ.update(
            {
                "HERMES_HOME": str(cls.home),
                "HERMES_BUNDLED_PLUGINS": str(cls.bundle),
                "M365_HERMES_RECALL_PROVENANCE_SECRET": _FIXTURE_SECRET,
                "M365_HERMES_PROVIDER": _PROVIDER,
                "M365_HERMES_GATEWAY_BASE_URL": cls.gateway.base_url,
                "M365_HERMES_ATTACHMENT_ALLOWED_ROOTS": str(cls.root),
                "HERMES_TERMINAL_CWD": str(cls.root),
                "SSL_CERT_FILE": str(cls.gateway.cert),
            }
        )
        sys.path.insert(0, str(cls.agent_root))
        from hermes_cli.middleware import apply_llm_request_middleware, apply_tool_request_middleware
        from hermes_cli.plugins import (
            discover_plugins,
            get_plugin_manager,
            invoke_hook,
            unload_plugins,
        )
        from tools import file_tools
        from tools.registry import registry
        cls.apply_llm_request_middleware = staticmethod(apply_llm_request_middleware)
        cls.apply_tool_request_middleware = staticmethod(apply_tool_request_middleware)
        cls.discover_plugins = staticmethod(discover_plugins)
        cls.get_plugin_manager = staticmethod(get_plugin_manager)
        cls.invoke_hook = staticmethod(invoke_hook)
        cls.unload_plugins = staticmethod(unload_plugins)
        cls.file_tools = file_tools
        cls.registry = registry
        cls.discover_plugins()

    @classmethod
    def _cleanup_resources(cls) -> None:
        if hasattr(cls, "invoke_hook"):
            try:
                cls.unload_plugins()
            except Exception:
                pass
        if hasattr(cls, "gateway"):
            cls.gateway.stop()
        if hasattr(cls, "_old_env"):
            for key, value in cls._old_env.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value
        if hasattr(cls, "agent_root"):
            try:
                sys.path.remove(str(cls.agent_root))
            except ValueError:
                pass
        # Keep the TemporaryDirectory finalizer for interpreter shutdown. The
        # fixture's process registry may still flush its isolated SQLite state
        # after class cleanup; deleting the directory here only creates a
        # teardown race and no additional safety.

    def _plugin_projection_request(
        self,
        session: str,
        turn: str,
        user_message: str,
        messages: list[dict[str, Any]] | None = None,
        *,
        provider: str = _PROVIDER,
        base_url: str | None = None,
        api_call_count: int = 0,
    ) -> tuple[dict[str, Any], Any]:
        recalled_content = (
            f"{user_message}\n\n<memory-context>\n"
            "synthetic recalled source\n</memory-context>"
        )
        wire_messages = copy.deepcopy(messages) if messages is not None else [
            {"role": "user", "content": recalled_content}
        ]
        if messages is not None:
            for message in wire_messages:
                if (
                    isinstance(message, dict)
                    and message.get("role") == "user"
                    and message.get("content") == user_message
                ):
                    message["content"] = recalled_content
                    break
        tools = self.registry.get_definitions(
            {"m365_native_attach", "read_file", "write_file", "skill_view"}, quiet=True
        )
        request = {
            "model": _MODEL,
            "messages": wire_messages,
            "tools": tools,
            "extra_body": {"caller_marker": "integration-fixture", "session_key": session},
        }
        original = copy.deepcopy(request)
        self.invoke_hook(
            "pre_llm_call",
            session_id=session,
            turn_id=turn,
            user_message=user_message,
        )
        result = self.apply_llm_request_middleware(
            request,
            task_id=f"integration-task-{session}",
            turn_id=turn,
            api_request_id=f"integration-request-{api_call_count}",
            session_id=session,
            platform="discord",
            model=_MODEL,
            provider=provider,
            base_url=base_url or self.gateway.base_url,
            api_mode=_API_MODE,
            api_call_count=api_call_count,
        )
        self.assertEqual(request, original, "middleware mutated the caller request")
        return request, result

    def _assert_common_projection(
        self,
        payload: dict[str, Any],
        session: str,
        native: bool,
        result_trace: Any = None,
    ) -> None:
        extra = payload.get("extra_body")
        self.assertIsInstance(extra, dict)
        self.assertEqual(extra.get("session_key"), session)
        self.assertEqual(extra.get("caller_marker"), "integration-fixture")
        self.assertIn("m365_recall_provenance", extra, result_trace)
        self.assertEqual("m365_native_attachment_context" in extra, native)
        tool_names = set()
        for tool in payload.get("tools", []):
            function = tool.get("function") if isinstance(tool, dict) else None
            if not isinstance(function, dict):
                continue
            name = function.get("name")
            if isinstance(name, str):
                tool_names.add(name)
            if name == "read_file":
                annotations = function.get("annotations")
                self.assertIsInstance(annotations, dict)
                self.assertTrue(annotations.get("readOnlyHint"))
                self.assertEqual(
                    annotations.get("m365ReadOnlyContract", {}).get("handler"),
                    "tools.file_tools._handle_read_file",
                )
            if name == "skill_view":
                self.assertNotIn("readOnlyHint", function)
                self.assertNotIn("m365ReadOnlyContract", function)
        self.assertIn("read_file", tool_names)
        if native:
            context = extra["m365_native_attachment_context"]
            self.assertIsInstance(context, dict)
            self.assertEqual(context.get("session_key"), session)
            self.assertEqual(context.get("schema"), "m365-hermes-native-attachment-context/v1")

    def _send_sdk_and_expect_fixture_boundary(self, payload: dict[str, Any]) -> dict[str, Any]:
        import httpx
        from openai import OpenAI

        client = OpenAI(
            api_key=self.gateway.api_key,
            base_url=self.gateway.base_url,
            http_client=httpx.Client(
                verify=ssl.create_default_context(cafile=str(self.gateway.cert)),
                timeout=15,
            ),
        )
        try:
            with self.assertRaises(Exception) as raised:
                client.chat.completions.create(**payload)
            error = raised.exception
            response = getattr(error, "response", None)
            self.assertIsNotNone(response, f"unexpected SDK failure type: {type(error).__name__}")
            body = response.json()
            self.assertEqual(body.get("error", {}).get("code"), "account_not_found")
        finally:
            client.close()
        return self.gateway.capture.last_chat()

    def _end_turn(self, session: str, turn: str) -> None:
        self.invoke_hook("on_session_end", session_id=session, turn_id=turn)

    def test_default_m365_plugin_manager_registration_and_order(self) -> None:
        manager = self.get_plugin_manager()
        plugins = {item["name"]: item for item in manager.list_plugins()}
        for name in ("m365-recall-provenance", "m365-native-attachments"):
            self.assertIn(name, plugins)
            self.assertTrue(plugins[name]["enabled"])
            self.assertIsNone(plugins[name]["error"])
        callbacks = manager._middleware.get("llm_request", [])
        self.assertEqual(
            [(callback.__module__, callback.__name__) for callback in callbacks],
            [
                ("hermes_plugins.m365_recall_provenance", "on_llm_request"),
                ("hermes_plugins.m365_native_attachments", "on_llm_request"),
            ],
        )
        native_module = manager._plugins["m365-native-attachments"].module
        recall_module = manager._plugins["m365-recall-provenance"].module
        self.assertIsNotNone(native_module)
        self.assertIsNotNone(recall_module)
        self.assertEqual(_sha256(Path(native_module.__file__)), _sha256(self.native_root / "__init__.py"))
        self.assertEqual(_sha256(Path(recall_module.__file__)), _sha256(self.recall_root / "__init__.py"))
        read_entry = self.registry.get_entry("read_file")
        self.assertIsNotNone(read_entry)
        self.assertIs(read_entry.handler, self.file_tools._handle_read_file)
        self.assertEqual(read_entry.handler.__module__, "tools.file_tools")
        self.assertIsNotNone(self.registry.get_entry("m365_native_attach"))

    def test_no_attachment_keeps_readback_contract_through_sdk(self) -> None:
        session, turn = "integration-no-attachment", "turn-no-attachment"
        try:
            _original, result = self._plugin_projection_request(
                session, turn, "Read the existing fixture and continue.", api_call_count=1
            )
            payload = result.payload
            self._assert_common_projection(payload, session, native=False, result_trace=result.trace)
            self.assertIn("caller_marker", payload["extra_body"])
            self.assertEqual(
                [item.get("source") for item in result.trace],
                ["m365-hermes-provenance", "m365-hermes-native-attachments"],
            )
            captured = self._send_sdk_and_expect_fixture_boundary(payload)
            self.assertEqual(self.gateway.capture.stage_requests, 0)
            self.assertTrue(captured["recall"])
            self.assertFalse(captured["native"])
            self.assertTrue(captured["session_key"])
            self.assertTrue(captured["read_only"])
        finally:
            self._end_turn(session, turn)

    def test_valid_attachment_reuse_preserves_projection_without_restaging(self) -> None:
        session, turn = "integration-attachment", "turn-attachment"
        try:
            fixture = self.root / "attachment-fixture.txt"
            fixture.write_text("synthetic attachment bytes\n", encoding="utf-8")
            request, initial = self._plugin_projection_request(
                session, turn, "Stage the original fixture and continue.", api_call_count=1
            )
            self.assertNotIn("m365_native_attachment_context", initial.payload["extra_body"])
            attach_entry = self.registry.get_entry("m365_native_attach")
            self.assertIsNotNone(attach_entry)
            tool_args = {
                "files": [
                    {
                        "local_path": str(fixture),
                        "attachment_id": "fixture-attachment",
                        "source_message_id": "fixture-message",
                    }
                ]
            }
            prepared = self.apply_tool_request_middleware(
                "m365_native_attach",
                tool_args,
                session_id=session,
                turn_id=turn,
                tool_call_id="fixture-attach-call",
            )
            tool_result = attach_entry.handler(
                prepared.payload,
                session_id=session,
                turn_id=turn,
                tool_call_id="fixture-attach-call",
            )
            parsed_tool_result = json.loads(tool_result)
            self.assertTrue(parsed_tool_result.get("ok"))
            stage_count = self.gateway.capture.stage_requests
            continuation_messages = [
                {"role": "user", "content": "Stage the original fixture and continue."},
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "fixture-attach-call",
                            "type": "function",
                            "function": {
                                "name": "m365_native_attach",
                                "arguments": json.dumps(tool_args),
                            },
                        }
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "fixture-attach-call",
                    "content": tool_result,
                },
            ]
            _request_again, result = self._plugin_projection_request(
                session,
                turn,
                "Stage the original fixture and continue.",
                messages=continuation_messages,
                api_call_count=2,
            )
            self._assert_common_projection(result.payload, session, native=True, result_trace=result.trace)
            self.assertEqual(self.gateway.capture.stage_requests, stage_count)
            context_digest = hashlib.sha256(
                json.dumps(
                    result.payload["extra_body"]["m365_native_attachment_context"],
                    sort_keys=True,
                ).encode("utf-8")
            ).hexdigest()
            recall_digest = hashlib.sha256(
                json.dumps(
                    result.payload["extra_body"]["m365_recall_provenance"],
                    sort_keys=True,
                ).encode("utf-8")
            ).hexdigest()
            _request_third, repeated = self._plugin_projection_request(
                session,
                turn,
                "Stage the original fixture and continue.",
                messages=continuation_messages,
                api_call_count=2,
            )
            repeated_digest = hashlib.sha256(
                json.dumps(
                    repeated.payload["extra_body"]["m365_native_attachment_context"],
                    sort_keys=True,
                ).encode("utf-8")
            ).hexdigest()
            self.assertEqual(repeated_digest, context_digest)
            repeated_recall_digest = hashlib.sha256(
                json.dumps(
                    repeated.payload["extra_body"]["m365_recall_provenance"],
                    sort_keys=True,
                ).encode("utf-8")
            ).hexdigest()
            self.assertEqual(repeated_recall_digest, recall_digest)
            self.assertEqual(self.gateway.capture.stage_requests, stage_count)
            captured = self._send_sdk_and_expect_fixture_boundary(result.payload)
            self.assertTrue(captured["recall"])
            self.assertTrue(captured["native"])
            self.assertTrue(captured["read_only"])
            self.assertEqual(captured["message_count"], len(continuation_messages))
            self.assertEqual(request["extra_body"], {"caller_marker": "integration-fixture", "session_key": session})
        finally:
            self._end_turn(session, turn)

    def test_read_modify_read_exact_path_reaches_gateway_ledger(self) -> None:
        session, turn = "integration-readback", "turn-readback"
        path = self.root / "readback-fixture.txt"
        read_args = {"path": str(path), "offset": 1, "limit": 8}
        old_content = "old synthetic value\n"
        new_content = "new synthetic value\n"
        path.write_text(old_content, encoding="utf-8")
        try:
            read_entry = self.registry.get_entry("read_file")
            write_entry = self.registry.get_entry("write_file")
            self.assertIsNotNone(read_entry)
            self.assertIsNotNone(write_entry)
            self.assertIs(read_entry.handler, self.file_tools._handle_read_file)
            old_result = read_entry.handler(copy.deepcopy(read_args), task_id="readback-task")
            write_result = write_entry.handler(
                {"path": read_args["path"], "content": new_content},
                task_id="readback-task",
                session_id=session,
            )
            new_result = read_entry.handler(copy.deepcopy(read_args), task_id="readback-task")
            self.assertIn("old synthetic value", old_result)
            self.assertIn("new synthetic value", new_result)
            self.assertNotIn("old synthetic value", new_result)
            self.assertNotEqual(hashlib.sha256(old_result.encode()).hexdigest(), hashlib.sha256(new_result.encode()).hexdigest())
            self.assertEqual(read_args, {"path": str(path), "offset": 1, "limit": 8})

            messages = [
                {"role": "user", "content": "Read, modify, and verify the fixture."},
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "read-old",
                            "type": "function",
                            "function": {"name": "read_file", "arguments": json.dumps(read_args)},
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "read-old", "content": old_result},
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "write-new",
                            "type": "function",
                            "function": {
                                "name": "write_file",
                                "arguments": json.dumps({"path": str(path), "content": new_content}),
                            },
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "write-new", "content": write_result},
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "read-new",
                            "type": "function",
                            "function": {"name": "read_file", "arguments": json.dumps(read_args)},
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "read-new", "content": new_result},
            ]
            _request, result = self._plugin_projection_request(
                session,
                turn,
                "Read, modify, and verify the fixture.",
                messages=messages,
                api_call_count=1,
            )
            self._assert_common_projection(result.payload, session, native=False, result_trace=result.trace)
            captured = self._send_sdk_and_expect_fixture_boundary(result.payload)
            self.assertTrue(captured["recall"])
            self.assertFalse(captured["native"])
            self.assertEqual(captured["message_count"], len(messages))
        finally:
            self._end_turn(session, turn)

    def test_active_turn_route_mismatch_fails_closed(self) -> None:
        session, turn = "integration-route-mismatch", "turn-route-mismatch"
        try:
            self._plugin_projection_request(
                session, turn, "Bind this turn to the M365 route.", api_call_count=1
            )
            _request, result = self._plugin_projection_request(
                session,
                turn,
                "Bind this turn to the M365 route.",
                provider="other-provider",
                api_call_count=2,
            )
            extra = result.payload.get("extra_body", {})
            context = extra.get("m365_native_attachment_context")
            self.assertIsInstance(context, dict)
            self.assertEqual(context.get("error"), "native_attachment_binding_invalid")
            self.assertNotIn("m365_recall_provenance", extra)
        finally:
            self._end_turn(session, turn)


if __name__ == "__main__":
    unittest.main(verbosity=2)
