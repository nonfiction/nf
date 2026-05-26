from __future__ import annotations

import argparse
import json
import shlex
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .config import discover_project_root, project_file
from .passwords import PasswordError, derive_password, secret_salt
from .state import StateError, load_state_bundle, matching_record
from .theme import ThemeError, load_project_metadata, package_theme


class ProjectError(RuntimeError):
    pass


@dataclass(frozen=True)
class ProjectCommand:
    name: str
    description: str
    run: str | list[str]


LOCAL_PROJECT_COMMANDS = (
    "composer",
    "npm",
    "build",
    "watch",
    "test",
    "setup",
    "up",
    "down",
    "restart",
    "logs",
    "reset",
    "fresh",
    "wp",
    "install-theme",
    "activate-theme",
)


def _slug_to_title(value: str) -> str:
    return " ".join(part.capitalize() for part in value.replace("_", "-").split("-") if part)


def _format_table(rows: list[list[str]]) -> str:
    if not rows:
        return ""
    widths = [max(len(row[index]) for row in rows) for index in range(len(rows[0]))]
    lines = []
    for index, row in enumerate(rows):
        padded = [cell.ljust(widths[col]) for col, cell in enumerate(row)]
        lines.append("  ".join(padded).rstrip())
        if index == 0:
            lines.append("  ".join("-" * width for width in widths).rstrip())
    return "\n".join(lines)


def _print_json(value: Any) -> None:
    print(json.dumps(value, indent=2, sort_keys=True))


def _load_project_metadata_or_error(root: Path | None = None) -> dict[str, Any]:
    project_root = discover_project_root(root)
    if project_root is None:
        return {}
    return load_project_metadata(project_root)


def _default_project_commands() -> dict[str, dict[str, Any]]:
    return {
        "composer": {
            "description": "Update theme Composer dependencies",
            "run": "composer --working-dir=theme update && composer --working-dir=theme dump-autoload -o",
        },
        "npm": {
            "description": "Refresh theme development dependencies",
            "run": "npm --prefix theme update --save-dev",
        },
        "build": {
            "description": "Build the theme assets",
            "run": "npm --prefix theme run build",
        },
        "watch": {
            "description": "Watch theme assets during development",
            "run": "npm --prefix theme start",
        },
        "test": {
            "description": "Run the theme test suite",
            "run": "composer --working-dir=theme test",
        },
        "setup": {
            "description": "Set up the local workbench",
            "run": "cd workbench && docker compose up -d && docker compose run --rm cli sh -lc 'wp core is-installed --allow-root || wp core install --url=\"$WP_URL\" --title=\"$WP_TITLE\" --admin_user=\"$ADMIN_USER\" --admin_password=\"$ADMIN_PASSWORD\" --admin_email=\"$ADMIN_EMAIL\" --skip-email --allow-root'",
        },
        "up": {
            "description": "Start the local workbench",
            "run": "cd workbench && docker compose up -d",
        },
        "down": {
            "description": "Stop the local workbench",
            "run": "cd workbench && docker compose down",
        },
        "restart": {
            "description": "Restart the local workbench",
            "run": "cd workbench && docker compose down && docker compose up -d",
        },
        "logs": {
            "description": "Show local workbench logs",
            "run": "cd workbench && docker compose logs -f wordpress",
        },
        "reset": {
            "description": "Reset the local workbench",
            "run": "cd workbench && docker compose down -v --remove-orphans",
        },
        "fresh": {
            "description": "Rebuild the local workbench from scratch",
            "run": "cd workbench && docker compose down -v --remove-orphans && docker compose up -d && docker compose run --rm cli sh -lc 'wp core is-installed --allow-root || wp core install --url=\"$WP_URL\" --title=\"$WP_TITLE\" --admin_user=\"$ADMIN_USER\" --admin_password=\"$ADMIN_PASSWORD\" --admin_email=\"$ADMIN_EMAIL\" --skip-email --allow-root'",
        },
        "wp": {
            "description": "Run wp-cli through the local workbench",
            "run": "cd workbench && docker compose run --rm cli sh -lc 'wp \"$@\" --allow-root' sh \"$@\"",
        },
        "install-theme": {
            "description": "Install the theme in the local workbench",
            "run": "cd workbench && docker compose run --rm cli sh -lc 'theme_zip=\"${1:?theme zip path required}\"; wp theme install \"$theme_zip\" --activate --allow-root' sh \"$@\"",
        },
        "activate-theme": {
            "description": "Activate the theme in the local workbench",
            "run": "cd workbench && docker compose run --rm cli sh -lc 'theme_slug=\"${1:?theme slug required}\"; wp theme activate \"$theme_slug\" --allow-root' sh \"$@\"",
        },
    }


def _render_command_run(run: str | list[str]) -> str:
    if isinstance(run, str):
        return run
    return shlex.join(run)


