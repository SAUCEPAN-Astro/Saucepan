"""Unit tests for worker/orchestrator.py — the pull-based bridge worker.

Network/subprocess calls (urllib, compute grading, storage backend) are
mocked out; nothing here hits a real network or process.
"""

from __future__ import annotations

import json
import io
import urllib.error
from unittest.mock import MagicMock, patch

import pytest

from worker import orchestrator


def test_fetch_pending_jobs_reads_from_json_file(tmp_path, monkeypatch):
    jobs_file = tmp_path / "jobs.json"
    jobs_file.write_text(json.dumps([{"task_id": 1}]))
    monkeypatch.setenv("WORKER_JOBS_JSON", str(jobs_file))
    jobs = orchestrator.fetch_pending_jobs("http://hot.test", "token")
    assert jobs == [{"task_id": 1}]
    assert orchestrator.fetch_pending_jobs("http://hot.test", "token") == []


@patch("worker.orchestrator.http_json.request_json")
def test_fetch_pending_jobs_polls_http_api(mock_http, monkeypatch):
    monkeypatch.delenv("WORKER_JOBS_JSON", raising=False)
    mock_http.return_value = {"jobs": [{"task_id": 2}]}
    jobs = orchestrator.fetch_pending_jobs("http://hot.test", "tok")
    assert jobs == [{"task_id": 2}]
    mock_http.assert_called_once_with("GET", "http://hot.test/api/v1/worker/pending", token="tok")


@patch("worker.orchestrator.http_json.request_json", side_effect=RuntimeError("network down"))
def test_fetch_pending_jobs_returns_empty_on_failure(mock_http, monkeypatch):
    monkeypatch.delenv("WORKER_JOBS_JSON", raising=False)
    assert orchestrator.fetch_pending_jobs("http://hot.test", "tok") == []


@patch("worker.orchestrator.http_json.request_json")
def test_grade_local_uses_compute_http_when_configured(mock_http, monkeypatch, tmp_path):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_http.return_value = {"headline": 5}
    result = orchestrator.grade_local(tmp_path / "a.fits", {"task_id": 1})
    assert result == {"headline": 5}
    mock_http.assert_called_once()
    assert mock_http.call_args[0][0] == "POST"
    assert mock_http.call_args[0][1] == "http://compute.test/v1/grade"


def _fake_http_response(body: bytes):
    response = MagicMock()
    response.__enter__.return_value = response
    response.read.return_value = body
    return response


@patch("http_json.urllib.request.urlopen")
def test_fetch_pending_jobs_http_request_preserves_get_auth_timeout_and_empty_body(
    mock_urlopen, monkeypatch
):
    monkeypatch.delenv("WORKER_JOBS_JSON", raising=False)
    mock_urlopen.return_value = _fake_http_response(b"")

    assert orchestrator.fetch_pending_jobs("http://hot.test/", "tok") == []
    request = mock_urlopen.call_args.args[0]
    assert request.full_url == "http://hot.test/api/v1/worker/pending"
    assert request.get_method() == "GET"
    assert request.get_header("Authorization") == "Bearer tok"
    assert mock_urlopen.call_args.kwargs["timeout"] == 120


@patch("http_json.urllib.request.urlopen")
def test_grade_local_http_request_preserves_post_auth_timeout_and_error(
    mock_urlopen, monkeypatch, tmp_path
):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test/")
    error = urllib.error.HTTPError(
        url="http://compute.test/v1/grade",
        code=502,
        msg="Bad Gateway",
        hdrs=None,
        fp=io.BytesIO(b"compute unavailable"),
    )
    mock_urlopen.side_effect = error

    with pytest.raises(RuntimeError, match=r"HTTP 502 request failed"):
        orchestrator.grade_local(tmp_path / "a.fits", {"task_id": 1})
    request = mock_urlopen.call_args.args[0]
    assert request.get_method() == "POST"
    assert request.get_header("Authorization") is None
    assert json.loads(request.data) == {
        "staged_path": str(tmp_path / "a.fits"),
        "task_context": {"task_id": 1},
        "post_ingest": True,
    }
    assert mock_urlopen.call_args.kwargs["timeout"] == 120


@patch("worker.orchestrator.get_storage_backend")
@patch("worker.orchestrator.grade_local")
def test_run_live_job_pulls_and_grades_each_object_key(
    mock_grade, mock_get_backend, tmp_path, monkeypatch
):
    monkeypatch.setenv("WORKER_SCRATCH", str(tmp_path / "scratch"))
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    mock_get_backend.return_value = backend
    mock_grade.return_value = {"headline": 42}

    job = {"task_id": 7, "campaign_id": "c1", "object_keys": ["c1/a.fits", "c1/b.fits"]}
    result = orchestrator.run_live_job(job)

    assert result["task_id"] == 7
    assert result["role"] == "process"
    assert result["pulled"] == ["c1/a.fits", "c1/b.fits"]
    assert len(result["grades"]) == 2
    assert backend.download_object.call_count == 2
    assert not (tmp_path / "scratch" / "task_7").exists()


