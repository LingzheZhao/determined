import copy
import inspect
import ntpath
import os
import posixpath
import stat
import tarfile
from typing import Any, Callable, Iterable, Union, cast

Path = Union[str, os.PathLike]


def _is_absolute(path: str) -> bool:
    # Tar member names use POSIX separators, but archives can be extracted on Windows too.
    drive, _ = ntpath.splitdrive(path)
    return bool(drive) or posixpath.isabs(path) or os.path.isabs(path) or ntpath.isabs(path)


def _escapes_archive_root(path: str) -> bool:
    posix_normalized = posixpath.normpath(path)
    windows_normalized = ntpath.normpath(path)
    return (
        posix_normalized == ".."
        or posix_normalized.startswith("../")
        or windows_normalized == ".."
        or windows_normalized.startswith("..\\")
    )


def _is_within_directory(path: str, directory: str) -> bool:
    try:
        return os.path.commonpath((path, directory)) == directory
    except ValueError:
        # Different drives on Windows, for example.
        return False


def _validated_member(member: tarfile.TarInfo, destination: str) -> tarfile.TarInfo:
    if _is_absolute(member.name):
        raise ValueError(f"absolute path in tar archive: {member.name!r}")
    if _escapes_archive_root(member.name):
        raise ValueError(f"path outside extraction directory in tar archive: {member.name!r}")

    target = os.path.realpath(os.path.join(destination, member.name))
    if not _is_within_directory(target, destination):
        raise ValueError(f"path outside extraction directory in tar archive: {member.name!r}")

    if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
        raise ValueError(f"special file in tar archive: {member.name!r}")

    if member.issym() or member.islnk():
        if _is_absolute(member.linkname):
            raise ValueError(
                f"absolute link target in tar archive: {member.name!r} -> {member.linkname!r}"
            )

        archive_link_target = member.linkname
        if member.issym():
            archive_link_target = posixpath.join(posixpath.dirname(member.name), member.linkname)
            windows_link_target = ntpath.join(ntpath.dirname(member.name), member.linkname)
            link_target = os.path.join(os.path.dirname(target), member.linkname)
        else:
            # Hard link targets are relative to the archive root.
            windows_link_target = member.linkname
            link_target = os.path.join(destination, member.linkname)
        if _escapes_archive_root(archive_link_target) or _escapes_archive_root(windows_link_target):
            raise ValueError(
                f"link target outside extraction directory in tar archive: "
                f"{member.name!r} -> {member.linkname!r}"
            )
        link_target = os.path.realpath(link_target)
        if not _is_within_directory(link_target, destination):
            raise ValueError(
                f"link target outside extraction directory in tar archive: "
                f"{member.name!r} -> {member.linkname!r}"
            )

    # Do not restore archive ownership or unsafe permission bits.  Regular executable files and
    # ordinary 0644/0755-style modes retain their modes.
    member = copy.copy(member)
    # Python 3.12 supports None here to suppress ownership restoration, but Python 3.8's typeshed
    # predates that API even though assigning None works at runtime.
    member.uid = member.gid = cast(Any, None)
    member.uname = member.gname = cast(Any, None)
    if member.mode is not None:
        member.mode &= 0o755
        if (member.isfile() or member.islnk()) and not member.mode & 0o100:
            member.mode &= ~0o111
    return member


def _prepare_link_path(target: str, destination: str) -> None:
    parent = os.path.dirname(target)
    os.makedirs(parent, exist_ok=True)
    resolved_parent = os.path.realpath(parent)
    if not _is_within_directory(resolved_parent, destination):
        raise ValueError(f"link path outside extraction directory: {target!r}")
    if os.path.lexists(target):
        if stat.S_ISDIR(os.lstat(target).st_mode):
            raise ValueError(f"link would overwrite a directory: {target!r}")
        os.unlink(target)


def _validated_members(
    members: Iterable[tarfile.TarInfo], destination: str
) -> Iterable[tarfile.TarInfo]:
    """Validate and create links without relying on tarfile's unsafe link fallback."""
    for member in members:
        member = _validated_member(member, destination)
        target = os.path.join(destination, member.name)
        if member.issym():
            # Create symlinks directly so tarfile cannot relocate a referenced symlink during its
            # link fallback.  The member and its target have just been containment-checked.
            _prepare_link_path(target, destination)
            os.symlink(member.linkname, target)
            continue
        if member.islnk():
            link_target = os.path.realpath(os.path.join(destination, member.linkname))
            if not os.path.isfile(link_target):
                raise ValueError(
                    f"hard link target is not an extracted regular file: "
                    f"{member.name!r} -> {member.linkname!r}"
                )
            _prepare_link_path(target, destination)
            os.link(link_target, target)
            continue
        yield member


def _supports_filter(archive: tarfile.TarFile) -> bool:
    return "filter" in inspect.signature(archive.extractall).parameters


def _extractall_with_filter(
    archive: tarfile.TarFile,
    destination: str,
    members: Iterable[tarfile.TarInfo],
    filter_fn: Callable[[tarfile.TarInfo, str], tarfile.TarInfo],
) -> None:
    # The filter argument was added after Python 3.8, so its typeshed signature does not include it.
    extractall = cast(Callable[..., None], archive.extractall)
    extractall(destination, members=members, filter=filter_fn)


def safe_extractall(archive: tarfile.TarFile, path: Path) -> None:
    """Extract regular files, directories, and contained links from an untrusted tar archive."""
    destination = os.path.realpath(os.fspath(path))
    os.makedirs(destination, exist_ok=True)

    # Validate the complete archive before writing anything.  The second, just-in-time validation
    # in _validated_members accounts for symlinks created while extraction is in progress.
    members = [_validated_member(member, destination) for member in archive.getmembers()]

    if _supports_filter(archive):
        _extractall_with_filter(
            archive,
            destination,
            _validated_members(members, destination),
            lambda member, _: _validated_member(member, destination),
        )
    else:
        archive.extractall(destination, members=_validated_members(members, destination))