def _parse_project_command(name: str, value: Any) -> ProjectCommand:
    if isinstance(value, str):
        return ProjectCommand(name=name, description=value, run=value)
    if isinstance(value, list) and all(isinstance(item, str) for item in value):
        run = [str(item) for item in value]
        return ProjectCommand(name=name, description=_render_command_run(run), run=run)
    if isinstance(value, dict):
        description = value.get("description")
        run = value.get("run")
        if not isinstance(description, str) or not description.strip():
            raise ProjectError(f".nf/project.json commands.{name} must include a description string")
        if isinstance(run, str):
            return ProjectCommand(name=name, description=description, run=run)
        if isinstance(run, list) and all(isinstance(item, str) for item in run):
            return ProjectCommand(name=name, description=description, run=[str(item) for item in run])
        raise ProjectError(f".nf/project.json commands.{name}.run must be a string or array of strings")
    raise ProjectError(f".nf/project.json commands.{name} must be a string, array, or object")


def _load_project_commands(root: Path | None = None) -> dict[str, ProjectCommand]:
    metadata = _load_project_metadata_or_error(root)
    commands = metadata.get("commands")
    if commands is None:
        return {}
    if not isinstance(commands, dict):
        raise ProjectError(".nf/project.json commands must be a JSON object")

    parsed: dict[str, ProjectCommand] = {}
    for name, value in commands.items():
        if not isinstance(name, str):
            raise ProjectError(".nf/project.json command names must be strings")
        parsed[name] = _parse_project_command(name, value)
    return parsed


def _discover_project_root_or_error(start: Path | None = None) -> Path:
    project_root = discover_project_root(start)
    if project_root is None:
        raise ProjectError("No .nf/project.json found above the current directory. Add one with commands.<name>.")
    return project_root


def _normalize_passthrough_args(args: list[str]) -> list[str]:
    if args and args[0] == "--":
        return args[1:]
    return args


def _execute_project_command(root: Path, command: ProjectCommand, extra_args: list[str]) -> int:
    try:
        if isinstance(command.run, str):
            completed = subprocess.run(["sh", "-lc", command.run, "sh", *extra_args], cwd=root)
        else:
            completed = subprocess.run([*command.run, *extra_args], cwd=root)
    except OSError as exc:
        print(str(exc), file=sys.stderr)
        return 127
    return completed.returncode


def _project_command_rows(commands: dict[str, ProjectCommand]) -> list[list[str]]:
    rows = [["name", "description", "run"]]
    for name in sorted(commands):
        command = commands[name]
        rows.append([name, command.description, _render_command_run(command.run)])
    return rows


def cmd_project_commands() -> int:
    project_root = discover_project_root()
    if project_root is None:
        print("No .nf/project.json found above the current directory. Add one with commands.<name>.", file=sys.stderr)
        return 1

    commands = _load_project_commands(project_root)
    if not commands:
        print("No local project commands configured. Add .nf/project.json commands.<name>.", file=sys.stderr)
        return 1

    print(_format_table(_project_command_rows(commands)))
    return 0


def cmd_project_run(name: str, extra_args: list[str]) -> int:
    project_root = _discover_project_root_or_error()
    commands = _load_project_commands(project_root)
    command = commands.get(name)
    if command is None:
        print(f"No configured local project command named {name!r}. Add .nf/project.json commands.{name}.", file=sys.stderr)
        return 1

    return _execute_project_command(project_root, command, _normalize_passthrough_args(extra_args))


def cmd_project_alias(name: str, extra_args: list[str]) -> int:
    return cmd_project_run(name, extra_args)


def cmd_list(kind: str) -> int:
    bundle = load_state_bundle()
    records = getattr(bundle, kind)
    if not records:
        print(f"No {kind} found.")
        return 0

    header = ["id", "name", "provider", "hostname", "status"]
    rows = [header]
    for record in records:
        rows.append([
            str(record.get("id", record.get("_state_key", ""))),
            str(record.get("name", record.get("slug", ""))),
            str(record.get("provider", "")),
            str(record.get("hostname", record.get("site_url", ""))),
            str(record.get("status", "")),
        ])
    print(_format_table(rows))
    return 0


def cmd_show(kind: str, needle: str) -> int:
    bundle = load_state_bundle()
    records = getattr(bundle, kind)
    record = matching_record(records, needle)
    if record is None:
        print(f"No {kind[:-1]} matched {needle!r}.", file=sys.stderr)
        return 1
    _print_json(record)
    return 0


def cmd_project_init(args: argparse.Namespace) -> int:
    root = Path.cwd()
    metadata = {
        "project_slug": args.project_slug,
        "project_name": args.project_name or _slug_to_title(args.project_slug),
        "theme_slug": args.theme_slug or args.project_slug,
        "theme_source": args.theme_source or "theme",
        "local_workbench_url": args.local_workbench_url,
        "default_provider": args.default_provider,
        "commands": _default_project_commands(),
    }

    project_path = project_file(root)
    project_path.parent.mkdir(parents=True, exist_ok=True)
    if project_path.exists() and not args.force:
        print(f"{project_path} already exists; use --force to overwrite.", file=sys.stderr)
        return 1

    project_path.write_text(json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"Wrote {project_path}")
    return 0


