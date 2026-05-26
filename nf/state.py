from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .config import state_dir


class StateError(RuntimeError):
    pass


def _load_json_file(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def _records_from_payload(payload: Any, key: str) -> list[dict[str, Any]]:
    if payload is None:
        return []
    if isinstance(payload, list):
        return [item for item in payload if isinstance(item, dict)]
    if isinstance(payload, dict):
        value = payload.get(key)
        if isinstance(value, list):
            return [item for item in value if isinstance(item, dict)]
        if all(isinstance(v, dict) for v in payload.values()):
            return [dict(value, _state_key=str(name)) for name, value in payload.items()]
    raise StateError(f"Unsupported JSON shape in {key}.json")


def load_state_records(kind: str) -> list[dict[str, Any]]:
    path = state_dir() / f"{kind}.json"
    if not path.exists():
        return []
    return _records_from_payload(_load_json_file(path), kind)


@dataclass(frozen=True)
class StateBundle:
    servers: list[dict[str, Any]]
    sites: list[dict[str, Any]]
    projects: list[dict[str, Any]]


def load_state_bundle() -> StateBundle:
    return StateBundle(
        servers=load_state_records("servers"),
        sites=load_state_records("sites"),
        projects=load_state_records("projects"),
    )


def matching_record(records: list[dict[str, Any]], needle: str) -> dict[str, Any] | None:
    normalized = needle.strip().lower()
    if not normalized:
        return None

    candidate_fields = ("id", "_state_key", "name", "slug", "hostname", "label")

    def matches(value: Any) -> bool:
        return isinstance(value, str) and value.strip().lower() == normalized

    for field in candidate_fields:
        for record in records:
            if matches(record.get(field)):
                return record

    for record in records:
        for value in record.values():
            if matches(value):
                return record

    return None
