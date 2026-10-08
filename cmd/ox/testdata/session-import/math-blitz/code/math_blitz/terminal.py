"""Standard-library console input; all platform-specific I/O stays here."""
import os
import select
import sys
import time
from dataclasses import dataclass
from typing import Optional

@dataclass(frozen=True)
class Reply:
    text: Optional[str]
    at: float
    timed_out: bool = False


class Terminal:
    def __init__(self):
        self.timed = False
        self._saved = None
        self._fd = None
        self._windows = os.name == "nt"

    def __enter__(self):
        if not (sys.stdin.isatty() and sys.stdout.isatty()):
            return self
        if self._windows:
            import ctypes
            import msvcrt
            from ctypes import wintypes
            # isatty() alone may also describe a compatibility-layer pseudo-TTY.
            # msvcrt reads the Windows console, so confirm this is a console handle.
            get_mode = ctypes.windll.kernel32.GetConsoleMode
            get_mode.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
            get_mode.restype = wintypes.BOOL
            mode = wintypes.DWORD()
            try:
                handle = msvcrt.get_osfhandle(sys.stdin.fileno())
                if not get_mode(handle, ctypes.byref(mode)):
                    return self
            except (OSError, ValueError):
                return self
            self._console = msvcrt
            self.timed = True
        else:
            import termios
            import tty
            self._termios = termios
            try:
                self._fd = sys.stdin.fileno()
                self._saved = termios.tcgetattr(self._fd)
                tty.setcbreak(self._fd)
                self.timed = True
            except (OSError, termios.error):
                self.__exit__(None, None, None)
            except BaseException:
                self.__exit__(None, None, None)
                raise
        return self

    def __exit__(self, *args):
        if self.timed and self._windows:
            self.clear_pending()
        if self._saved is not None:
            self._termios.tcsetattr(self._fd, self._termios.TCSANOW, self._saved)
            # Clear partial input and macOS PENDIN set by canonical restoration.
            self._termios.tcflush(self._fd, self._termios.TCIFLUSH)
            self._saved = None
        self.timed = False

    def clear_pending(self):
        if not self.timed:
            return
        if self._windows:
            while self._console.kbhit():
                self._console.getwch()
        else:
            self._termios.tcflush(self._fd, self._termios.TCIFLUSH)

    def read(self, deadline: float) -> Reply:
        if not self.timed:
            line = sys.stdin.readline()
            return Reply(line.rstrip("\r\n") if line else None, time.monotonic())
        chars = []
        special = False
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.clear_pending()
                print()
                return Reply(None, time.monotonic(), True)
            if self._windows:
                if not self._console.kbhit():
                    time.sleep(min(0.01, remaining))
                    continue
                char = self._console.getwch()
                if special:
                    special = False
                    continue
                if char in ("\x00", "\xe0"):
                    special = True
                    continue
            else:
                ready, _, _ = select.select([self._fd], [], [], remaining)
                if not ready:
                    continue
                data = os.read(self._fd, 1)
                if not data:
                    return Reply(None, time.monotonic())
                char = data.decode("ascii", errors="ignore")
            now = time.monotonic()
            if now >= deadline:
                self.clear_pending()
                print()
                return Reply(None, now, True)
            if char == "\x03":
                raise KeyboardInterrupt
            if char in ("\x04", "\x1a"):
                print()
                return Reply(None, now)
            if char in ("\r", "\n"):
                print()
                return Reply("".join(chars), now)
            if char in ("\x08", "\x7f"):
                if chars:
                    chars.pop()
                    print("\b \b", end="", flush=True)
            elif char and char.isprintable() and len(chars) < 128:
                chars.append(char)
                print(char, end="", flush=True)
