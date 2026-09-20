import json
import threading
import time
from typing import Any
from unittest import mock

import pytest

from determined import core
from determined.common.api import errors
from determined.core import _train


class FakeSession:
    def __init__(self) -> None:
        self.calls = []
        self.closed = False
        self.entered = threading.Event()
        self.release = threading.Event()
        self.block_first = False
        self.errors = []

    def with_retry(self, retries: int) -> "FakeSession":
        assert retries == 0
        return self

    def post(self, path: str, *, data: str, timeout: int) -> None:
        self.calls.append((path, json.loads(data), timeout))
        self.entered.set()
        if self.block_first and len(self.calls) == 1:
            self.release.wait()
        if self.errors:
            raise self.errors.pop(0)

    def close(self) -> None:
        self.closed = True


def reporter(session: FakeSession, **kwargs: Any) -> _train._ProgressReporter:
    return _train._ProgressReporter(session, 7, request_timeout=1, **kwargs)  # type: ignore


def wait_for_calls(session: FakeSession, count: int) -> None:
    deadline = time.monotonic() + 1
    while len(session.calls) < count and time.monotonic() < deadline:
        time.sleep(0.001)
    assert len(session.calls) == count


def test_progress_is_nonblocking_and_coalesces_latest() -> None:
    session = FakeSession()
    session.block_first = True
    progress = reporter(session, close_timeout=1)
    progress.publish(0.1)
    assert session.entered.wait(1)

    started = time.monotonic()
    progress.publish(0.2)
    progress.publish(0.3)
    assert time.monotonic() - started < 0.1
    session.release.set()
    progress.close()

    assert [call[1]["progress"] for call in session.calls] == [0.1, 0.3]
    assert session.closed


def test_progress_retries_transport_errors_and_disables_on_permanent_error(
    caplog: pytest.LogCaptureFixture,
) -> None:
    session = FakeSession()
    session.errors = [
        errors.MasterNotFoundException("offline"),
        errors.MasterNotFoundException("offline"),
    ]
    progress = reporter(session, max_attempts=3, retry_backoff=0, close_timeout=1)
    progress.publish(0.4)
    wait_for_calls(session, 3)
    progress.close()
    assert len(session.calls) == 3

    failed_session = FakeSession()
    failed_session.errors = [errors.UnauthenticatedException()]
    failed = reporter(failed_session, retry_backoff=0, close_timeout=1)
    failed.publish(0.5)
    wait_for_calls(failed_session, 1)
    failed.close()
    failed.publish(0.6)
    assert len(failed_session.calls) == 1
    assert "permanent error" in caplog.text


def test_progress_retry_uses_latest_value() -> None:
    session = FakeSession()
    session.block_first = True
    session.errors = [errors.MasterNotFoundException("offline")]
    progress = reporter(session, max_attempts=2, retry_backoff=0, close_timeout=1)
    progress.publish(0.1)
    assert session.entered.wait(1)
    progress.publish(0.8)
    session.release.set()
    wait_for_calls(session, 2)
    progress.close()

    assert [call[1]["progress"] for call in session.calls] == [0.1, 0.8]


def test_progress_retry_exhaustion_drops_value_without_disabling() -> None:
    session = FakeSession()
    session.errors = [
        errors.MasterNotFoundException("offline"),
        errors.MasterNotFoundException("offline"),
    ]
    progress = reporter(session, max_attempts=2, retry_backoff=0, close_timeout=1)
    progress.publish(0.2)
    wait_for_calls(session, 2)
    progress.publish(0.7)
    wait_for_calls(session, 3)
    progress.close()
    assert session.calls[-1][1]["progress"] == 0.7


def test_progress_close_is_bounded_and_validation_is_synchronous(
    caplog: pytest.LogCaptureFixture,
) -> None:
    session = FakeSession()
    session.block_first = True
    progress = reporter(session, max_attempts=1, close_timeout=0.01)
    with pytest.raises(ValueError, match="between 0 and 1"):
        progress.publish(2.0)
    progress.publish(0.6)
    assert session.entered.wait(1)
    progress.publish(0.9)

    started = time.monotonic()
    progress.close()
    assert time.monotonic() - started < 0.2
    assert "shutdown deadline" in caplog.text
    shutdown_deadline = progress._shutdown_deadline
    progress.close()
    assert progress._shutdown_deadline == shutdown_deadline
    calls_at_close = len(session.calls)
    session.release.set()
    progress.join(1)
    assert len(session.calls) == calls_at_close
    assert session.closed


def test_context_controls_progress_reporter_lifecycle() -> None:
    train = mock.Mock()
    context = core.Context(
        checkpoint=mock.Mock(),
        distributed=mock.Mock(),
        preempt=mock.Mock(),
        train=train,
        profiler=mock.Mock(),
        _metrics=mock.Mock(),
    )
    context.start()
    context.close()
    train.start.assert_called_once_with()
    train.close.assert_called_once_with()
