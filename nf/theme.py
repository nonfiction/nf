from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile

from .config import project_file


EXCLUDED_NAMES = {".git", ".DS_Store", "node_modules"}


class ThemeError(RuntimeError):
    pass


def load_project_metadata(root: Path | None = None) -> dict:
    path = project_file(root)
    if not path.exists():
        return {}
    with path.open("r", encoding="utf-8") as handle:
        payload = json.load(handle)
    if not isinstance(payload, dict):
        raise ThemeError(f"{path} must contain a JSON object")
    return payload


def _should_skip(path: Path, output_path: Path | None = None) -> bool:
    if any(part in EXCLUDED_NAMES for part in path.parts):
        return True
    if output_path is not None and path.resolve() == output_path.resolve():
        return True
    return False


def _archive_name(root: Path, path: Path) -> str:
    return path.relative_to(root.parent).as_posix()


@dataclass(frozen=True)
class ThemePackageResult:
    source_dir: Path
    output_path: Path
    file_count: int
    dry_run: bool


def package_theme(source_dir: Path, output_path: Path, dry_run: bool = False) -> ThemePackageResult:
    if not source_dir.exists() or not source_dir.is_dir():
        raise ThemeError(f"Theme source directory does not exist: {source_dir}")

    files = [path for path in sorted(source_dir.rglob("*")) if path.is_file() and not _should_skip(path, output_path)]

    if not dry_run:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        with ZipFile(output_path, "w", compression=ZIP_DEFLATED) as archive:
            for path in files:
                archive.write(path, arcname=_archive_name(source_dir, path))

    return ThemePackageResult(source_dir=source_dir, output_path=output_path, file_count=len(files), dry_run=dry_run)
