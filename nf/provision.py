from __future__ import annotations

import base64
import json
import os
import shutil
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .config import discover_project_root, state_dir
from .passwords import derive_password, secret_salt
from .theme import load_project_metadata


class ProvisionError(RuntimeError):
    pass


def _slug_to_title(value: str) -> str:
    return " ".join(part.capitalize() for part in value.replace("_", "-").split("-") if part)


def _env_or_none(name: str) -> str | None:
    value = os.environ.get(name)
    return value if value else None


def _required_env(name: str) -> str:
    value = _env_or_none(name)
    if value is None:
        raise ProvisionError(f"Expected {name} in the environment.")
    return value


def _project_context() -> tuple[Path | None, dict[str, Any]]:
    root = discover_project_root()
    if root is None:
        return None, {}
    return root, load_project_metadata(root)


def _project_slug(explicit: str | None, metadata: dict[str, Any]) -> str:
    if explicit:
        return explicit
    metadata_slug = metadata.get("project_slug")
    if isinstance(metadata_slug, str) and metadata_slug.strip():
        return metadata_slug.strip()
    return Path.cwd().name


def _project_name(metadata: dict[str, Any]) -> str | None:
    value = metadata.get("project_name")
    if isinstance(value, str) and value.strip():
        return value.strip()
    return None


def _default_remote_wp_path(project_slug: str) -> str:
    if not project_slug:
        raise ProvisionError("--remote-wp-path is required when no project slug can be determined.")
    return f"/var/www/{project_slug}"


def _php_fpm_service_name(socket_path: str) -> str:
    return Path(socket_path).name.removesuffix(".sock")


def _clean_path(value: str | None) -> Path | None:
    if value is None:
        return None
    return Path(value).expanduser()


def _render_template(template: str, replacements: dict[str, str]) -> str:
    rendered = template
    for placeholder, value in sorted(replacements.items(), key=lambda item: len(item[0]), reverse=True):
        rendered = rendered.replace(placeholder, value)
    return rendered


def _require_gum() -> None:
    if shutil.which("gum") is None:
        raise ProvisionError(
            "gum is required for interactive provision-server; install gum or pass --non-interactive with explicit flags."
        )


def _gum_command(args: list[str], *, input_text: str | None = None) -> str:
    _require_gum()
    completed = subprocess.run(
        ["gum", *args],
        check=False,
        capture_output=True,
        text=True,
        input=input_text,
    )
    if completed.returncode != 0:
        stderr = completed.stderr.strip()
        stdout = completed.stdout.strip()
        details = stderr or stdout or "gum command failed"
        raise ProvisionError(details)
    return completed.stdout.strip()


def _gum_input(prompt: str, default: str | None = None) -> str:
    args = ["input", "--prompt", prompt]
    if default is not None:
        args.extend(["--value", default])
    return _gum_command(args)


def _gum_choose(prompt: str, options: list[str]) -> str:
    args = ["choose", "--header", prompt, *options]
    return _gum_command(args)


def _gum_confirm(prompt: str) -> bool:
    _require_gum()
    completed = subprocess.run(["gum", "confirm", prompt], check=False)
    return completed.returncode == 0


def _resolve_value(
    explicit: str | None,
    *,
    prompt: str,
    default: str,
    non_interactive: bool,
    allow_blank: bool = False,
) -> str | None:
    if explicit is not None:
        value = explicit.strip()
        return value if value or allow_blank else default
    if non_interactive:
        return default if default or not allow_blank else None
    value = _gum_input(prompt, default).strip()
    if not value and allow_blank:
        return None
    return value or default


