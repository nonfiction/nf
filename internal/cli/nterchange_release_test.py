"""Real filesystem/archive checks with Docker calls mocked. Not a container/CMS test."""
import copy
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import nterchange_release as worker


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='nf-release-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.root = self.base / 'site with spaces'
        for relative in ('var', 'public_html/upload', 'public_html/videos'):
            (self.root / relative).mkdir(parents=True, exist_ok=True)
        (self.root / 'public_html/index.php').write_text('BASELINE')
        (self.root / '.env').write_text('ENV-SENTINEL')
        (self.root / 'var/session').write_text('SESSION-SENTINEL')
        (self.root / 'public_html/videos/a.mp4').write_text('MEDIA-SENTINEL')
        self.recipe = self.base / 'compose.yaml'
        self.recipe.write_text('BASE-COMPOSE-SENTINEL')
        self.site = dict(name='client', env='review', kind='nterchange', url='https://client.example.invalid/', path=str(self.root), compose_file=str(self.recipe), service='app', database='client', media=['public_html/upload', 'public_html/videos'])
        self.inventory = self.base / 'inventory.json'
        self.inventory.write_text(json.dumps([self.site]))
        self.state = self.base / '.nf-releases/client-review'
        self.mounts = [dict(Type='bind', Source=str(self.root), Destination=worker.SITE, RW=False)]
        self.mounts += [dict(Type='bind', Source=str(self.root / p), Destination=worker.SITE + '/' + p, RW=rw) for p, rw in [('var', True), ('.env', False), ('public_html/upload', True)]]
        self.image = 'sha256:fixture'
        self.database_mounts = [dict(Type='bind', Source='/stable/mysql', Destination='/var/lib/mysql', RW=True),
                                dict(Type='bind', Source='/stable/client.cnf', Destination='/run/secrets/mysql-client.cnf', RW=False)]
        self.calls = []
        self.app_serial = 0
        self.fail_recovery = False
        self.fail_up_once = False

    def container(self, service):
        return dict(Id='database-fixed' if service == 'db' else 'app-' + str(self.app_serial), State=dict(Running=True), Image=self.image, Config=dict(Env=['SITE=unchanged']), Mounts=copy.deepcopy(self.database_mounts if service == 'db' else self.mounts), NetworkSettings=dict(Networks={'internal': {}}))

    def docker(self, args):
        self.calls.append(args)
        if args[:2] == ['docker', 'compose']:
            if args[-2:] == ['config', 'json'] or args[-3:] == ['config', '--format', 'json']:
                return b'UNCHANGED-CONFIG'
            if 'ps' in args:
                return args[-1].encode()
            if 'up' in args:
                self.assertEqual(args[-1], 'app')
                for flag in ('--no-deps', '--no-build', '--force-recreate'):
                    self.assertIn(flag, args)
                if self.fail_up_once:
                    self.fail_up_once = False
                    raise RuntimeError('injected recreation error')
                overlay = json.loads((self.state / 'active.compose.json').read_bytes())
                self.mounts = [dict(Type='bind', Source=m['source'], Destination=m['target'], RW=not m['read_only']) for m in overlay['services']['app']['volumes']]
                self.app_serial += 1
                return b''
        if args[:2] == ['docker', 'inspect']:
            return json.dumps([self.container(args[-1])]).encode()
        if args[:2] == ['docker', 'exec']:
            root = Path(next(m['Source'] for m in self.mounts if m['Destination'] == worker.SITE))
            if self.fail_recovery or 'BAD' in (root / 'public_html/index.php').read_text():
                raise RuntimeError('injected HTTP failure')
            return b''
        self.fail('unexpected command ' + str(args))

    def archive(self, text='GOOD', bad_member=None):
        files = {'nf.json': '{}', 'composer.json': '{}', 'composer.lock': '{}', 'conf/conf.php': '<?php', 'public_html/index.php': text, 'vendor/autoload.php': '<?php'}
        manifest = dict(version=1, project='client', commit='1' * 40, excluded_media=self.site['media'], composer_lock_sha256=hashlib.sha256(b'{}').hexdigest(), dependencies=[], files=[dict(path=p, size=len(data), mode='0644', sha256=hashlib.sha256(data.encode()).hexdigest()) for p, data in files.items()])
        body = io.BytesIO()
        with tarfile.open(fileobj=body, mode='w') as archive:
            for name, data in files.items():
                record = tarfile.TarInfo(bad_member or name)
                record.size = len(data)
                archive.addfile(record, io.BytesIO(data.encode()))
        return manifest, body.getvalue()

    def invoke(self, operation='deploy', text='GOOD', bad_member=None):
        manifest, archive = self.archive(text, bad_member)
        request = dict(operation=operation, site=self.site)
        if operation == 'deploy':
            request['manifest'] = manifest
        stream = io.BytesIO(json.dumps(request).encode() + b'\n' + (archive if operation == 'deploy' else b''))
        opened = []
        real_open = os.open
        def track(path, *args, **kwargs):
            fd = real_open(path, *args, **kwargs)
            if str(path).endswith('operation.lock'):
                opened.append(fd)
            return fd
        try:
            with patch.object(worker, 'INVENTORY', self.inventory), patch.object(worker, 'run', self.docker), patch.object(worker.time, 'sleep', lambda _: None), patch.object(worker.os, 'open', track):
                worker.main(stream)
        finally:
            for fd in opened:
                os.close(fd)

    def test_activate_twice_and_rollback_preserves_data(self):
        before = {p: (self.root / p).read_bytes() for p in ('.env', 'var/session', 'public_html/videos/a.mp4', 'public_html/index.php')}
        self.invoke()
        first = json.loads((self.state / 'state.json').read_bytes())['current']
        self.invoke(text='GOOD SECOND')
        self.invoke('rollback')
        state = json.loads((self.state / 'state.json').read_bytes())
        self.assertEqual(state['current'], first)
        for p, data in before.items():
            self.assertEqual((self.root / p).read_bytes(), data)
        self.assertFalse((self.state / 'pending.json').exists())
        self.assertTrue(any(m['Destination'] == worker.SITE + '/public_html/videos' and not m['RW'] for m in self.mounts))

    def test_reordered_database_mounts_allow_activation_recovery_and_rollback(self):
        original = self.container
        def inspect(service):
            value = original(service)
            if service == 'db' and self.app_serial % 2:
                value['Mounts'].reverse()
            return value
        with patch.object(self, 'container', inspect):
            self.invoke()
            first_state = (self.state / 'state.json').read_bytes()
            with self.assertRaisesRegex(RuntimeError, 'previous code restored'):
                self.invoke(text='BAD')
            self.assertEqual((self.state / 'state.json').read_bytes(), first_state)
            self.assertFalse((self.state / 'pending.json').exists())
            self.invoke(text='GOOD SECOND')
            self.invoke('rollback')
            self.assertEqual(json.loads((self.state / 'state.json').read_bytes())['current'], json.loads(first_state)['current'])
            self.assertFalse((self.state / 'pending.json').exists())

    def test_database_identity_and_mount_changes_still_fail_health(self):
        self.invoke()
        previous_state = (self.state / 'state.json').read_bytes()
        original = self.container
        for change in ('id', 'source', 'destination', 'access', 'type', 'missing'):
            with self.subTest(change=change):
                def inspect(service):
                    value = original(service)
                    if service == 'db' and self.app_serial % 2 == 0:
                        if change == 'id':
                            value['Id'] = 'replaced-database'
                        elif change == 'missing':
                            value['Mounts'].pop()
                        else:
                            field, replacement = {'source': ('Source', '/other/mysql'), 'destination': ('Destination', '/other/data'),
                                                  'access': ('RW', False), 'type': ('Type', 'volume')}[change]
                            value['Mounts'][0][field] = replacement
                    return value
                with patch.object(self, 'container', inspect), self.assertRaisesRegex(RuntimeError, 'previous code restored'):
                    self.invoke(text='GOOD CANDIDATE')
                self.assertEqual((self.state / 'state.json').read_bytes(), previous_state)
                self.assertFalse((self.state / 'pending.json').exists())

    def test_first_rollback_uses_untouched_baseline(self):
        self.invoke()
        self.invoke('rollback')
        self.assertIsNone(json.loads((self.state / 'state.json').read_bytes())['current'])

    def test_failed_health_and_recreation_restore_previous(self):
        self.invoke()
        before = (self.state / 'state.json').read_bytes()
        with self.assertRaisesRegex(RuntimeError, 'previous code restored'):
            self.invoke(text='BAD')
        self.assertEqual((self.state / 'state.json').read_bytes(), before)
        self.fail_up_once = True
        with self.assertRaisesRegex(RuntimeError, 'previous code restored'):
            self.invoke()
        self.assertEqual((self.state / 'state.json').read_bytes(), before)
        self.assertFalse((self.state / 'pending.json').exists())

    def test_failed_recovery_keeps_journal_and_blocks_next(self):
        self.invoke()
        self.fail_recovery = True
        with self.assertRaisesRegex(RuntimeError, 'recovery failed'):
            self.invoke(text='BAD')
        self.assertTrue((self.state / 'pending.json').exists())
        with self.assertRaisesRegex(RuntimeError, 'manual repair'):
            self.invoke()

    def test_bad_archives_never_recreate_and_are_removed(self):
        for name in ('../escape', 'public_html/upload/secret', '/absolute'):
            with self.assertRaises(RuntimeError):
                self.invoke(bad_member=name)
            self.assertFalse(any('up' in call for call in self.calls))
            self.assertFalse(list(self.state.glob('.upload-*')))

    def test_inventory_and_mount_drift_rejected(self):
        self.inventory.write_text('[]')
        with self.assertRaisesRegex(RuntimeError, 'identity'):
            self.invoke()
        self.inventory.write_text(json.dumps([self.site]))
        self.mounts[0]['RW'] = True
        with self.assertRaisesRegex(RuntimeError, 'root mount'):
            self.invoke()
        self.assertFalse(any('up' in call for call in self.calls))

    def test_package_or_original_code_tampering_rejected(self):
        self.invoke()
        state = json.loads((self.state / 'state.json').read_bytes())
        original = self.root / 'public_html/index.php'
        original.write_text('changed original code')
        with self.assertRaisesRegex(RuntimeError, 'Original site code changed'):
            self.invoke()
        original.write_text('BASELINE')
        (self.state / state['current'] / 'code/public_html/index.php').write_text('tampered')
        with self.assertRaisesRegex(RuntimeError, 'integrity'):
            self.invoke('rollback')

    def test_runtime_mount_slots_are_empty_and_verified(self):
        self.invoke()
        state = json.loads((self.state / 'state.json').read_bytes())
        code = self.state / state['current'] / 'code'
        self.assertEqual((code / '.env').read_bytes(), b'')
        for relative in ['var'] + self.site['media']:
            self.assertEqual(list((code / relative).iterdir()), [])
        (code / '.env').write_text('NOT-AN-EMPTY-MOUNTPOINT')
        with self.assertRaisesRegex(RuntimeError, 'mountpoint must remain empty'):
            self.invoke('rollback')

    def read_root(self, mounted=None, image=None):
        request = dict(root=str(self.root), compose=str(self.recipe), service='app', media=self.site['media'], database='client', image=image or self.image,
                       mounted=mounted or next(m['Source'] for m in self.mounts if m['Destination'] == worker.SITE))
        before = {str(p.relative_to(self.base)): p.read_bytes() for p in self.base.rglob('*') if p.is_file()}
        calls_before = len(self.calls)
        with patch.object(worker, 'INVENTORY', self.inventory), patch.object(worker, 'run', self.docker), patch.object(worker, 'atomic_json', side_effect=AssertionError('read-only inspector attempted a write')):
            result = worker.inspect_root(request)
        self.assertEqual(before, {str(p.relative_to(self.base)): p.read_bytes() for p in self.base.rglob('*') if p.is_file()})
        self.assertTrue(all(call[-3:] == ['config', '--format', 'json'] for call in self.calls[calls_before:]))
        return result

    def test_read_only_root_inspection_baseline_managed_and_rollback(self):
        self.assertEqual(self.read_root(), {'root': str(self.root), 'managed': False})
        self.assertFalse(self.state.parent.exists())
        self.invoke()
        selected = next(m['Source'] for m in self.mounts if m['Destination'] == worker.SITE)
        self.assertEqual(self.read_root(), {'root': selected, 'managed': True})
        self.invoke('rollback')
        self.assertEqual(self.read_root(), {'root': str(self.root), 'managed': True})

    def test_read_only_root_inspection_rejects_drift(self):
        self.invoke()
        state_path = self.state / 'state.json'
        state_bytes = state_path.read_bytes()
        state = json.loads(state_bytes)
        with self.assertRaisesRegex(RuntimeError, 'recorded release'):
            self.read_root(mounted=str(self.root))
        with self.assertRaisesRegex(RuntimeError, 'site/image'):
            self.read_root(image='sha256:wrong-image')
        state_path.unlink()
        with self.assertRaisesRegex(RuntimeError, 'Missing state'):
            self.read_root()
        state_path.write_bytes(state_bytes)
        state['site']['database'] = 'other'
        state_path.write_text(json.dumps(state))
        with self.assertRaisesRegex(RuntimeError, 'site/image'):
            self.read_root()
        state_path.write_bytes(state_bytes)
        journal = self.state / 'pending.json'
        journal.write_text('{}')
        with self.assertRaisesRegex(RuntimeError, 'Pending release'):
            self.read_root()
        journal.unlink()
        with open(self.state / 'operation.lock', 'r+') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(RuntimeError, 'in progress'):
                self.read_root()
        code = self.state / state['current'] / 'code'
        (code / 'public_html/index.php').write_text('TAMPERED')
        with self.assertRaisesRegex(RuntimeError, 'integrity'):
            self.read_root()

    def test_read_only_root_inspection_rejects_symlinked_state(self):
        outside = self.base / 'outside'
        outside.mkdir()
        self.state.parent.symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, 'symlinked directory'):
            self.read_root()

    def test_concurrent_lock_blocks_without_recreate(self):
        self.state.mkdir(parents=True)
        with open(self.state / 'operation.lock', 'w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(RuntimeError, 'Another release'):
                self.invoke()
        self.assertFalse(any('up' in call for call in self.calls))


if __name__ == '__main__':
    unittest.main()
