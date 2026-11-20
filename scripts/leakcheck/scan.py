"""Require independent Git exit evidence from the scanner's actual subprocess."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

# Preserve gitleaks' diff parsing and default path rules. Stdin scanning loses
# file context (including default exclusions) and is not equivalent to git mode.
GIT_WRAPPER = '''#!/usr/bin/env python3
import os
from pathlib import Path
import subprocess
import sys

args = sys.argv[1:]
status = Path(os.environ['SCAN_GIT_STATUS'])
result = subprocess.run([os.environ['SCAN_REAL_GIT'], *args])
command = args[2] if len(args) >= 3 and args[0] == '-C' else args[0]
# Remote-URL discovery is optional; failures reading history are fatal.
if command in ('log', 'cat-file') and result.returncode != 0:
    (status / 'failed').touch()
if command == 'log' and result.returncode == 0:
    (status / 'log-success').touch()
sys.exit(result.returncode if result.returncode >= 0 else 1)
'''


def main():
    real_git = shutil.which('git')
    if real_git is None:
        return 1
    with tempfile.TemporaryDirectory(prefix='scan-history-') as tmp:
        directory = Path(tmp)
        wrapper = directory / 'git'
        wrapper.write_text(GIT_WRAPPER)
        wrapper.chmod(0o700)
        env = dict(os.environ, SCAN_REAL_GIT=real_git, SCAN_GIT_STATUS=tmp,
                   PATH=tmp + os.pathsep + os.environ['PATH'])
        result = subprocess.run([
            'gitleaks', 'git', '--config', '.github/gitleaks.toml',
            '--log-opts=--full-history --no-ext-diff --no-textconv HEAD',
            '--gitleaks-ignore-path=/dev/null', '--ignore-gitleaks-allow',
            '--redact=100', '--no-banner',
        ], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return 0 if (result.returncode == 0
                     and (directory / 'log-success').is_file()
                     and not (directory / 'failed').exists()) else 1


if __name__ == '__main__':
    raise SystemExit(main())
