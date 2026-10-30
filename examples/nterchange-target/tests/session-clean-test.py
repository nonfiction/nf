"""Run with Python 3; all files are disposable, with a synthetic future clock."""
import fcntl
import importlib.util
import os
from pathlib import Path
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('session_clean', Path(__file__).resolve().parents[1] / 'session-clean.py')
cleaner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleaner)


class SessionCleanupTest(unittest.TestCase):
    def test_expired_recent_open_and_unrelated_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            now = time.time() + cleaner.LIFETIME + 120
            expired = root / 'sess_expired'
            expired.write_bytes(b'synthetic expired authentication')
            recent = root / 'sess_recent'
            recent.write_bytes(b'synthetic active authentication')
            os.utime(recent, (now, now))
            locked = root / 'sess_locked'
            locked.write_bytes(b'synthetic open request')
            unrelated = root / '.htaccess'
            unrelated.write_bytes(b'Require all denied')
            outside = root / 'outside'
            outside.write_bytes(b'not a session')
            link = root / 'sess_link'
            link.symlink_to(outside)
            hardlink = root / 'sess_hardlink'
            os.link(outside, hardlink)
            directory = root / 'sess_directory'
            directory.mkdir()
            snapshot = {p: (p.read_bytes(), p.stat()) for p in [recent, locked, unrelated, outside]}
            with locked.open('rb') as held:
                fcntl.flock(held, fcntl.LOCK_EX)
                dry = cleaner.clean_directory(root, now, True)
                self.assertEqual(dry, dict(eligible=1, removed=0, locked=1))
                self.assertTrue(expired.exists())
                result = cleaner.clean_directory(root, now)
                self.assertEqual(result, dict(eligible=1, removed=1, locked=1))
            self.assertFalse(expired.exists())
            self.assertTrue(link.is_symlink())
            self.assertTrue(hardlink.exists())
            self.assertTrue(directory.is_dir())
            for path, (content, before) in snapshot.items():
                after = path.stat()
                self.assertEqual(path.read_bytes(), content)
                self.assertEqual((after.st_ino, after.st_mtime_ns, after.st_mode, after.st_uid, after.st_gid),
                                 (before.st_ino, before.st_mtime_ns, before.st_mode, before.st_uid, before.st_gid))
            # A freshly restored file with an old mtime must survive its initial lifetime.
            self.assertEqual(cleaner.clean_directory(root, time.time())['removed'], 0)

    def test_inventory_rejects_missing_shared_and_symlinked_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            sessions = root / 'var/sessions'
            sessions.mkdir(parents=True)
            site = dict(kind='nterchange', path=str(root))
            self.assertEqual(cleaner.session_directories([site]), [sessions])
            with self.assertRaises(ValueError):
                cleaner.session_directories([site, site])
            sessions.rmdir()
            with self.assertRaises(FileNotFoundError):
                cleaner.session_directories([site])
            other = root / 'other'
            other.mkdir()
            sessions.symlink_to(other)
            with self.assertRaises(ValueError):
                cleaner.session_directories([site])


if __name__ == '__main__':
    unittest.main()
