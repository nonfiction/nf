import fcntl
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('provider_jobs', Path(__file__).resolve().parent.parent / 'provider-jobs.py')
jobs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(jobs)


class ProviderJobsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'sites/.nf-releases/client-live'
        self.state.mkdir(parents=True)
        (self.state / 'operation.lock').touch()
        self.locks = self.root / 'locks'
        self.locks.mkdir()
        self.config = self.root / 'jobs.json'
        self.inventory = self.root / 'sites.json'
        self.config.write_text(json.dumps({'client': {'site': 'client', 'public_host': 'www.client.example.com', 'action': 'news'}}))
        self.inventory.write_text(json.dumps([{'name': 'client', 'env': 'live', 'kind': 'nterchange', 'path': str(self.root / 'sites/client'), 'compose_file': '/etc/nf/nterchange/compose.yaml', 'service': 'client'}]))
        for name, value in [('CONFIG', self.config), ('INVENTORY', self.inventory), ('LOCKS', self.locks)]:
            patch = mock.patch.object(jobs, name, value)
            patch.start()
            self.addCleanup(patch.stop)

    def test_check_does_not_run_or_create_locks(self):
        with mock.patch.object(jobs.subprocess, 'check_output') as lookup, mock.patch.object(jobs.subprocess, 'run') as run:
            jobs.run_job('client', check=True)
            lookup.assert_not_called()
            run.assert_not_called()
        self.assertEqual(list(self.locks.iterdir()), [])

    def test_local_request_holds_shared_release_lock(self):
        def request(args, **kwargs):
            self.assertEqual(args[:4], ['docker', 'exec', '--user', 'www-data'])
            self.assertIn('http://127.0.0.1/nterchange/news_release/refresh', args[-1])
            self.assertIn('www.client.example.com', args[-1])
            self.assertIn("'follow_location'=>0", args[-1])
            with open(self.state / 'operation.lock', 'r') as other:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with mock.patch.object(jobs.subprocess, 'check_output', return_value='a' * 64 + '\n'), mock.patch.object(jobs.subprocess, 'run', side_effect=request):
            jobs.run_job('client')

    def test_pending_and_symlinked_state_refuse_requests(self):
        pending = self.state / 'pending.json'
        for broken_link in [False, True]:
            if broken_link:
                pending.symlink_to(self.root / 'missing')
            else:
                pending.write_text('{}')
            with self.assertRaises(ValueError), mock.patch.object(jobs.subprocess, 'run') as run:
                jobs.run_job('client')
            run.assert_not_called()
            pending.unlink()
        original = self.state.with_name('saved')
        self.state.rename(original)
        self.state.symlink_to(original, target_is_directory=True)
        with self.assertRaises(ValueError):
            jobs.run_job('client', check=True)

    def test_unknown_action_and_invalid_host_refused(self):
        for action, host in [('unknown', 'www.client.example.com'), ('news', 'client.example.com/other')]:
            self.config.write_text(json.dumps({'client': {'site': 'client', 'public_host': host, 'action': action}}))
            with self.assertRaises((ValueError, KeyError)):
                jobs.run_job('client', check=True)

    def test_failed_request_propagates_without_state_changes(self):
        with mock.patch.object(jobs.subprocess, 'check_output', return_value='a' * 64), mock.patch.object(jobs.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'fixture')):
            with self.assertRaises(subprocess.CalledProcessError):
                jobs.run_job('client')
        self.assertEqual([p.name for p in self.state.iterdir()], ['operation.lock'])


if __name__ == '__main__':
    unittest.main()
