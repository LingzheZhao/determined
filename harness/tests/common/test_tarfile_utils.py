import io
import os
import pathlib
import stat
import tarfile
from typing import List

import pytest

from determined.common import tarfile_utils


def _archive(*members: tarfile.TarInfo) -> tarfile.TarFile:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for member in members:
            contents = io.BytesIO(b"contents") if member.isfile() else None
            archive.addfile(member, contents)
    buffer.seek(0)
    return tarfile.open(fileobj=buffer, mode="r")


def _file(name: str, mode: int = 0o644) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.size = len(b"contents")
    member.mode = mode
    return member


def _directory(name: str, mode: int = 0o755) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.type = tarfile.DIRTYPE
    member.mode = mode
    return member


def _link(name: str, target: str, link_type: bytes) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.type = link_type
    member.linkname = target
    return member


@pytest.mark.parametrize("legacy", [False, True])
def test_safe_extractall_preserves_safe_files_links_and_modes(
    tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch, legacy: bool
) -> None:
    if legacy:
        monkeypatch.setattr(tarfile_utils, "_supports_filter", lambda _: False)
    members: List[tarfile.TarInfo] = [
        _directory("target", 0o750),
        _file("target/executable", 0o755),
        _file("unsafe-mode", 0o7777),
        _link("symbolic", "target/executable", tarfile.SYMTYPE),
        _link("hard", "target/executable", tarfile.LNKTYPE),
        _link("directory-link", "target", tarfile.SYMTYPE),
        _file("directory-link/through-link"),
    ]

    with _archive(*members) as archive:
        tarfile_utils.safe_extractall(archive, tmp_path)

    assert (tmp_path / "target/executable").read_bytes() == b"contents"
    assert stat.S_IMODE((tmp_path / "target/executable").stat().st_mode) == 0o755
    assert stat.S_IMODE((tmp_path / "target").stat().st_mode) == 0o750
    assert stat.S_IMODE((tmp_path / "unsafe-mode").stat().st_mode) == 0o755
    assert (tmp_path / "symbolic").is_symlink()
    assert (tmp_path / "symbolic").read_bytes() == b"contents"
    assert os.path.samefile(tmp_path / "hard", tmp_path / "target/executable")
    assert (tmp_path / "target/through-link").read_bytes() == b"contents"


@pytest.mark.parametrize(
    "name",
    [
        "/absolute",
        "C:/absolute",
        "C:\\absolute",
        "../parent",
        "nested/../../parent",
        "nested\\..\\..\\parent",
    ],
)
def test_safe_extractall_rejects_paths_outside_destination(
    tmp_path: pathlib.Path, name: str
) -> None:
    with _archive(_file("would-have-been-extracted"), _file(name)) as archive:
        with pytest.raises(ValueError, match="path|absolute"):
            tarfile_utils.safe_extractall(archive, tmp_path / "destination")

    assert not (tmp_path / "destination/would-have-been-extracted").exists()
    assert not (tmp_path / "parent").exists()


@pytest.mark.parametrize("link_type", [tarfile.SYMTYPE, tarfile.LNKTYPE])
@pytest.mark.parametrize(
    "target", ["/absolute", "C:/absolute", "C:\\absolute", "../outside", "..\\outside"]
)
def test_safe_extractall_rejects_links_outside_destination(
    tmp_path: pathlib.Path, link_type: bytes, target: str
) -> None:
    with _archive(_link("link", target, link_type)) as archive:
        with pytest.raises(ValueError, match="link target"):
            tarfile_utils.safe_extractall(archive, tmp_path / "destination")


def test_safe_extractall_rejects_symlink_chain_outside_destination(
    tmp_path: pathlib.Path,
) -> None:
    with _archive(
        _link("first", "second", tarfile.SYMTYPE),
        _link("second", "../outside", tarfile.SYMTYPE),
    ) as archive:
        with pytest.raises(ValueError, match="link target"):
            tarfile_utils.safe_extractall(archive, tmp_path / "destination")


def test_legacy_extraction_rejects_relocated_symlink_fallback(
    tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(tarfile_utils, "_supports_filter", lambda _: False)
    destination = tmp_path / "destination"
    with _archive(
        _link("nested/link", "../outside", tarfile.SYMTYPE),
        _link("hard", "nested/link", tarfile.LNKTYPE),
    ) as archive:
        with pytest.raises(ValueError, match="hard link target"):
            tarfile_utils.safe_extractall(archive, destination)

    assert not (destination / "hard").exists()
    assert not (tmp_path / "outside").exists()


@pytest.mark.parametrize("special_type", [tarfile.FIFOTYPE, tarfile.CHRTYPE, tarfile.BLKTYPE])
def test_safe_extractall_rejects_special_files(tmp_path: pathlib.Path, special_type: bytes) -> None:
    special = tarfile.TarInfo("fifo")
    special.type = special_type

    with _archive(special) as archive:
        with pytest.raises(ValueError, match="special file"):
            tarfile_utils.safe_extractall(archive, tmp_path)


def test_safe_extractall_rejects_existing_symlink_to_outside_destination(
    tmp_path: pathlib.Path,
) -> None:
    destination = tmp_path / "destination"
    outside = tmp_path / "outside"
    destination.mkdir()
    outside.mkdir()
    (destination / "linked-directory").symlink_to(outside, target_is_directory=True)

    with _archive(_file("linked-directory/file")) as archive:
        with pytest.raises(ValueError, match="outside extraction directory"):
            tarfile_utils.safe_extractall(archive, destination)

    assert not (outside / "file").exists()
