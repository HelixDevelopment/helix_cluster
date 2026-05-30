#!/usr/bin/env python3
"""Updates docs/continuation.md §1 and §2 in-place while preserving §11.4.44 format."""

import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[2]
CONTINUATION_FILE = PROJECT_ROOT / "docs" / "continuation.md"
TIMESTAMP = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def git_cmd(args: list[str]) -> str:
    result = subprocess.run(
        ["git", "-C", str(PROJECT_ROOT)] + args,
        capture_output=True, text=True
    )
    return result.stdout.strip()


def main():
    if not CONTINUATION_FILE.exists():
        print(f"[docs] ERROR: {CONTINUATION_FILE} not found")
        sys.exit(1)

    content = CONTINUATION_FILE.read_text()

    # Extract and increment revision
    rev_match = re.search(r'^\*\*Revision:\*\*\s*(\d+)', content, re.MULTILINE)
    rev = int(rev_match.group(1)) + 1 if rev_match else 1

    # Gather git state
    branch = git_cmd(["rev-parse", "--abbrev-ref", "HEAD"]) or "unknown"
    commit = git_cmd(["rev-parse", "--short", "HEAD"]) or "unknown"
    log = git_cmd(["log", "--oneline", "-10"])

    # Update revision header
    content = re.sub(
        r'^\*\*Revision:\*\*\s*\d+$',
        f'**Revision:** {rev}',
        content,
        flags=re.MULTILINE
    )
    content = re.sub(
        r'^\*\*Last modified:\*\*\s*.*$',
        f'**Last modified:** {TIMESTAMP}',
        content,
        flags=re.MULTILINE
    )

    # Build new §1
    rows = []
    for line in log.split('\n'):
        line = line.strip()
        if not line:
            continue
        parts = line.split(' ', 1)
        h = parts[0]
        m = parts[1] if len(parts) > 1 else ''
        rows.append(f'| `{h}` | {m} |')

    new_s1 = (
        '## §1: Recently Completed Work (last 10 commits)\n\n'
        '| Commit | Message |\n'
        '|--------|---------|\n'
        + '\n'.join(rows)
    )

    # Build new §2
    new_s2 = (
        f'## §2: Environment Snapshot\n\n'
        f'| Property | Value |\n'
        f'|----------|-------|\n'
        f'| **Branch** | `{branch}` |\n'
        f'| **Commit** | `{commit}` |\n'
        f'| **Timestamp** | {TIMESTAMP} |'
    )

    # Replace §1
    content = re.sub(
        r'## §1:.*?\n(?=## §2:)',
        new_s1 + '\n\n',
        content,
        flags=re.DOTALL
    )

    # Replace §2
    content = re.sub(
        r'## §2:.*?\n(?=## §3:)',
        new_s2 + '\n\n',
        content,
        flags=re.DOTALL
    )

    CONTINUATION_FILE.write_text(content)
    print(f'[docs] continuation.md updated — revision {rev}, {TIMESTAMP}')


if __name__ == '__main__':
    main()