CLOUD_INIT_TEMPLATE = r"""#cloud-config
package_update: true
package_upgrade: true
packages:
  - nginx
  - mariadb-server
  - php-fpm
  - php-mysql
  - php-xml
  - php-mbstring
  - php-curl
  - php-zip
  - php-gd
  - php-intl
  - unzip
  - curl
  - certbot
  - python3-certbot-dns-dnsimple
  - composer
  - rsync
  - zip

users:
  - name: __SSH_USER__
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [sudo]
    ssh_authorized_keys:
      - __SSH_PUBLIC_KEY__

write_files:
  - path: /etc/nginx/sites-available/__SERVER_NAME__
    permissions: '0644'
    content: |
      server {
          listen 80;
          listen [::]:80;
          server_name __SITE_DOMAIN__;
          root __REMOTE_WP_PATH__;
          index index.php index.html;
          client_max_body_size 64M;

          access_log /var/log/nginx/__SERVER_NAME__.access.log;
          error_log /var/log/nginx/__SERVER_NAME__.error.log;

          location / {
              try_files $uri $uri/ /index.php?$args;
          }

          location ~ \.php$ {
              include snippets/fastcgi-php.conf;
              fastcgi_pass unix:__PHP_FPM_SOCKET__;
          }

          location ~* \.(css|js|jpg|jpeg|png|gif|ico|svg|webp)$ {
              expires 7d;
              access_log off;
          }
      }
  - path: /usr/local/bin/__SERVER_NAME__-enable-tls
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euo pipefail

      cat >/etc/nginx/sites-available/__SERVER_NAME__ <<'EOF'
      server {
          listen 80;
          listen [::]:80;
          server_name __SITE_DOMAIN__;

          return 301 https://$host$request_uri;
      }

      server {
          listen 443 ssl http2;
          listen [::]:443 ssl http2;
          server_name __SITE_DOMAIN__;
          root __REMOTE_WP_PATH__;
          index index.php index.html;
          client_max_body_size 64M;

          ssl_certificate /etc/letsencrypt/live/__SITE_DOMAIN__/fullchain.pem;
          ssl_certificate_key /etc/letsencrypt/live/__SITE_DOMAIN__/privkey.pem;

          access_log /var/log/nginx/__SERVER_NAME__.access.log;
          error_log /var/log/nginx/__SERVER_NAME__.error.log;

          location / {
              try_files $uri $uri/ /index.php?$args;
          }

          location ~ \.php$ {
              include snippets/fastcgi-php.conf;
              fastcgi_pass unix:__PHP_FPM_SOCKET__;
          }

          location ~* \.(css|js|jpg|jpeg|png|gif|ico|svg|webp)$ {
              expires 7d;
              access_log off;
          }
      }
      EOF

      nginx -t
      systemctl reload nginx
  - path: /root/.secrets/certbot/dnsimple.ini
    permissions: '0600'
    content: |
      dns_dnsimple_token = __DNSIMPLE_TOKEN__
      dns_dnsimple_account = __DNSIMPLE_ACCOUNT_ID__

runcmd:
  - mkdir -p __REMOTE_WP_PATH__
  - chown -R __SSH_USER__:www-data __REMOTE_WP_PATH__
  - chmod -R 775 __REMOTE_WP_PATH__
  - bash -lc 'cd /tmp && curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar && chmod +x wp-cli.phar && mv wp-cli.phar /usr/local/bin/wp'
  - bash -lc 'cd /tmp && curl -O https://wordpress.org/latest.zip && unzip -o latest.zip && rsync -av wordpress/ __REMOTE_WP_PATH__/'
  - chown -R __SSH_USER__:www-data __REMOTE_WP_PATH__
  - bash -lc 'find __REMOTE_WP_PATH__ -type d -exec chmod 775 {} + && find __REMOTE_WP_PATH__ -type f -exec chmod 664 {} +'
  - mysql -e "CREATE DATABASE IF NOT EXISTS \`__DB_NAME__\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
  - mysql -e "CREATE USER IF NOT EXISTS '__DB_USER__'@'localhost' IDENTIFIED BY '__DB_PASS__';"
  - mysql -e "ALTER USER '__DB_USER__'@'localhost' IDENTIFIED BY '__DB_PASS__';"
  - mysql -e "GRANT ALL PRIVILEGES ON \`__DB_NAME__\`.* TO '__DB_USER__'@'localhost'; FLUSH PRIVILEGES;"
  - bash -lc 'cd __REMOTE_WP_PATH__ && wp config create --dbname=__DB_NAME__ --dbuser=__DB_USER__ --dbpass=__DB_PASS__ --dbhost=localhost --allow-root --skip-check'
  - bash -lc 'cd __REMOTE_WP_PATH__ && wp config set WP_DEBUG false --raw --type=constant --allow-root && wp config set WP_DEBUG_LOG true --raw --type=constant --allow-root && wp config set WP_DEBUG_DISPLAY false --raw --type=constant --allow-root && wp config set FS_METHOD direct --type=constant --allow-root'
  - rm -f /etc/nginx/sites-enabled/default
  - ln -sf /etc/nginx/sites-available/__SERVER_NAME__ /etc/nginx/sites-enabled/__SERVER_NAME__
  - nginx -t
  - systemctl enable nginx
  - systemctl restart nginx
  - systemctl enable __PHP_FPM_SERVICE__
  - systemctl restart __PHP_FPM_SERVICE__
  - bash -lc 'wp core install --path=__REMOTE_WP_PATH__ --url=__SITE_URL__ --title="__SITE_TITLE__" --admin_user=__WP_ADMIN_USER__ --admin_password=__WP_ADMIN_PASS__ --admin_email=__WP_ADMIN_EMAIL__ --allow-root'
  - bash -lc 'wp option update blog_public 0 --path=__REMOTE_WP_PATH__ --allow-root && wp rewrite structure "/%postname%/" --path=__REMOTE_WP_PATH__ --allow-root && wp rewrite flush --path=__REMOTE_WP_PATH__ --allow-root || true'
  - bash -lc 'certbot certonly --non-interactive --agree-tos --dns-dnsimple --dns-dnsimple-credentials /root/.secrets/certbot/dnsimple.ini -m __WP_ADMIN_EMAIL__ -d __SITE_DOMAIN__ -d *.__SITE_DOMAIN__'
  - /usr/local/bin/__SERVER_NAME__-enable-tls
"""


