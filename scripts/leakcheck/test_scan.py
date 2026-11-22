"""Exercise the exact workflow scan command without printing fixture contents."""
import os
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
COMMAND = ['bash', 'scripts/leakcheck/scan.sh']
REAL_GIT = shutil.which('git')


class ScanTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.env = dict(os.environ, TMPDIR=str(self.repo))
        for path in ['scripts/leakcheck/scan.sh', 'scripts/leakcheck/scan.py', '.github/gitleaks.toml']:
            dest = self.repo / path
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, dest)
        self.git('init', '-q')
        self.git('config', 'user.name', 'Scanner fixture')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.commit('harmless example\n')

    def git(self, *args):
        subprocess.run([REAL_GIT, *args], cwd=self.repo, env=self.env,
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def commit(self, content):
        (self.repo / 'fixture.txt').write_text(content)
        self.git('add', '.')
        self.git('commit', '-qm', 'scanner fixture')

    def scan(self, fail=False):
        result = subprocess.run(COMMAND, cwd=self.repo, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 1 if fail else 0)
        self.assertEqual(result.stdout, 'Secret scan failed; inspect locally with the pinned scanner.\n' if fail else '')
        self.assertEqual(result.stderr, '')
        # No temporary history stream survives either success or failure.
        self.assertEqual(list(self.repo.glob('scan-history-*')), [])

    def test_clean_history(self):
        self.scan()

    def test_removed_credential_and_inline_allow(self):
        # Construct a synthetic default-rule match; never log or publish it.
        self.commit('token = "' + 'ghp_' + hashlib.sha256(b'synthetic scanner fixture').hexdigest()[:36] + '" # gitleaks:allow\n')
        self.commit('harmless replacement\n')
        self.scan(fail=True)

    def test_git_history_read_error(self):
        shim = self.repo / 'bin'
        shim.mkdir()
        git = shim / 'git'
        git.write_text('#!/bin/sh\nif [ "$1" = log ] || [ "$3" = log ]; then\n'
                       '  if [ "$PARTIAL_HISTORY" = yes ]; then "$REAL_GIT" "$@"; fi\n'
                       '  echo "synthetic history-read error" >&2\n'
                       '  exit 128\nfi\nexec "$REAL_GIT" "$@"\n')
        git.chmod(0o755)
        self.env.update(PATH=str(shim) + os.pathsep + self.env['PATH'], REAL_GIT=REAL_GIT)
        for partial in ['no', 'yes']:
            self.env['PARTIAL_HISTORY'] = partial
            self.scan(fail=True)

    def test_scanner_success_without_history_read(self):
        shim = self.repo / 'bin'
        shim.mkdir()
        scanner = shim / 'gitleaks'
        scanner.write_text('#!/bin/sh\nexit 0\n')
        scanner.chmod(0o755)
        self.env['PATH'] = str(shim) + os.pathsep + self.env['PATH']
        self.scan(fail=True)

    def test_invalid_config(self):
        (self.repo / '.github/gitleaks.toml').write_text('invalid = [')
        self.scan(fail=True)


if __name__ == '__main__':
    unittest.main()