def _default_theme_output(metadata: dict[str, Any], source_dir: Path) -> Path:
    theme_slug = metadata.get("theme_slug") or source_dir.name
    return Path("dist") / f"{theme_slug}.zip"


def _resolve_theme_output(root: Path, metadata: dict[str, Any], source_dir: Path, override: str | None) -> Path:
    theme_slug = metadata.get("theme_slug") or source_dir.name
    default_file = f"{theme_slug}.zip"
    if not override:
        return root / _default_theme_output(metadata, source_dir)

    output = Path(override)
    if output.exists() and output.is_dir():
        return output / default_file

    if output.suffix.lower() == ".zip":
        return output if output.is_absolute() else root / output

    output_dir = output if output.is_absolute() else root / output
    return output_dir / default_file


def _resolve_theme_source(root: Path, metadata: dict[str, Any], override: str | None) -> Path:
    source_value = override or metadata.get("theme_source") or "theme"
    source = Path(source_value)
    if not source.is_absolute():
        source = root / source
    return source


def cmd_theme_package(args: argparse.Namespace) -> int:
    root = discover_project_root() or Path.cwd()
    metadata = _load_project_metadata_or_error(root)
    source_dir = _resolve_theme_source(root, metadata, args.source)
    output_path = _resolve_theme_output(root, metadata, source_dir, args.output)

    result = package_theme(source_dir, output_path, dry_run=args.dry_run)
    if result.dry_run:
        print(f"Would package {result.source_dir} -> {result.output_path} ({result.file_count} files)")
    else:
        print(f"Wrote {result.output_path} ({result.file_count} files)")
    return 0


def cmd_password_derive(args: argparse.Namespace) -> int:
    salt = secret_salt()
    print(derive_password(args.project_slug, args.purpose, salt))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="nf", description="Safe local nf CLI skeleton")
    subparsers = parser.add_subparsers(dest="command", required=True)

    subparsers.add_parser("commands", help="List configured local project commands")

    run_parser = subparsers.add_parser("run", help="Run a configured local project command")
    run_parser.add_argument("name")
    run_parser.add_argument("args", nargs=argparse.REMAINDER)

    for name in LOCAL_PROJECT_COMMANDS:
        alias_parser = subparsers.add_parser(name, help=f"Run the configured {name} command")
        alias_parser.add_argument("args", nargs=argparse.REMAINDER)

    list_parser = subparsers.add_parser("list", help="List shared state records")
    list_subparsers = list_parser.add_subparsers(dest="kind", required=True)
    list_subparsers.add_parser("servers", help="List servers")
    list_subparsers.add_parser("sites", help="List sites")

    show_parser = subparsers.add_parser("show", help="Show a shared state record")
    show_subparsers = show_parser.add_subparsers(dest="kind", required=True)
    show_server = show_subparsers.add_parser("server", help="Show one server")
    show_server.add_argument("identifier")
    show_site = show_subparsers.add_parser("site", help="Show one site")
    show_site.add_argument("identifier")

    project_parser = subparsers.add_parser("project", help="Project metadata commands")
    project_subparsers = project_parser.add_subparsers(dest="project_command", required=True)
    project_init = project_subparsers.add_parser("init", help="Create .nf/project.json")
    project_init.add_argument("--project-slug", required=True)
    project_init.add_argument("--project-name")
    project_init.add_argument("--theme-slug")
    project_init.add_argument("--theme-source")
    project_init.add_argument("--local-workbench-url", default="http://localhost:18181")
    project_init.add_argument("--default-provider", default="linode")
    project_init.add_argument("--force", action="store_true")

    password_parser = subparsers.add_parser("password", help="Password helpers")
    password_subparsers = password_parser.add_subparsers(dest="password_command", required=True)
    password_derive = password_subparsers.add_parser("derive", help="Derive a project password")
    password_derive.add_argument("project_slug")
    password_derive.add_argument("purpose")

    theme_parser = subparsers.add_parser("theme", help="Theme packaging commands")
    theme_subparsers = theme_parser.add_subparsers(dest="theme_command", required=True)
    theme_package = theme_subparsers.add_parser("package", help="Package a theme into a zip")
    theme_package.add_argument("--source")
    theme_package.add_argument("--output")
    theme_package.add_argument("--dry-run", action="store_true")

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        if args.command == "commands":
            return cmd_project_commands()
        if args.command == "run":
            return cmd_project_run(args.name, args.args)
        if args.command in LOCAL_PROJECT_COMMANDS:
            return cmd_project_alias(args.command, args.args)
        if args.command == "list":
            return cmd_list(args.kind)
        if args.command == "show":
            return cmd_show(args.kind, args.identifier)
        if args.command == "project":
            if args.project_command == "init":
                return cmd_project_init(args)
        if args.command == "password":
            if args.password_command == "derive":
                return cmd_password_derive(args)
        if args.command == "theme":
            if args.theme_command == "package":
                return cmd_theme_package(args)
    except (PasswordError, ProjectError, StateError, ThemeError) as exc:
        print(str(exc), file=sys.stderr)
        return 1

    parser.error("unsupported command")
    return 2