@dataclass(frozen=True)
class ProvisionPlan:
    provider: str
    project_root: Path | None
    project_slug: str
    project_name: str | None
    server_name: str
    label: str
    region: str
    linode_type: str
    image: str
    ssh_user: str
    ssh_public_key_file: Path
    site_domain: str
    remote_wp_path: str
    php_fpm_socket: str
    db_name: str
    db_user: str
    wp_admin_user: str
    wp_admin_email: str
    site_title: str
    dns_zone: str | None
    dnsimple_account_id: str
    write_cloud_init: Path | None
    execute: bool
    yes: bool
    dry_run: bool
    non_interactive: bool
    show_cloud_init: bool


@dataclass(frozen=True)
class ProvisionResult:
    linode_id: str
    ipv4: str
    dns_zone: str | None
    server_state_path: Path
    site_state_path: Path


def build_provision_plan(args: Any) -> ProvisionPlan:
    non_interactive = bool(getattr(args, "non_interactive", False))
    project_root, metadata = _project_context()

    if not non_interactive:
        provider = getattr(args, "provider", None) or _gum_choose("Select provider", ["linode"])
    else:
        provider = getattr(args, "provider", None) or "linode"

    project_slug = _project_slug(getattr(args, "project_slug", None), metadata)
    if getattr(args, "project_slug", None) is None and not non_interactive:
        project_slug = (_gum_input("Project slug: ", project_slug) or project_slug).strip()

    project_name = _project_name(metadata)
    default_server_name = getattr(args, "server_name", None) or "app1"
    server_name = _resolve_value(
        getattr(args, "server_name", None),
        prompt="Server name: ",
        default=default_server_name,
        non_interactive=non_interactive,
    )
    assert server_name is not None

    label = _resolve_value(
        getattr(args, "label", None),
        prompt="Linode label: ",
        default=project_slug or server_name,
        non_interactive=non_interactive,
    )
    site_domain = _resolve_value(
        getattr(args, "site_domain", None),
        prompt="Site domain: ",
        default=f"{project_slug}.ln.nfweb.dev",
        non_interactive=non_interactive,
    )
    remote_wp_path = _resolve_value(
        getattr(args, "remote_wp_path", None),
        prompt="Remote WordPress path: ",
        default=_default_remote_wp_path(project_slug),
        non_interactive=non_interactive,
    )
    region = _resolve_value(
        getattr(args, "region", None),
        prompt="Linode region: ",
        default="ca-central",
        non_interactive=non_interactive,
    )
    linode_type = _resolve_value(
        getattr(args, "type", None),
        prompt="Linode type: ",
        default="g6-standard-1",
        non_interactive=non_interactive,
    )
    image = _resolve_value(
        getattr(args, "image", None),
        prompt="Linode image: ",
        default="linode/ubuntu24.04",
        non_interactive=non_interactive,
    )
    ssh_user = _resolve_value(
        getattr(args, "ssh_user", None),
        prompt="Deployment SSH user: ",
        default="nonfiction",
        non_interactive=non_interactive,
    )
    ssh_public_key_file = _clean_path(
        _resolve_value(
            getattr(args, "ssh_public_key_file", None),
            prompt="SSH public key file: ",
            default="~/.ssh/id_ed25519.pub",
            non_interactive=non_interactive,
        )
    )
    php_fpm_socket = _resolve_value(
        getattr(args, "php_fpm_socket", None),
        prompt="PHP-FPM socket: ",
        default="/var/run/php/php8.3-fpm.sock",
        non_interactive=non_interactive,
    )
    db_name = _resolve_value(
        getattr(args, "db_name", None),
        prompt="Database name: ",
        default=project_slug,
        non_interactive=non_interactive,
    )
    db_user = _resolve_value(
        getattr(args, "db_user", None),
        prompt="Database user: ",
        default=project_slug,
        non_interactive=non_interactive,
    )
    wp_admin_user = _resolve_value(
        getattr(args, "wp_admin_user", None),
        prompt="WP admin user: ",
        default=f"nf-{project_slug}",
        non_interactive=non_interactive,
    )
    wp_admin_email = _resolve_value(
        getattr(args, "wp_admin_email", None),
        prompt="WP admin email: ",
        default="web@nonfiction.ca",
        non_interactive=non_interactive,
    )
    site_title = _resolve_value(
        getattr(args, "site_title", None),
        prompt="Site title: ",
        default=project_name or _slug_to_title(project_slug),
        non_interactive=non_interactive,
    )
    dnsimple_account_id = _resolve_value(
        getattr(args, "dnsimple_account_id", None),
        prompt="DNSimple account id: ",
        default=_env_or_none("DNSIMPLE_ACCOUNT_ID") or "14",
        non_interactive=non_interactive,
    )
    dns_zone = _resolve_value(
        getattr(args, "dns_zone", None),
        prompt="DNS zone (blank to infer during execution): ",
        default="",
        non_interactive=non_interactive,
        allow_blank=True,
    )
    preview_path_value = _resolve_value(
        getattr(args, "write_cloud_init", None),
        prompt="Cloud-init preview path (blank to skip): ",
        default="",
        non_interactive=non_interactive,
        allow_blank=True,
    )

    return ProvisionPlan(
        provider=provider,
        project_root=project_root,
        project_slug=project_slug,
        project_name=project_name,
        server_name=server_name or "app1",
        label=label or project_slug,
        region=region or "ca-central",
        linode_type=linode_type or "g6-standard-1",
        image=image or "linode/ubuntu24.04",
        ssh_user=ssh_user or "nonfiction",
        ssh_public_key_file=ssh_public_key_file or Path("~/.ssh/id_ed25519.pub").expanduser(),
        site_domain=site_domain or f"{project_slug}.ln.nfweb.dev",
        remote_wp_path=remote_wp_path or _default_remote_wp_path(project_slug),
        php_fpm_socket=php_fpm_socket or "/var/run/php/php8.3-fpm.sock",
        db_name=db_name or project_slug,
        db_user=db_user or project_slug,
        wp_admin_user=wp_admin_user or f"nf-{project_slug}",
        wp_admin_email=wp_admin_email or "web@nonfiction.ca",
        site_title=site_title or project_name or _slug_to_title(project_slug),
        dns_zone=dns_zone or None,
        dnsimple_account_id=dnsimple_account_id or "14",
        write_cloud_init=_clean_path(preview_path_value),
        execute=bool(getattr(args, "execute", False)),
        yes=bool(getattr(args, "yes", False)),
        dry_run=bool(getattr(args, "dry_run", False) or not getattr(args, "execute", False)),
        non_interactive=non_interactive,
        show_cloud_init=bool(getattr(args, "show_cloud_init", False)),
    )


