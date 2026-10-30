#!/usr/bin/env python3
"""Clean expired file sessions for nterchange inventory records."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import time

LIFETIME = 1440


def session_directories(inventory):
    directories = []
    for site in inventory:
        if site['kind'] != 'nterchange':
            continue
        root = Path(site['path'])
        directory = root / 'var/sessions'
        if not root.is_absolute() or directory.resolve(strict=True) != directory or not directory.is_dir():
            raise ValueError('Session directory missing or symlinked: ' + str(directory))
        if directory in directories:
            raise ValueError('Shared session directory: ' + str(directory))
        directories.append(directory)
    if not directories:
        raise ValueError('No CMS session directories in inventory')
    return directories


def clean_directory(directory, now, dry_run=False):
    result = dict(eligible=0, removed=0, locked=0)
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
    with os.scandir(directory) as entries:
        for entry in entries:
            if not re.fullmatch(r'sess_[A-Za-z0-9,-]{1,256}', entry.name) or not entry.is_file(follow_symlinks=False):
                continue
            try:
                fd = os.open(entry.path, flags)
            except FileNotFoundError:
                continue  # A request can destroy or regenerate its session concurrently.
            try:
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    result['locked'] += 1
                    continue
                info = os.fstat(fd)
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                    continue
                # ctime also protects restored files and matches the old Ubuntu cleanup.
                if max(info.st_mtime, info.st_ctime) >= now - LIFETIME:
                    continue
                current = os.stat(entry.path, follow_symlinks=False)
                if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
                    continue
                result['eligible'] += 1
                if not dry_run:
                    os.unlink(entry.path)
                    result['removed'] += 1
            except FileNotFoundError:
                continue
            finally:
                os.close(fd)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    inventory = json.loads(Path('/var/lib/nf/sites.json').read_text())
    # Validate every directory before deleting anything. Never log session IDs or data.
    directories = session_directories(inventory)
    now = time.time()
    result = {str(p): clean_directory(p, now, args.dry_run) for p in directories}
    print(json.dumps(dict(dry_run=args.dry_run, lifetime_seconds=LIFETIME, directories=result), sort_keys=True))


if __name__ == '__main__':
    main()
