"""Mandatory real-Hermes/SDK checkpoint regression, launched by the Rust fixture.

Only synthetic files and a loopback Gateway are used. Missing prerequisites fail.
This imports the pinned Hermes send path and real file handlers, not a copied
implementation of json.dumps or a simulated caller tool.
"""

from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import subprocess
import sys


def main() -> None:
    agent_root = Path(os.environ["HERMES_AGENT_ROOT"]).resolve()
    expected = "641f7c810449d9af5c21b0a5ee33b29b192b4117"
    actual = subprocess.check_output(["git", "-C", str(agent_root), "rev-parse", "HEAD"], text=True).strip()
    if actual != expected:
        raise RuntimeError("pinned Hermes fixture identity mismatch")
    if subprocess.check_output(
        ["git", "-C", str(agent_root), "status", "--porcelain", "--untracked-files=all"], text=True
    ).strip():
        raise RuntimeError("pinned Hermes fixture has modified or untracked source")
    root = Path(os.environ["M365_IDENTITY_FIXTURE_ROOT"]).resolve()
    os.environ["HERMES_HOME"] = str(root / "hermes-home")
    os.environ["TERMINAL_ENV"] = "local"
    os.chdir(root)
    sys.path.insert(0, str(agent_root))

    from agent.conversation_loop import _clone_message_for_send, _canonicalize_api_tool_calls
    from tools.file_tools import _handle_read_file, _handle_write_file
    from openai import APIStatusError, OpenAI

    client = OpenAI(api_key=os.environ["M365_IDENTITY_FIXTURE_KEY"],
                    base_url=os.environ["M365_IDENTITY_FIXTURE_URL"], max_retries=0, timeout=30)
    tools = json.loads((root / "sdk-tools.json").read_text(encoding="utf-8"))
    path = root / "fixture_中文😀.txt"
    path.write_text("original 中文😀\n", encoding="utf-8")
    history = [{"role": "user", "content": "Read the synthetic file, modify it once, then read and verify it."}]
    dispatches = []
    reserialized = False
    denials = 0

    def send(messages, normalize=True, prove_unicode=False):
        nonlocal reserialized
        before = copy.deepcopy(messages)
        wire = [_clone_message_for_send(message) for message in messages]
        if normalize:
            _canonicalize_api_tool_calls(wire)
        if prove_unicode:
            original = before[1]["tool_calls"][0]["function"]["arguments"]
            encoded = wire[1]["tool_calls"][0]["function"]["arguments"]
            assert "中文😀" in original
            assert r"\u4e2d\u6587\ud83d\ude00" in encoded
            assert original != encoded and json.loads(original) == json.loads(encoded)
        assert messages == before, "the real Hermes send path must not mutate saved history"
        response = client.chat.completions.create(model="gpt-5.6-reasoning", messages=wire,
            tools=tools, tool_choice="auto", extra_body={"session_key": "identity-sdk-fixture"})
        reserialized |= prove_unicode
        return response

    for step in range(4):
        response = send(history, prove_unicode=step == 1)
        message = response.choices[0].message
        if step == 3:
            assert not message.tool_calls
            assert "IDENTITY_SDK_COMPLETE" in (message.content or "")
            break
        assert message.tool_calls and len(message.tool_calls) == 1
        call = message.tool_calls[0]
        expected_name = ["read_file", "write_file", "read_file"][step]
        assert call.function.name == expected_name
        arguments = json.loads(call.function.arguments)
        assert Path(arguments["path"]).resolve() == path, "fixture tools may touch only their own file"
        if step == 1:
            result = _handle_write_file(arguments, task_id="identity-fixture", session_id="identity-sdk-fixture")
        else:
            result = _handle_read_file(arguments, task_id="identity-fixture")
            assert ("original 中文😀" if step == 0 else "modified 中文😀") in result, result
        dispatches.append(expected_name)
        history.append(message.model_dump(exclude_none=True))
        history.append({"role": "tool", "tool_call_id": call.id, "content": result})

        if step == 0:
            assert "中文😀" in call.function.arguments, "the fixture must expose literal Unicode before Hermes serialization"
            for change in ["path", "type", "value", "id", "role", "name"]:
                changed = copy.deepcopy(history)
                tc = changed[1]["tool_calls"][0]
                if change in {"path", "type"}:
                    args = json.loads(tc["function"]["arguments"])
                    args["path" if change == "path" else "limit"] = str(path) + ".changed" if change == "path" else 8.0
                    tc["function"]["arguments"] = json.dumps(args, ensure_ascii=False)
                elif change == "value":
                    args = json.loads(tc["function"]["arguments"])
                    args["limit"] = 9
                    tc["function"]["arguments"] = json.dumps(args, ensure_ascii=False)
                elif change == "id":
                    tc["id"] += "-changed"
                elif change == "role":
                    changed[1]["role"] = "user"
                else:
                    tc["function"]["name"] = "write_file"
                try:
                    send(changed)
                except APIStatusError as error:
                    assert error.status_code in {400, 409}, error.status_code
                    denials += 1
                else:
                    raise AssertionError("changed accepted history reached success")
            # Duplicate-key evidence must reach admission intact; Hermes's parser
            # cannot reconstruct keys that a caller has already discarded.
            changed = copy.deepcopy(history)
            changed[1]["tool_calls"][0]["function"]["arguments"] = '{"x":1,"x":2}'
            try:
                send(changed, normalize=False)
            except APIStatusError as error:
                assert error.status_code == 400, error.status_code
                denials += 1
            else:
                raise AssertionError("ambiguous arguments reached success")

    assert reserialized, "test never exercised Hermes argument reserialization"
    assert dispatches == ["read_file", "write_file", "read_file"]
    assert path.read_text(encoding="utf-8") == "modified 中文😀\n"
    assert denials == 7
    client.close()
    print(json.dumps({"result": "PASS", "tool_dispatches": 3, "writes": 1, "denials": denials}))


if __name__ == "__main__":
    main()
