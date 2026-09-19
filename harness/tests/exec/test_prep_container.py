import base64
import io
import pathlib
import tarfile
import types
from unittest import mock

import pytest

from determined import constants
from determined.exec import prep_container


def _unsafe_context_archive() -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        symlink = tarfile.TarInfo("nested/link")
        symlink.type = tarfile.SYMTYPE
        symlink.linkname = "../outside"
        archive.addfile(symlink)

        hardlink = tarfile.TarInfo("hard")
        hardlink.type = tarfile.LNKTYPE
        hardlink.linkname = "nested/link"
        archive.addfile(hardlink)
    return buffer.getvalue()


def test_download_context_directory_rejects_unsafe_link_fallback(
    tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    working_directory = tmp_path / "working"
    working_directory.mkdir()
    monkeypatch.chdir(working_directory)
    monkeypatch.setattr(constants, "MANAGED_TRAINING_MODEL_COPY", str(tmp_path / "model-copy"))
    response = types.SimpleNamespace(b64Tgz=base64.b64encode(_unsafe_context_archive()).decode())

    with mock.patch.object(
        prep_container.bindings, "get_GetTaskContextDirectory", return_value=response
    ):
        with pytest.raises(ValueError, match="hard link target"):
            prep_container.download_context_directory(
                mock.Mock(), types.SimpleNamespace(task_id="task-id")
            )

    assert not (tmp_path / "outside").exists()
