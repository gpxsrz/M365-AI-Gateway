"""Consume exported Gateway HTTP/SSE bytes with pinned, unmodified Hermes and SDK.

M365_CAPACITY_FIXTURES=/tmp/capacity.json cargo test --locked --lib temporary_capacity_sdk_wire_contract
HERMES_AGENT_ROOT=/path/to/hermes python capacity_error_contract.py /tmp/capacity.json
Only MockTransport is used. No real provider calls, sleeps, or credential loading.
"""

from __future__ import annotations

import json
import contextlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace


def consume_image_errors(fixtures):
    """Use the unmodified conversation loop, including its normal three-attempt policy.

    The only substituted boundary is the HTTP client. No installed profiles,
    tools, credentials, or model requests participate in this offline fixture.
    """
    import httpx
    import openai
    from run_agent import AIAgent

    assert fixtures, "missing Gateway image fixtures"
    results = []
    for fixture in fixtures:
        calls = []
        hook_events = []
        retry_states = []

        def observe_retry(event_frame, event, _value):
            if (event == "return" and event_frame.f_code.co_name == "handle_api_error"
                    and event_frame.f_globals.get("__name__") == "agent.turn_api_error"):
                state = event_frame.f_locals["_retry"]
                retry_states.append(state.auto_recovery_cycles_used)

        def respond(request):
            calls.append(json.loads(request.content))
            return httpx.Response(fixture["status"], headers=fixture["headers"],
                                  content=fixture["body"].encode(), request=request)

        with openai.OpenAI(api_key="synthetic", base_url="https://gateway.invalid/hermes/v1",
                          http_client=httpx.Client(transport=httpx.MockTransport(respond))) as client:
            agent = None
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                try:
                    agent = AIAgent(
                        base_url="https://gateway.invalid/hermes/v1", api_key="synthetic",
                        provider="m365", api_mode="chat_completions", model="gpt-5.6-terra",
                        max_iterations=2, quiet_mode=True, platform="test",
                        session_id=f"image-contract-{fixture['stream']}",
                        skip_context_files=True, skip_memory=True, skip_background_review=True,
                        load_soul_identity=False,
                    )
                    agent._disable_streaming = not fixture["stream"]
                    assert not agent._has_pending_fallback()
                    assert agent._api_max_retries == 3
                    assert agent._auto_recovery_cycles == 5
                    agent._create_request_openai_client = lambda **kwargs: client
                    original_hook = agent._invoke_api_request_error_hook

                    def capture_hook(**kwargs):
                        hook_events.append({key: kwargs[key] for key in ("reason", "retryable", "max_retries")})
                        return original_hook(**kwargs)

                    agent._invoke_api_request_error_hook = capture_hook

                    def forbidden_recovery(*args, **kwargs):
                        raise AssertionError("format rejection attempted compression or image shrinking")

                    agent._compress_context = forbidden_recovery
                    agent._try_shrink_image_parts_in_messages = forbidden_recovery
                    previous_profile = sys.getprofile()
                    sys.setprofile(observe_retry)
                    result = agent.run_conversation(
                        "Read the attached synthetic image.",
                        conversation_history=[{"role": "user", "content": [
                            {"type": "text", "text": "Synthetic attachment"},
                            {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFklEQVR4nGP8z8DAwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg=="}},
                        ]}, {"role": "assistant", "content": "Attachment received."}],
                        task_id="synthetic-image-format-contract",
                    )
                    assert len(calls) == 1, len(calls)
                    assert bool(calls[0].get("stream", False)) == fixture["stream"]
                    assert "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFklEQVR4nGP8z8DAwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg==" in json.dumps(calls[0])
                    assert result.get("failure_reason") == "format_error", result.get("failure_reason")
                    assert result.get("failed") and not result.get("failure_retryable")
                    assert hook_events == [{"reason": "format_error", "retryable": False, "max_retries": 3}], hook_events
                    assert not getattr(agent, "_image_rejecting_models", set())
                    assert retry_states == [0], retry_states
                    results.append({"stream": fixture["stream"], "requests": len(calls),
                                    "code": fixture.get("code", "invalid_bmp"),
                                    "reason": result["failure_reason"], "compression": 0,
                                    "auto_recovery": retry_states[0], "fallback_configured": False})
                finally:
                    if "previous_profile" in locals():
                        sys.setprofile(previous_profile)
                    if agent is not None:
                        agent.close()
    print(json.dumps({"result": "PASS", "sdk": openai.__version__, "cases": results}, sort_keys=True))


def main() -> None:
    fixtures = json.loads(Path(sys.argv[1]).read_text())
    agent_root = Path(os.environ["HERMES_AGENT_ROOT"]).resolve()
    expected = "f97608f178d1ffeca59860195ab7da295f7c8e5f"
    assert subprocess.check_output(
        ["git", "-C", str(agent_root), "rev-parse", "HEAD"], text=True
    ).strip() == expected
    assert not subprocess.check_output(
        ["git", "-C", str(agent_root), "status", "--porcelain", "--untracked-files=all"], text=True
    ).strip(), "Hermes source must be unmodified"

    with tempfile.TemporaryDirectory(prefix="m365-capacity-consumer-") as root:
        os.environ["HERMES_HOME"] = root
        # Use the supported profile setting so synthetic image content reaches
        # the real request builder without a models.dev network capability probe.
        if all(fixture["kind"] == "image_format" for fixture in fixtures):
            Path(root, "config.yaml").write_text("model:\n  supports_vision: true\n")
        os.environ["PYTHONDONTWRITEBYTECODE"] = "1"
        sys.dont_write_bytecode = True
        sys.path.insert(0, str(agent_root))

        def no_network(event, _args):
            if event in {"socket.connect", "socket.getaddrinfo"}:
                raise AssertionError("offline consumer attempted network access")

        sys.addaudithook(no_network)
        import httpx
        import openai
        from agent.chat_completion_helpers import _StreamingCall
        from agent.error_classifier import FailoverReason, classify_api_error
        from agent.turn_recovery import compute_error_backoff
        from agent.turn_recovery_autorecover import ladder_eligible, ladder_wait_seconds

        assert openai.__version__ == "2.24.0"
        if all(fixture["kind"] == "image_format" for fixture in fixtures):
            consume_image_errors(fixtures)
            return
        results = []
        for fixture in fixtures:
            calls = []

            def respond(request):
                calls.append(request.url.path)
                return httpx.Response(fixture["status"], headers=fixture["headers"],
                                      content=fixture["body"].encode(), request=request)

            delivered = ""
            caught = None
            with openai.OpenAI(api_key="synthetic-fixture-key", base_url="https://gateway.invalid/v1",
                               max_retries=0, http_client=httpx.Client(transport=httpx.MockTransport(respond))) as client:
                try:
                    response = client.chat.completions.create(
                        model="fixture", messages=[{"role": "user", "content": "fixture"}],
                        stream=fixture["stream"],
                    )
                    if fixture["stream"]:
                        for chunk in response:
                            for choice in chunk.choices:
                                delivered += choice.delta.content or ""
                except openai.APIError as error:
                    caught = error
            assert caught is not None, "SDK must raise a parsed API error"
            assert len(calls) == 1, "consumer must not replay inside SDK"
            classified = classify_api_error(caught)
            hard = fixture["kind"] == "hard429"
            assert classified.reason == (FailoverReason.rate_limit if hard else FailoverReason.overloaded), (
                fixture["route"], fixture["stream"], fixture["kind"], type(caught).__name__, classified.reason.value
            )
            partial = fixture["stream"] and fixture["kind"] == "partial_capacity"
            assert delivered == ("Already delivered answer." if partial else "")
            agent = SimpleNamespace(
                _auto_recovery_cycles=5, _current_streamed_assistant_text=delivered,
                _has_content_after_think_block=lambda text: bool(text.strip()),
                _buffer_diagnostic_status=lambda text: None,
                _emit_diagnostic_status=lambda text: None,
                _emit_diagnostic_wait=lambda text: None,
                _client_log_context=lambda: "offline fixture",
                _is_provider_stream_parse_error=lambda error: False,
            )
            eligible = ladder_eligible(agent, classified)
            assert eligible == (not hard and not partial)
            waits = [compute_error_backoff(
                agent, caught, retry_count=count, max_retries=3, is_rate_limited=hard,
                is_zai_coding_overload=False, base_url="https://gateway.invalid/v1", model="fixture",
            ) for count in (1, 2)]
            if not hard:
                assert waits == [60, 60], waits
                assert ladder_wait_seconds(1, caught) == 60
            elif not fixture["stream"]:
                assert waits == [600, 600], "existing Hermes hard429 cap must remain unchanged"
            if partial:
                # Exercise the stock stream handler as well as the post-exhaustion guard.
                call = _StreamingCall.__new__(_StreamingCall)
                call.agent = agent
                call._request_cancelled = {"value": False}
                call.deltas_were_sent = {"yes": True}
                call.provider_tool_in_flight = {"yes": False}
                call.result = {}
                assert call._handle_stream_error(caught, 0, 3) is False
                assert call.result["error"] is caught
            results.append({"route": fixture["route"], "stream": fixture["stream"],
                            "kind": fixture["kind"], "exception": type(caught).__name__,
                            "reason": classified.reason.value, "waits": waits,
                            "auto_recovery_eligible": eligible, "delivered_chars": len(delivered)})
        assert len(results) == 18
        print(json.dumps({"result": "PASS", "hermes": expected, "sdk": openai.__version__,
                          "cases": results}, sort_keys=True))


if __name__ == "__main__":
    main()
