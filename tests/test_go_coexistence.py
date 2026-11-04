import json

import pytest


def test_python_does_not_apply_go_initialization_draft(store):
    store.directory.mkdir()
    marker = store.directory / "go-initialization.json"
    marker.write_text('{"private":"draft-secret"}', encoding="utf-8")
    with pytest.raises(ValueError, match="Go initialization draft") as caught:
        with store.lock():
            pytest.fail("Python took over an unapplied Go initialization")
    assert "draft-secret" not in str(caught.value)
    marker.unlink()
    with store.lock():
        pass


@pytest.mark.parametrize("value", [
    {"version": 1, "status": "preparing"},
    {"version": 1, "status": "running"},
    {"version": 1, "status": "failed"},
    {"version": 2, "status": "complete"},
    {"version": 1, "status": "unknown"},
    [],
    None,
])
def test_python_refuses_unfinished_or_unknown_go_operation(store, value):
    store.directory.mkdir()
    marker = store.directory / "go-operation.json"
    marker.write_text(json.dumps(value), encoding="utf-8")
    with pytest.raises(ValueError, match="Go controller operation"):
        with store.lock():
            pytest.fail("Python acquired write ownership during Go recovery")
    # The failed guard must release its OS lock.
    marker.unlink()
    with store.lock():
        pass


@pytest.mark.parametrize("status", ["complete", "cancelled"])
def test_python_accepts_terminal_go_operation(store, status):
    store.directory.mkdir()
    (store.directory / "go-operation.json").write_text(
        json.dumps({"version": 1, "status": status}), encoding="utf-8")
    with store.lock():
        pass


def test_corrupt_go_operation_does_not_echo_secret(store):
    store.directory.mkdir()
    (store.directory / "go-operation.json").write_text(
        '{"status":"private-marker-secret', encoding="utf-8")
    with pytest.raises(ValueError, match="Cannot read Go operation") as caught:
        with store.lock():
            pytest.fail("corrupt Go state accepted")
    assert "private-marker-secret" not in str(caught.value)
