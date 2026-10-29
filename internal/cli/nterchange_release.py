"""Fixed SSH release worker. Input is one JSON line followed by a file-only tar stream."""
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from urllib.parse import urlsplit

INVENTORY = Path('/var/lib/nf/sites.json')
SITE = '/var/www/site'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def run(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
    require(result.returncode == 0, 'Command failed: ' + ' '.join(args[:3]))
    return result.stdout


def atomic_json(path, value):
    temporary = path.with_suffix('.tmp')
    with open(temporary, 'x') as out:
        os.chmod(temporary, 0o600)
        json.dump(value, out, sort_keys=True)
        out.flush()
        os.fsync(out.fileno())
    os.replace(temporary, path)
    directory = os.open(path.parent, os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def real_directory(path):
    require(path.is_dir() and path.resolve() == path, 'Missing or symlinked directory: ' + str(path))


def package_path(name, media):
    p = PurePosixPath(name)
    require(name == str(p) and not p.is_absolute() and '..' not in p.parts and '\\' not in name and not any(ord(c) < 32 for c in name), 'Unsafe package path')
    require(name in ('nf.json', 'composer.json', 'composer.lock') if len(p.parts) == 1 else p.parts[0] in ('app', 'conf', 'lib', 'public_html', 'vendor'), 'Unexpected package root: ' + name)
    for excluded in ['var', 'public_html/upload'] + media:
        require(name != excluded and not name.startswith(excluded + '/'), 'Package contains persistent data')
    for part in p.parts:
        lower = part.lower()
        # Match the Go packager's generated-path policy. PEAR's Log/ is library code, not runtime logs.
        require((not part.startswith('.') or part == '.htaccess') and lower not in ('var', 'cache', 'caches', 'tmp', 'temp', 'logs', 'sessions', 'scratch', 'backups', 'node_modules', 'auth.json', 'credentials.json', 'credentials.yml', 'credentials.yaml', 'credentials.ini', 'secrets.json', 'secrets.yml', 'secrets.yaml', 'secrets.ini', 'id_rsa', 'id_ed25519') and not lower.startswith('sess_') and '.sql.' not in lower and '.env.' not in lower and not lower.endswith(('.env', '.pem', '.key', '.p12', '.pfx', '.sql', '.sql.gz', '.sql.bz2', '.sql.zip', '.sqlite', '.sqlite3', '.db', '.log', '.bak')), 'Package contains excluded data: ' + name)
    return p


def verify_code(code, manifest, mountpoints=False):
    expected = {f['path']: f for f in manifest['files']}
    require(len(expected) == len(manifest['files']) and expected, 'Duplicate or empty package file list')
    seen = set()
    for directory, dirs, files in os.walk(code, followlinks=False):
        # os.walk puts links to directories in dirs. Verify those as links, never traverse them.
        for name in list(dirs):
            if (Path(directory) / name).is_symlink():
                dirs.remove(name)
                files.append(name)
        for name in files:
            full = Path(directory) / name
            relative = full.relative_to(code).as_posix()
            if mountpoints and relative == '.env':
                require(not full.is_symlink() and full.is_file() and full.stat().st_size == 0, 'Environment mountpoint must remain empty')
                continue
            package_path(relative, manifest['excluded_media'])
            require(relative in expected, 'Unlisted package file')
            record = expected[relative]
            info = full.lstat()
            if full.is_symlink():
                target = os.readlink(full)
                require(target == record.get('target') and not os.path.isabs(target) and full.resolve().is_relative_to(code) and full.exists(), 'Unsafe package link')
                package_path(full.resolve().relative_to(code).as_posix(), manifest['excluded_media'])
                content = target.encode()
            else:
                require(full.is_file() and not record.get('target'), 'Unsupported package file')
                content = full.read_bytes()
            require(len(content) == record['size'] and digest(content) == record['sha256'] and format(info.st_mode & 0o777, '04o') == record['mode'], 'Package integrity mismatch: ' + relative)
            seen.add(relative)
    require(seen == set(expected), 'Missing package file')
    for name in ('nf.json', 'composer.json', 'composer.lock', 'conf/conf.php', 'public_html/index.php', 'vendor/autoload.php'):
        require(name in expected and not expected[name].get('target') and expected[name]['size'] > 0, 'Missing required package input')
    require(digest((code / 'composer.lock').read_bytes()) == manifest['composer_lock_sha256'], 'Lock integrity mismatch')


def receive_package(stream, directory, manifest):
    code = directory / 'code'
    code.mkdir(mode=0o755)
    expected = {f['path']: f for f in manifest['files']}
    require(len(expected) == len(manifest['files']), 'Duplicate package records')
    seen = set()
    links = []
    with tarfile.open(fileobj=stream, mode='r|') as archive:
        for member in archive:
            package_path(member.name, manifest['excluded_media'])
            require(member.name in expected and member.name not in seen, 'Unexpected archive member')
            record = expected[member.name]
            seen.add(member.name)
            target = code / member.name
            # Delay links until all ordinary files exist, so no archive write traverses one.
            if member.issym():
                require(member.linkname == record.get('target'), 'Archive link differs from manifest')
                links.append((target, member.linkname))
                continue
            require(member.isfile() and not record.get('target') and member.size == record['size'], 'Unsupported archive member')
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
            with archive.extractfile(member) as source, open(target, 'xb') as out:
                shutil.copyfileobj(source, out)
            mode = int(record['mode'], 8)
            require(mode in (0o600, 0o644, 0o755), 'Unsupported package mode')
            os.chmod(target, mode)
    require(seen == set(expected), 'Missing archive member')
    for target, value in links:
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
        target.symlink_to(value)
    for parent, dirs, _ in os.walk(code, followlinks=False):
        os.chmod(parent, 0o755)
        for name in dirs:
            if not (Path(parent) / name).is_symlink():
                os.chmod(Path(parent) / name, 0o755)
    verify_code(code, manifest)
    atomic_json(directory / 'manifest.json', manifest)
    # runc cannot create nested mount targets inside the read-only code bind.
    # These empty slots contain no runtime data or credentials; stable host binds cover them.
    for relative in ['var'] + manifest['excluded_media']:
        slot = code / relative
        slot.mkdir(parents=True, exist_ok=True)
        os.chmod(slot, 0o755)
    (code / '.env').touch(mode=0o600)


def baseline_fingerprint(root, media):
    baseline = hashlib.sha256()
    for directory, dirs, files in os.walk(root, followlinks=False):
        parent = Path(directory)
        for name in list(dirs):
            relative = (parent / name).relative_to(root).as_posix()
            if relative in ['var'] + media or (parent == root and name not in ('app', 'conf', 'lib', 'public_html', 'vendor')):
                dirs.remove(name)
            elif (parent / name).is_symlink():
                dirs.remove(name)
                files.append(name)
        dirs.sort()
        for name in sorted(files):
            full = parent / name
            relative = full.relative_to(root).as_posix()
            if parent == root and name not in ('nf.json', 'composer.json', 'composer.lock'):
                continue
            content = os.readlink(full).encode() if full.is_symlink() else full.read_bytes()
            baseline.update(json.dumps([relative, full.lstat().st_mode & 0o777, digest(content)]).encode())
    return baseline.hexdigest()


def inspect_root(request):
    """Read-only pull preflight. Never initialize state, recreate a service or request HTTP."""
    root = Path(request['root'])
    real_directory(root)
    inventory = json.loads(INVENTORY.read_bytes())
    matches = [s for s in inventory if s['path'] == str(root) and s['compose_file'] == request['compose'] and s['service'] == request['service']]
    require(len(matches) == 1, 'Managed-root inventory identity missing or duplicated')
    site = dict(matches[0])
    site['media'] = site.get('media') or ['public_html/upload']
    require(site['kind'] == 'nterchange' and site['media'] == request['media'] and site['database'] == request['database'], 'Managed-root inventory differs from pull source')
    for field in ('name', 'env'):
        require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]*', site[field]), 'Unsafe managed-root identity')
    state_parent = root.parent / '.nf-releases'
    state_dir = state_parent / (site['name'] + '-' + site['env'])
    if not state_parent.exists() and not state_parent.is_symlink():
        require(request['mounted'] == str(root), 'Unmanaged code root differs from inventory')
        return {'root': str(root), 'managed': False}
    real_directory(state_parent)
    if not state_dir.exists() and not state_dir.is_symlink():
        require(request['mounted'] == str(root), 'Missing state for managed code root')
        return {'root': str(root), 'managed': False}
    real_directory(state_dir)
    lock_path = state_dir / 'operation.lock'
    lock = None
    try:
        if lock_path.exists() or lock_path.is_symlink():
            lock = os.open(lock_path, os.O_RDONLY | os.O_NOFOLLOW)
            try:
                fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
            except BlockingIOError:
                raise RuntimeError('Release operation in progress; retry pull after review')
        journal = state_dir / 'pending.json'
        require(not journal.exists() and not journal.is_symlink(), 'Pending release recovery blocks pull preflight')
        state_file = state_dir / 'state.json'
        if not state_file.exists() and not state_file.is_symlink():
            require(request['mounted'] == str(root), 'Missing state for managed code root')
            return {'root': str(root), 'managed': False}
        require(lock is not None and state_file.is_file() and not state_file.is_symlink(), 'Invalid managed release state or lock')
        state = json.loads(state_file.read_bytes())
        require(state['site'] == site and state['image'] == request['image'], 'Managed release site/image differs from running app')
        compose_hash = digest(run(['docker', 'compose', '-f', request['compose'], 'config', '--format', 'json']))
        require(state['compose_sha256'] == compose_hash and state['baseline_sha256'] == baseline_fingerprint(root, site['media']), 'Managed release base recipe/original code changed')
        current = state['current']
        require(current is None or re.fullmatch(r'release-[a-zA-Z0-9_-]+', current), 'Unsafe managed release ID')
        expected = root
        if current is not None:
            directory = state_dir / current
            real_directory(directory)
            expected = directory / 'code'
            real_directory(expected)
            manifest_file = directory / 'manifest.json'
            require(manifest_file.is_file() and not manifest_file.is_symlink(), 'Invalid managed package manifest')
            manifest = json.loads(manifest_file.read_bytes())
            require(manifest['version'] == 1 and manifest['project'] == site['name'] and manifest['excluded_media'] == site['media'], 'Managed package identity differs from inventory')
            verify_code(expected, manifest, mountpoints=True)
        require(request['mounted'] == str(expected), 'Running code root differs from recorded release')
        return {'root': str(expected), 'managed': True}
    finally:
        if lock is not None:
            os.close(lock)


def main(stream):
    os.umask(0o077)
    header = stream.readline(8 * 1024 * 1024 + 1)
    require(header.endswith(b'\n') and len(header) <= 8 * 1024 * 1024, 'Invalid request header')
    request = json.loads(header)
    site = request['site']
    require(request['operation'] in ('deploy', 'rollback'), 'Unsupported operation')
    current_inventory = json.loads(INVENTORY.read_bytes())
    matches = [s for s in current_inventory if s['name'] == site['name'] and s['env'] == site['env']]
    require(len(matches) == 1, 'Site identity missing or duplicated in current inventory')
    actual = dict(matches[0])
    actual['media'] = actual.get('media') or ['public_html/upload']
    require(actual == site, 'Remote inventory changed; refresh and review again')
    require(sum(s['compose_file'] == site['compose_file'] and s['service'] == site['service'] for s in current_inventory) == 1, 'App service is shared by inventory records')
    require(site['kind'] == 'nterchange', 'Not a nterchange environment')
    for field in ('name', 'env', 'service'):
        require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]*', site[field]), 'Unsafe site identifier')
    root, recipe = Path(site['path']), Path(site['compose_file'])
    real_directory(root)
    require(recipe.is_file() and recipe.resolve() == recipe, 'Missing or symlinked Compose recipe')
    state_parent = root.parent / '.nf-releases'
    if not state_parent.exists():
        state_parent.mkdir(mode=0o700)
    real_directory(state_parent)
    state_dir = state_parent / (site['name'] + '-' + site['env'])
    if not state_dir.exists():
        state_dir.mkdir(mode=0o700)
    real_directory(state_dir)
    lock = os.open(state_dir / 'operation.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise RuntimeError('Another release operation holds this site lock')
    journal = state_dir / 'pending.json'
    require(not journal.exists(), 'Interrupted/failed recovery requires manual repair: ' + str(journal))
    state_file = state_dir / 'state.json'
    state = json.loads(state_file.read_bytes()) if state_file.exists() else None
    base = ['docker', 'compose', '-f', str(recipe)]
    app_service = site['service']

    def inspect(service):
        ids = run(base + ['ps', '-q', service]).decode().split()
        require(len(ids) == 1, 'Exactly one running container required: ' + service)
        value = json.loads(run(['docker', 'inspect', ids[0]]))[0]
        require(value['State']['Running'] and not value['State'].get('Paused') and value['State'].get('Health', {}).get('Status', 'healthy') == 'healthy', 'Container not healthy: ' + service)
        return value

    original, database = inspect(app_service), inspect('db')
    compose_hash = digest(run(base + ['config', '--format', 'json']))
    mount_root = [m for m in original['Mounts'] if m['Destination'] == SITE]
    if state:
        for release in (state['current'], state.get('previous')):
            require(release is None or re.fullmatch(r'release-[a-zA-Z0-9_-]+', release), 'Unsafe recorded release ID')
        if state['current'] is not None:
            current_dir = state_dir / state['current']
            verify_code(current_dir / 'code', json.loads((current_dir / 'manifest.json').read_bytes()), mountpoints=True)
    expected_root = str(root) if state is None or state['current'] is None else str(state_dir / state['current'] / 'code')
    require(len(mount_root) == 1 and mount_root[0]['Type'] == 'bind' and not mount_root[0]['RW'] and mount_root[0]['Source'] == expected_root, 'App root mount differs from managed release')
    persistent = []
    allowed = {SITE + '/var', SITE + '/.env'} | {SITE + '/' + p for p in site['media']}
    for mount in original['Mounts']:
        if mount['Destination'].startswith(SITE + '/'):
            require(mount['Destination'] in allowed and mount['Type'] == 'bind', 'Unrecognized nested application mount')
            persistent.append(mount)
    env_mount = [m for m in persistent if m['Destination'] == SITE + '/.env']
    require(len(env_mount) == 1 and not env_mount[0]['RW'], 'Missing read-only environment bind')
    environment_file = Path(env_mount[0]['Source'])
    require(environment_file.is_file() and environment_file.resolve() == environment_file, 'Environment bind must resolve to a regular stable file')
    for relative, writable in [('var', True), ('public_html/upload', True)]:
        require(any(m['Destination'] == SITE + '/' + relative and m['Source'] == str(root / relative) and m['RW'] == writable for m in persistent), 'Missing stable var/env/upload bind')
    for relative in site['media']:
        path = root / relative
        real_directory(path)
        existing = [m for m in persistent if m['Destination'] == SITE + '/' + relative]
        require(not existing or existing[0]['Source'] == str(path), 'Media bind source differs from stable site path')
        if not existing:
            persistent.append({'Type': 'bind', 'Source': str(path), 'Destination': SITE + '/' + relative, 'RW': False})
    if state:
        require(state['compose_sha256'] == compose_hash and state['image'] == original['Image'] and state['site'] == site, 'Base Compose, image or inventory changed; manual review required')
    # The initial unmanaged code tree is never renamed or copied. Detect later host edits before relying on it for recovery.
    baseline_hash = baseline_fingerprint(root, site['media'])
    if state:
        require(state['baseline_sha256'] == baseline_hash, 'Original site code changed since first activation; manual review required')
    previous = state['current'] if state else None
    if request['operation'] == 'rollback':
        require(state is not None and 'previous' in state and state['previous'] != previous, 'No previous release to restore')
        selected = state['previous']
        if selected is not None:
            require(re.fullmatch(r'release-[a-zA-Z0-9_-]+', selected), 'Unsafe release ID')
            verify_code(state_dir / selected / 'code', json.loads((state_dir / selected / 'manifest.json').read_bytes()), mountpoints=True)
    else:
        manifest = request['manifest']
        require(manifest['version'] == 1 and manifest['project'] == site['name'] and manifest['excluded_media'] == site['media'] and re.fullmatch(r'[0-9a-f]{40}([0-9a-f]{24})?', manifest['commit']), 'Package identity differs from target')
        directory = Path(tempfile.mkdtemp(prefix='.upload-', dir=state_dir))
        try:
            receive_package(stream, directory, manifest)
            selected = 'release-' + directory.name.removeprefix('.upload-')
            directory.rename(state_dir / selected)
        except BaseException:
            shutil.rmtree(directory)
            raise

    def overlay(release):
        source = str(root) if release is None else str(state_dir / release / 'code')
        volumes = [{'type': 'bind', 'source': source, 'target': SITE, 'read_only': True, 'bind': {'create_host_path': False}}]
        volumes += [{'type': 'bind', 'source': m['Source'], 'target': m['Destination'], 'read_only': not m['RW'], 'bind': {'create_host_path': False}} for m in persistent]
        return {'services': {app_service: {'image': original['Image'], 'volumes': volumes}}}

    def switch(release):
        override = state_dir / 'active.compose.json'
        atomic_json(override, overlay(release))
        run(base + ['-f', str(override), 'up', '-d', '--no-deps', '--no-build', '--pull', 'never', '--force-recreate', app_service])

    def healthy(release):
        expected = str(root) if release is None else str(state_dir / release / 'code')
        host = urlsplit(site['url']).netloc
        require(host and '\r' not in host and '\n' not in host, 'Invalid health Host')
        php = '$c=stream_context_create(["http"=>["timeout"=>3,"ignore_errors"=>true,"follow_location"=>0,"header"=>"Host: ".' + 'hex2bin("' + host.encode().hex() + '")' + '."\\r\\n"]]); @file_get_contents("http://127.0.0.1/",false,$c); exit(isset($http_response_header[0]) && preg_match("~^HTTP/\\S+ 200(?: |$)~",$http_response_header[0]) ? 0 : 1);'
        # Docker can reorder unchanged mount records between inspections.
        database_mounts = sorted(database['Mounts'], key=lambda mount: mount['Destination'])
        failure = None
        for _ in range(15):
            try:
                app = inspect(app_service)
                require(app['Image'] == original['Image'] and sorted(app['Config']['Env']) == sorted(original['Config']['Env']), 'App image/environment changed')
                require(app['NetworkSettings']['Networks'].keys() == original['NetworkSettings']['Networks'].keys(), 'App networks changed')
                root_mount = [m for m in app['Mounts'] if m['Destination'] == SITE]
                require(len(root_mount) == 1 and root_mount[0]['Source'] == expected and not root_mount[0]['RW'], 'Release root not mounted read-only')
                for mount in persistent:
                    require(any(m['Destination'] == mount['Destination'] and m['Source'] == mount['Source'] and m['RW'] == mount['RW'] for m in app['Mounts']), 'Persistent bind changed')
                db = inspect('db')
                require(db['Id'] == database['Id'] and sorted(db['Mounts'], key=lambda mount: mount['Destination']) == database_mounts, 'Database container/storage changed')
                run(['docker', 'exec', app['Id'], 'php', '-r', php])
                return
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                failure = error
                time.sleep(1)
        raise RuntimeError('Container-local HTTP/mount health failed: ' + str(failure))

    # Persist intent before recreating the app. SIGKILL/disconnect can require manual repair.
    atomic_json(journal, {'from': previous, 'to': selected, 'site': site, 'recovery': 'Restore from with active.compose.json, recreate only app, verify health, then repair state/journal.'})
    try:
        switch(selected)
        healthy(selected)
        atomic_json(state_file, {'current': selected, 'previous': previous, 'site': site, 'image': original['Image'], 'compose_sha256': compose_hash, 'baseline_sha256': baseline_hash})
    except BaseException:
        try:
            switch(previous)
            healthy(previous)
        except BaseException:
            raise RuntimeError('Release failed AND recovery failed; manual repair required: ' + str(journal))
        journal.unlink()
        raise RuntimeError('Release failed; previous code restored and container-local HTTP/mount health passed')
    journal.unlink()
    print('Release operation passed. Current: ' + str(selected) + '; previous: ' + str(previous))
    print('State/recovery: ' + str(state_dir))


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--inspect-root':
            print(json.dumps(inspect_root(json.loads(sys.argv[2]))))
        else:
            main(sys.stdin.buffer)
    except Exception as error:
        print('nterchange release: ' + str(error), file=sys.stderr)
        sys.exit(1)
