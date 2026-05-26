from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


def _home_dir() -> Path:
    return Path.home()


def config_home() -> Path:
    override = os.environ.get("NF_CONFIG_HOME")
    if override:
        return Path(override).expanduser()

    xdg = os.environ.get("XDG_CONFIG_HOME")
    if xdg:
        return Path(xdg).expanduser() / "nf"

    return _home_dir() / ".config" / "nf"


def state_dir() -> Path:
    return config_home() / "state"


def env_file() -> Path:
    return config_home() / ".env"


def project_file(root: Path | None = None) -> Path:
    return (root or Path.cwd()) / ".nf" / "project.json"


def discover_project_root(start: Path | None = None) -> Path | None:
    current = (start or Path.cwd()).resolve()
    if current.is_file():
        current = current.parent

    for candidate in (current, *current.parents):
        if project_file(candidate).exists():
            return candidate

    return None


@dataclass(frozen=True)
class ThemePackagePaths:
    project_root: Path
    source_dir: Path
    output_path: Path
