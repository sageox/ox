"""Console-script entry point: exec the native ox binary, forwarding argv/stdio."""

from __future__ import annotations

import os
import subprocess
import sys

from ._binary import ensure


def main() -> int:
    try:
        binary = ensure()
    except Exception as err:  # surface a clear message, non-zero exit
        print(f"[sageox] {err}", file=sys.stderr)
        return 1

    # Replace this process with ox where possible so signals/exit codes pass
    # through cleanly; fall back to subprocess on platforms without execv.
    argv = [str(binary), *sys.argv[1:]]
    if hasattr(os, "execv"):
        os.execv(str(binary), argv)  # noqa: S606 (trusted, verified binary)
        return 0  # unreachable
    return subprocess.call(argv)


if __name__ == "__main__":
    raise SystemExit(main())
