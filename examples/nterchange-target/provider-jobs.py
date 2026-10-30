#!/usr/bin/python3
"""Run one configured local provider refresh. Timers stay disabled until cutover."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess

CONFIG = Path('/etc/nf/nterchange/provider-jobs.json')
INVENTORY = Path('/var/lib/nf/sites.json')
LOCKS = Path('/var/lib/nf/provider-jobs/locks')
ACTIONS = {
    'news': '/nterchange/news_release/refresh',
    'program-load': '/nterchange/program/load_programs',
    'program-reload': '/nterchange/program/reload_programs',
}


def checked_job(name):
    jobs = json.loads(CONFIG.read_text())
    job = jobs[name]
    if set(job) != {'site', 'public_host', 'action'}:
        raise ValueError('Unexpected provider job fields')
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*', job['site']):
        raise ValueError('Invalid site')
    if not re.fullmatch(r'[a-z0-9][a-z0-9.-]*', job['public_host']):
        raise ValueError('Invalid public host')
    path = ACTIONS[job['action']]
    records = [s for s in json.loads(INVENTORY.read_text())
               if s['name'] == job['site'] and s['env'] == 'live' and s['kind'] == 'nterchange']
    if len(records) != 1:
        raise ValueError('Expected exactly one live nterchange environment')
    site = records[0]
    root = Path(site['path'])
    state = root.parent / '.nf-releases' / (site['name'] + '-' + site['env'])
    if not state.is_dir() or state.resolve() != state:
        raise ValueError('Missing or symlinked release state')
    if (state / 'pending.json').exists() or (state / 'pending.json').is_symlink():
        raise ValueError('Release recovery is pending')
    return job, site, state, path


def run_job(name, check=False):
    job, site, state, path = checked_job(name)
    if check:
        print(f"Checked {name}: {job['site']} {path}; no request or job lock created")
        return
    lock_path = LOCKS / (job['site'] + '.lock')
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'r+') as job_lock:
        # Daily reload and ten-minute load must not overlap for one site.
        fcntl.flock(job_lock, fcntl.LOCK_EX)
        with os.fdopen(os.open(state / 'operation.lock', os.O_RDONLY | os.O_NOFOLLOW), 'r') as release_lock:
            fcntl.flock(release_lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
            if (state / 'pending.json').exists() or (state / 'pending.json').is_symlink():
                raise ValueError('Release recovery is pending')
            base = ['docker', 'compose', '-f', site['compose_file']]
            container = subprocess.check_output(base + ['ps', '-q', site['service']], text=True).strip()
            if not re.fullmatch(r'[a-f0-9]{64}', container):
                raise ValueError('Expected one running app container')
            # Stay inside this app, even if its public nginx route is proxied back.
            php = (
                "$ctx=stream_context_create(['http'=>['timeout'=>600,'ignore_errors'=>true,"
                "'follow_location'=>0,'header'=>'Host: '." + json.dumps(job['public_host']) +
                ".\"\r\nX-Forwarded-Proto: https\r\nConnection: close\r\n\"]]);"
                "$body=file_get_contents(" + json.dumps('http://127.0.0.1' + path) + ",false,$ctx);"
                "if($body===false||!preg_match('/^HTTP\\/\\S+ 200(?: |$)/',$http_response_header[0]??''))"
                "{fwrite(STDERR,'Provider refresh did not return HTTP 200'.PHP_EOL);exit(1);}"
                "echo 'Provider refresh HTTP 200'.PHP_EOL;"
            )
            subprocess.run(['docker', 'exec', '--user', 'www-data', container, 'php',
                            '-d', 'session.gc_probability=0', '-r', php], check=True, timeout=620)
    print(f'Completed {name}')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('job')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--check', action='store_true', help='validate configuration without running a refresh')
    mode.add_argument('--execute', action='store_true', help='run the configured write-capable refresh')
    args = parser.parse_args()
    run_job(args.job, args.check)