def _cloud_init_replacements(
    plan: ProvisionPlan,
    *,
    ssh_public_key: str,
    db_pass: str,
    wp_admin_pass: str,
    dnsimple_token: str,
) -> dict[str, str]:
    return {
        "__SSH_USER__": plan.ssh_user,
        "__SSH_PUBLIC_KEY__": ssh_public_key,
        "__SERVER_NAME__": plan.server_name,
        "__SITE_DOMAIN__": plan.site_domain,
        "__SITE_URL__": f"https://{plan.site_domain}",
        "__REMOTE_WP_PATH__": plan.remote_wp_path,
        "__PHP_FPM_SOCKET__": plan.php_fpm_socket,
        "__DB_NAME__": plan.db_name,
        "__DB_USER__": plan.db_user,
        "__DB_PASS__": db_pass,
        "__WP_ADMIN_USER__": plan.wp_admin_user,
        "__WP_ADMIN_PASS__": wp_admin_pass,
        "__WP_ADMIN_EMAIL__": plan.wp_admin_email,
        "__DNSIMPLE_TOKEN__": dnsimple_token,
        "__DNSIMPLE_ACCOUNT_ID__": plan.dnsimple_account_id,
        "__SITE_TITLE__": plan.site_title,
        "__PHP_FPM_SERVICE__": _php_fpm_service_name(plan.php_fpm_socket),
    }


