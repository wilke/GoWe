"""Parser tests against the RECORDED Workspace responses shared with the Go and
TypeScript clients (pkg/bvbrc/testdata/workspace/). The fixtures are raw
JSON-RPC ``result`` values captured from the production service, so these tests
pin the Python parser to what the service actually emits.

Run from ``mcp-servers/python`` with ``pytest``. Requires Python >= 3.10 plus the
package's dependencies: ``pip install -e .`` (or ``pip install httpx "mcp<2" pytest``
and ``PYTHONPATH=src pytest``). ``mcp`` must be < 2 for now — importing the package
pulls in ``server.py``, which still uses the 1.x ``Server.list_tools`` decorator
(see issue #176).
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

from bvbrc_mcp.client import (
    BVBRCClient,
    _parse_download_urls,
    _parse_workspace_get_entry,
    _parse_workspace_object,
)

FIXTURE_DIR = Path(__file__).resolve().parents[3] / "pkg" / "bvbrc" / "testdata" / "workspace"

FOLDER = "/awilke@bvbrc/home/.gowe-fixtures/cd0bcb45-449e-4aa0-a8b0-88dff3c633a7"
INLINE = f"{FOLDER}/inline.txt"
SHOCK = f"{FOLDER}/shock.txt"
NODE = "https://p3.theseed.org/services/shock_api/node/1c66310d-929b-4d5d-85dc-adfdf5c7007d"
OWNER = "awilke@bvbrc"

FIXTURES = [
    "ls.json",
    "get.json",
    "get_metadata_only.json",
    "get_download_url.json",
    "update_auto_meta.json",
    "create_inline.json",
    "create_upload_node.json",
]


def fixture(name: str):
    return json.loads((FIXTURE_DIR / name).read_text())


def replay_client(monkeypatch: pytest.MonkeyPatch, name: str) -> tuple[BVBRCClient, list]:
    """A client whose Workspace RPC replays one fixture, recording each call."""
    result = fixture(name)
    calls: list[tuple[str, list]] = []
    client = BVBRCClient(token="fixture-token", workspace_url="http://replay.invalid/ws")

    def fake_call(method: str, params: list):
        calls.append((method, params))
        return result

    monkeypatch.setattr(client, "_call_workspace", fake_call)
    return client, calls


def assert_inline_meta(obj) -> None:
    assert obj.name == "inline.txt"
    assert obj.path == INLINE, "path = [2] + [0]"
    assert obj.type == "txt"
    assert obj.owner == OWNER, "owner = [5]"
    assert obj.id == "1161B900-A0EF-11F1-ACAA-E63D5F7A854C"
    assert obj.size == 6 and isinstance(obj.size, int)
    assert obj.user_permission == "o"
    assert obj.global_permission == "n"
    assert obj.shock_url is None, "inline object has no Shock URL"
    assert obj.error is None, "impl emits 12 slots — no error slot"
    assert obj.auto_metadata == {"is_folder": 0}


def assert_shock_meta(obj) -> None:
    assert obj.name == "shock.txt"
    assert obj.path == SHOCK, "path = [2] + [0]"
    assert obj.owner == OWNER
    assert obj.size == 12
    assert obj.user_permission == "o"
    assert obj.global_permission == "n"
    assert obj.shock_url == NODE, "shock_url = [11]"
    assert obj.error is None


def test_fixture_tuples_have_twelve_slots_and_numeric_size() -> None:
    tuples = fixture("ls.json")[0][FOLDER]
    assert len(tuples) == 2
    for tup in tuples:
        assert len(tup) == 12, f"{tup[0]} has {len(tup)} slots"
        assert isinstance(tup[6], int)


def test_parse_workspace_object_recorded_ls() -> None:
    inline, shock = (_parse_workspace_object(t) for t in fixture("ls.json")[0][FOLDER])
    assert_inline_meta(inline)
    assert_shock_meta(shock)
    assert inline.data is None and shock.data is None


def test_workspace_ls(monkeypatch: pytest.MonkeyPatch) -> None:
    client, calls = replay_client(monkeypatch, "ls.json")
    got = client.workspace_ls([FOLDER])
    assert calls == [("Workspace.ls", [{"paths": [FOLDER]}])]
    assert list(got) == [FOLDER]
    assert_inline_meta(got[FOLDER][0])
    assert_shock_meta(got[FOLDER][1])


def test_workspace_get_pairs_carry_data(monkeypatch: pytest.MonkeyPatch) -> None:
    client, calls = replay_client(monkeypatch, "get.json")
    got = client.workspace_get([INLINE, SHOCK])
    assert calls == [("Workspace.get", [{"objects": [INLINE, SHOCK]}])]
    assert len(got) == 2
    assert_inline_meta(got[0])
    assert got[0].data == "hello\n", "inline object: data is the content"
    assert_shock_meta(got[1])
    assert got[1].data == NODE, "Shock-backed object: data is the node URL, not the bytes"


def test_workspace_get_metadata_only(monkeypatch: pytest.MonkeyPatch) -> None:
    client, calls = replay_client(monkeypatch, "get_metadata_only.json")
    got = client.workspace_get([INLINE, SHOCK], metadata_only=True)
    assert calls[0][1] == [{"objects": [INLINE, SHOCK], "metadata_only": True}]
    assert_inline_meta(got[0])
    assert_shock_meta(got[1])
    assert got[0].data is None and got[1].data is None


def test_parse_get_entry_accepts_bare_tuple() -> None:
    bare = _parse_workspace_get_entry(fixture("ls.json")[0][FOLDER][1])
    assert_shock_meta(bare)
    assert bare.data is None


def test_workspace_create_replies(monkeypatch: pytest.MonkeyPatch) -> None:
    client, calls = replay_client(monkeypatch, "create_inline.json")
    obj = client.workspace_create(INLINE, "txt", "hello\n")
    assert calls[0][1] == [{"objects": [[INLINE, "txt", {}, "hello\n"]]}]
    assert_inline_meta(obj)

    node = _parse_workspace_object(fixture("create_upload_node.json")[0][0])
    assert node.path == SHOCK
    assert node.shock_url == NODE, "upload-node create returns the Shock URL in [11]"
    assert node.size == 0, "nothing PUT yet"


def test_update_auto_meta_reply_reports_stored_size() -> None:
    reply = fixture("update_auto_meta.json")
    assert len(reply) == 1 and len(reply[0]) == 1
    obj = _parse_workspace_object(reply[0][0])
    assert_shock_meta(obj)
    assert "inspection_started" in obj.auto_metadata


def test_get_download_url_flat_list_with_null_for_folder(monkeypatch: pytest.MonkeyPatch) -> None:
    raw = fixture("get_download_url.json")
    assert len(raw) == 1, "wrapped exactly once by JSON-RPC"
    assert len(raw[0]) == 3
    assert raw[0][2] is None, "folder -> null"

    parsed = _parse_download_urls([INLINE, SHOCK, FOLDER], raw)
    assert list(parsed) == [INLINE, SHOCK], "folder omitted"
    assert parsed[INLINE].endswith("/inline.txt")
    assert parsed[SHOCK].endswith("/shock.txt")

    client, calls = replay_client(monkeypatch, "get_download_url.json")
    got = client.workspace_get_download_url([INLINE, SHOCK, FOLDER])
    assert calls == [("Workspace.get_download_url", [{"objects": [INLINE, SHOCK, FOLDER]}])]
    assert got == parsed
    # This is what server.py's workspace_download_url handler does with it.
    assert got.get(FOLDER) is None
    assert got.get(INLINE) == parsed[INLINE]


def test_parse_download_urls_edge_cases() -> None:
    assert _parse_download_urls(["/a", "/b"], [["u", None]]) == {"/a": "u"}
    assert _parse_download_urls(["/a", "/b"], [["u"]]) == {"/a": "u"}, "short list tolerated"
    assert _parse_download_urls(["/a"], []) == {}
    assert _parse_download_urls(["/a"], None) == {}
    assert _parse_download_urls(["/a"], [[""]]) == {}, "empty string is no URL"


@pytest.mark.parametrize("name", FIXTURES)
def test_fixtures_contain_no_token_material(name: str) -> None:
    text = (FIXTURE_DIR / name).read_text()
    assert not re.search(r"un=|tokenid=|expiry=|sig=", text), f"{name} carries token fields"