@patch("worker.orchestrator.get_storage_backend")
@patch("worker.orchestrator.grade_local")
def test_run_live_job_does_not_assume_emulator(
    mock_grade, mock_get_backend, tmp_path, monkeypatch
):
    monkeypatch.setenv("WORKER_SCRATCH", str(tmp_path / "scratch"))
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    mock_get_backend.return_value = backend
    mock_grade.return_value = {}

    orchestrator.run_live_job({"task_id": 8, "object_key": "c1/a.fits"})

    context = mock_grade.call_args.args[1]
    assert context["allow_emulator"] is False
    assert context["telescope_is_emulator"] is False


def test_run_live_job_raises_without_object_keys():
    with pytest.raises(ValueError, match="job missing object_key"):
        orchestrator.run_live_job({"task_id": 1})


@patch("worker.orchestrator.get_storage_backend")
@patch("worker.orchestrator.grade_local")
def test_run_live_job_accepts_singular_object_key(
    mock_grade, mock_get_backend, tmp_path, monkeypatch
):
    monkeypatch.setenv("WORKER_SCRATCH", str(tmp_path / "scratch"))
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    mock_get_backend.return_value = backend
    mock_grade.return_value = {}

    job = {"task_id": 1, "object_key": "c1/only.fits"}
    result = orchestrator.run_live_job(job)
    assert result["pulled"] == ["c1/only.fits"]


@patch("worker.orchestrator.normalize_frame", side_effect=lambda path: path)
@patch("worker.orchestrator.grade_local", return_value={})
@patch("worker.orchestrator.get_storage_backend")
def test_run_live_job_separates_same_basename_object_keys(
    mock_get_backend, mock_grade, _mock_normalize, tmp_path, monkeypatch
):
    monkeypatch.setenv("WORKER_SCRATCH", str(tmp_path / "scratch"))
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    mock_get_backend.return_value = backend

    orchestrator.run_live_job(
        {"task_id": 9, "object_keys": ["north/target.fits", "south/target.fits"]}
    )

    downloaded_paths = [call.args[2] for call in backend.download_object.call_args_list]
    assert downloaded_paths[0] != downloaded_paths[1]
    upload_ids = [call.args[1]["upload_id"] for call in mock_grade.call_args_list]
    assert upload_ids[0] != upload_ids[1]


def test_run_job_routes_to_run_live_job(monkeypatch):
    with patch("worker.orchestrator.run_live_job", return_value={"ok": True}) as mock_live:
        result = orchestrator.run_job({"task_id": 1})
    assert result == {"ok": True}
    mock_live.assert_called_once()


@patch("worker.orchestrator.http_json.request_json")
def test_requeue_failed_remote_job(mock_http, monkeypatch):
    monkeypatch.delenv("WORKER_JOBS_JSON", raising=False)
    orchestrator.requeue_failed_job("http://hot.test", "tok", {"task_id": 9})
    mock_http.assert_called_once_with(
        "POST", "http://hot.test/api/v1/worker/enqueue", {"task_id": 9}, token="tok"
    )


@patch("worker.orchestrator.publish_stack_product", return_value="c1/7/stack-7.fits")
@patch("worker.orchestrator.stack_frames", return_value={"n_frames_used": 2})
@patch("worker.orchestrator.normalize_frame")
@patch("worker.orchestrator.grade_local", return_value={"headline": 42})
@patch("worker.orchestrator.get_storage_backend")
def test_run_live_job_normalizes_and_publishes_explicit_stack(
    mock_get_backend,
    mock_grade,
    mock_normalize,
    mock_stack,
    mock_publish,
    tmp_path,
    monkeypatch,
):
    monkeypatch.setenv("WORKER_SCRATCH", str(tmp_path / "scratch"))
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    mock_get_backend.return_value = backend
    mock_normalize.side_effect = lambda path: path.with_suffix(".normalized.fits")

    result = orchestrator.run_live_job(
        {
            "task_id": 7,
            "campaign_id": "c1",
            "product_mode": "stack",
            "object_keys": ["c1/a.fits", "c1/b.fits"],
        }
    )

    assert result["stack"]["object_key"] == "c1/7/stack-7.fits"
    assert mock_normalize.call_count == 2
    mock_stack.assert_called_once()
    mock_publish.assert_called_once()
    assert mock_grade.call_count == 2