def _record_matches(record: dict[str, Any], candidate: dict[str, Any]) -> bool:
    for key in ("linode_id", "hostname", "name", "slug", "label"):
        left = record.get(key)
        right = candidate.get(key)
        if left is not None and right is not None and str(left) == str(right):
            return True
    return False


def _load_state_payload(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    with path.open("r", encoding="utf-8") as handle:
        payload = json.load(handle)
    if payload is None:
        return []
    if isinstance(payload, list):
        return [item for item in payload if isinstance(item, dict)]
    if isinstance(payload, dict):
        records = payload.get(path.stem)
        if isinstance(records, list):
            return [item for item in records if isinstance(item, dict)]
        if all(isinstance(value, dict) for value in payload.values()):
            return [dict(value, _state_key=str(name)) for name, value in payload.items()]
    raise ProvisionError(f"Unsupported JSON shape in {path}")


def _save_state_payload(path: Path, records: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(records, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _upsert_state_record(path: Path, candidate: dict[str, Any]) -> None:
    records = _load_state_payload(path)
    updated = False
    for index, record in enumerate(records):
        if _record_matches(record, candidate):
            records[index] = candidate
            updated = True
            break
    if not updated:
        records.append(candidate)
    _save_state_payload(path, records)


def _dnsimple_request(method: str, url: str, token: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    body = None if payload is None else json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/json",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            response_payload = json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body_text = exc.read().decode("utf-8", errors="replace")
        raise ProvisionError(f"DNSimple API request failed: {method} {url} (HTTP {exc.code})\n{body_text}") from exc
    except urllib.error.URLError as exc:
        raise ProvisionError(f"DNSimple API request failed: {method} {url}: {exc.reason}") from exc
    if not isinstance(response_payload, dict):
        raise ProvisionError("Unexpected DNSimple API response shape.")
    return response_payload


def _dnsimple_data(payload: dict[str, Any]) -> Any:
    return payload.get("data", payload)


def _dnsimple_url(account_id: str, path: str) -> str:
    return f"https://api.dnsimple.com/v2/{account_id}{path}"


def _find_dnsimple_zone(plan: ProvisionPlan, token: str) -> str:
    if plan.dns_zone:
        return plan.dns_zone
    parts = plan.site_domain.split(".")
    for index in range(len(parts) - 1):
        candidate = ".".join(parts[index:])
        encoded = urllib.parse.quote(candidate, safe="")
        url = _dnsimple_url(plan.dnsimple_account_id, f"/zones/{encoded}")
        try:
            _dnsimple_request("GET", url, token)
            return candidate
        except ProvisionError as exc:
            if "HTTP 404" in str(exc):
                continue
            raise
    raise ProvisionError(f"Could not find a matching DNSimple zone for {plan.site_domain}")


def _relative_record_name(fqdn: str, zone: str) -> str:
    if fqdn == zone:
        return ""
    suffix = f".{zone}"
    if fqdn.endswith(suffix):
        return fqdn[: -len(suffix)]
    return fqdn


def _dnsimple_upsert_a_record(token: str, account_id: str, zone: str, name: str, ip: str) -> None:
    encoded_zone = urllib.parse.quote(zone, safe="")
    url = _dnsimple_url(account_id, f"/zones/{encoded_zone}/records?type=A")
    payload = _dnsimple_request("GET", url, token)
    records = _dnsimple_data(payload)
    if not isinstance(records, list):
        raise ProvisionError("Unexpected DNSimple records response shape.")

    existing = None
    for record in records:
        if isinstance(record, dict) and record.get("name") == name:
            existing = record
            break

    if existing is not None:
        record_id = existing.get("id")
        current_ip = str(existing.get("content", ""))
        if current_ip == ip:
            return
        if record_id is None:
            raise ProvisionError("DNSimple record is missing an id.")
        patch_url = _dnsimple_url(account_id, f"/zones/{encoded_zone}/records/{record_id}")
        _dnsimple_request("PATCH", patch_url, token, {"content": ip, "ttl": 60})
        return

    create_url = _dnsimple_url(account_id, f"/zones/{encoded_zone}/records")
    _dnsimple_request("POST", create_url, token, {"name": name, "type": "A", "content": ip, "ttl": 60})


def _plan_lines(plan: ProvisionPlan, cloud_init_path: Path | None) -> list[str]:
    return [
        f"provider: {plan.provider}",
        f"project slug: {plan.project_slug}",
        f"project name: {plan.project_name or plan.site_title}",
        f"server name: {plan.server_name}",
        f"label: {plan.label}",
        f"region: {plan.region}",
        f"type: {plan.linode_type}",
        f"image: {plan.image}",
        f"site domain: {plan.site_domain}",
        f"remote wp path: {plan.remote_wp_path}",
        f"db name/user: {plan.db_name} / {plan.db_user}",
        f"wp admin user: {plan.wp_admin_user}",
        f"site title: {plan.site_title}",
        f"dns zone: {plan.dns_zone or 'inferred during execution'}",
        f"dnsimple account id: {plan.dnsimple_account_id}",
        f"cloud-init preview: {cloud_init_path or 'not written'}",
    ]


def render_plan(plan: ProvisionPlan, cloud_init_path: Path | None, cloud_init_preview: str | None = None) -> str:
    header = "Provision server plan" if plan.execute else "Provision server dry-run plan"
    if plan.execute and plan.non_interactive and not plan.yes:
        header = "Provision server blocked (missing --yes)"
    lines = [header, ""]
    lines.extend(f"- {line}" for line in _plan_lines(plan, cloud_init_path))
    if cloud_init_preview is not None:
        lines.extend(["", "cloud-init preview:", cloud_init_preview.rstrip()])
    return "\n".join(lines).rstrip() + "\n"


def _base64_user_data(content: str) -> str:
    return base64.b64encode(content.encode("utf-8")).decode("ascii")


def _render_cloud_init(plan: ProvisionPlan, *, actual: bool, db_pass: str | None = None, wp_admin_pass: str | None = None, dnsimple_token: str | None = None) -> str:
    if actual:
        if db_pass is None or wp_admin_pass is None or dnsimple_token is None:
            raise ProvisionError("Missing secrets for cloud-init rendering.")
        ssh_public_key = plan.ssh_public_key_file.read_text(encoding="utf-8").strip()
        return _render_template(
            CLOUD_INIT_TEMPLATE,
            _cloud_init_replacements(
                plan,
                ssh_public_key=ssh_public_key,
                db_pass=db_pass,
                wp_admin_pass=wp_admin_pass,
                dnsimple_token=dnsimple_token,
            ),
        )

    return _render_template(
        CLOUD_INIT_TEMPLATE,
        _cloud_init_replacements(
            plan,
            ssh_public_key="<ssh public key>",
            db_pass="<derived database password>",
            wp_admin_pass="<derived wp admin password>",
            dnsimple_token="<dnsimple token>",
        ),
    )


def _write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def _write_cloud_init_preview_if_requested(plan: ProvisionPlan, preview: str) -> Path | None:
    if plan.write_cloud_init is None:
        return None
    _write_text(plan.write_cloud_init, preview)
    return plan.write_cloud_init


def _validate_actual_execution(plan: ProvisionPlan) -> None:
    if plan.provider != "linode":
        raise ProvisionError(f"Unsupported provider {plan.provider!r}. Only linode is available in this slice.")
    _required_env("NF_SECRET_SALT")
    if _env_or_none("LINODE_CLI_TOKEN") is None and _env_or_none("LINODE_TOKEN") is None:
        raise ProvisionError("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment.")
    _required_env("DNSIMPLE_TOKEN")
    if not plan.ssh_public_key_file.exists():
        raise ProvisionError(f"Missing SSH public key file: {plan.ssh_public_key_file}")


def _linode_token_env() -> str:
    token = _env_or_none("LINODE_CLI_TOKEN")
    if token is not None:
        return token
    token = _env_or_none("LINODE_TOKEN")
    if token is not None:
        return token
    raise ProvisionError("Expected LINODE_CLI_TOKEN or LINODE_TOKEN in the environment.")


def _run_linode_cli(args: list[str]) -> dict[str, Any]:
    env = os.environ.copy()
    token = _linode_token_env()
    env.setdefault("LINODE_CLI_TOKEN", token)
    env.setdefault("LINODE_TOKEN", token)
    completed = subprocess.run(
        ["linode-cli", "--suppress-warnings", "--json", *args],
        check=False,
        capture_output=True,
        text=True,
        env=env,
    )
    if completed.returncode != 0:
        stderr = completed.stderr.strip()
        stdout = completed.stdout.strip()
        details = stderr or stdout or "linode-cli failed"
        raise ProvisionError(details)
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise ProvisionError(f"Unexpected linode-cli JSON output: {exc}") from exc


def _linode_payload(data: Any) -> dict[str, Any]:
    if isinstance(data, list) and data and isinstance(data[0], dict):
        return data[0]
    if isinstance(data, dict):
        return data
    raise ProvisionError("Unexpected Linode CLI response while creating the instance.")


def _prepare_plan(plan: ProvisionPlan) -> tuple[ProvisionPlan | None, Path | None]:
    preview = _render_cloud_init(plan, actual=False)
    preview_path = _write_cloud_init_preview_if_requested(plan, preview)
    preview_text = preview if plan.show_cloud_init else None
    print(render_plan(plan, preview_path, preview_text), end="", flush=True)

    if plan.non_interactive:
        if plan.execute and not plan.yes:
            raise ProvisionError("Remote execution requires both --execute and --yes.")
        return plan, preview_path

    if plan.execute and plan.yes:
        return plan, preview_path

    if not _gum_confirm("Execute remote provisioning?"):
        return None, preview_path
    if not _gum_confirm("This will create a Linode and DNS records. Continue?"):
        return None, preview_path
    return replace(plan, execute=True, yes=True, dry_run=False), preview_path


def provision_server(plan: ProvisionPlan) -> ProvisionResult | None:
    if plan.provider != "linode":
        raise ProvisionError(f"Unsupported provider {plan.provider!r}. Only linode is available in this slice.")

    effective_plan, preview_path = _prepare_plan(plan)
    if effective_plan is None:
        return None
    if not effective_plan.execute:
        return None

    _validate_actual_execution(effective_plan)

    salt = secret_salt()
    root_pass = derive_password(effective_plan.project_slug, "root", salt)
    db_pass = derive_password(effective_plan.project_slug, "db", salt)
    wp_admin_pass = derive_password(effective_plan.project_slug, "wp", salt)
    dnsimple_token = _required_env("DNSIMPLE_TOKEN")

    rendered = _render_cloud_init(
        effective_plan,
        actual=True,
        db_pass=db_pass,
        wp_admin_pass=wp_admin_pass,
        dnsimple_token=dnsimple_token,
    )

    ssh_public_key = effective_plan.ssh_public_key_file.read_text(encoding="utf-8").strip()
    linode_payload = _linode_payload(
        _run_linode_cli(
            [
                "linodes",
                "create",
                "--region",
                effective_plan.region,
                "--type",
                effective_plan.linode_type,
                "--image",
                effective_plan.image,
                "--label",
                effective_plan.label,
                "--root_pass",
                root_pass,
                "--authorized_keys",
                ssh_public_key,
                "--metadata.user_data",
                _base64_user_data(rendered),
            ]
        )
    )
    linode_id = str(linode_payload.get("id", ""))
    ipv4 = linode_payload.get("ipv4")
    if isinstance(ipv4, list) and ipv4:
        linode_ip = str(ipv4[0])
    elif isinstance(ipv4, str):
        linode_ip = ipv4
    else:
        raise ProvisionError("Linode response did not include an IPv4 address.")

    dns_zone = _find_dnsimple_zone(effective_plan, dnsimple_token)
    _dnsimple_upsert_a_record(
        dnsimple_token,
        effective_plan.dnsimple_account_id,
        dns_zone,
        _relative_record_name(effective_plan.site_domain, dns_zone),
        linode_ip,
    )
    _dnsimple_upsert_a_record(
        dnsimple_token,
        effective_plan.dnsimple_account_id,
        dns_zone,
        _relative_record_name(f"*.{effective_plan.site_domain}", dns_zone),
        linode_ip,
    )

    server_state_path = state_dir() / "servers.json"
    site_state_path = state_dir() / "sites.json"
    now = datetime.now(timezone.utc).isoformat()
    server_record = {
        "id": linode_id,
        "provider": effective_plan.provider,
        "project_slug": effective_plan.project_slug,
        "name": effective_plan.server_name,
        "label": effective_plan.label,
        "hostname": effective_plan.site_domain,
        "status": "provisioned",
        "linode_id": linode_id,
        "ipv4": linode_ip,
        "region": effective_plan.region,
        "type": effective_plan.linode_type,
        "image": effective_plan.image,
        "ssh_user": effective_plan.ssh_user,
        "remote_wp_path": effective_plan.remote_wp_path,
        "dns_zone": dns_zone,
        "created_at": now,
    }
    site_record = {
        "provider": effective_plan.provider,
        "project_slug": effective_plan.project_slug,
        "slug": effective_plan.project_slug,
        "name": effective_plan.project_slug,
        "hostname": effective_plan.site_domain,
        "site_url": f"https://{effective_plan.site_domain}",
        "server": effective_plan.server_name,
        "label": effective_plan.label,
        "status": "provisioned",
        "remote_wp_path": effective_plan.remote_wp_path,
        "db_name": effective_plan.db_name,
        "db_user": effective_plan.db_user,
        "wp_admin_user": effective_plan.wp_admin_user,
        "dns_zone": dns_zone,
        "created_at": now,
    }
    _upsert_state_record(server_state_path, server_record)
    _upsert_state_record(site_state_path, site_record)

    print(f"created linode id: {linode_id}")
    print(f"ipv4: {linode_ip}")
    print(f"dns zone: {dns_zone}")
    print(f"cloud-init preview: {preview_path or 'not written'}")
    print(f"state updated: {server_state_path}")
    print(f"state updated: {site_state_path}")
    return ProvisionResult(
        linode_id=linode_id,
        ipv4=linode_ip,
        dns_zone=dns_zone,
        server_state_path=server_state_path,
        site_state_path=site_state_path,
    )
