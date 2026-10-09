import io
import os
import subprocess
import sys
import time
import unittest
from unittest.mock import patch

from math_blitz.terminal import Terminal


class FakeConsole:
    def __init__(self, chars):
        self.chars = list(chars)

    def kbhit(self):
        return bool(self.chars)

    def getwch(self):
        return self.chars.pop(0)


class WindowsAdapterTests(unittest.TestCase):
    def terminal(self, chars):
        terminal = Terminal()
        terminal.timed = True
        terminal._windows = True
        terminal._console = FakeConsole(chars)
        return terminal

    def test_editing_and_extended_keys(self):
        terminal = self.terminal(['\xe0', 'K', '1', '2', '\b', '3', '\r'])
        with patch('sys.stdout', io.StringIO()):
            reply = terminal.read(time.monotonic() + 1)
        self.assertEqual(reply.text, '13')
        self.assertFalse(reply.timed_out)

    def test_timeout_and_pending_input_cleanup(self):
        terminal = self.terminal('123\r456\r')
        with patch('sys.stdout', io.StringIO()):
            reply = terminal.read(time.monotonic() - 1)
        self.assertTrue(reply.timed_out)
        self.assertEqual(terminal._console.chars, [])

    def test_ctrl_c_and_eof(self):
        with patch('sys.stdout', io.StringIO()):
            with self.assertRaises(KeyboardInterrupt):
                self.terminal('\x03').read(time.monotonic() + 1)
            self.assertIsNone(self.terminal('\x1a').read(time.monotonic() + 1).text)


@unittest.skipIf(os.name == 'nt', 'POSIX pseudo-terminal integration')
class PosixTerminalTests(unittest.TestCase):
    def exercise(self, script, input_bytes=None):
        import pty
        import select
        import termios
        master, slave = pty.openpty()
        before = termios.tcgetattr(slave)
        # Compare exactly the state the adapter receives, including control chars.
        script = ('import termios\n'
                  'saved = termios.tcgetattr(0)\n' + script +
                  '\nassert termios.tcgetattr(0) == saved, "terminal not restored"\n')
        child = subprocess.Popen([sys.executable, '-u', '-c', script],
                                 stdin=slave, stdout=slave, stderr=slave)
        output = b''
        sent = False
        end = time.monotonic() + 4
        try:
            while child.poll() is None and time.monotonic() < end:
                ready, _, _ = select.select([master], [], [], 0.05)
                if ready:
                    output += os.read(master, 4096)
                    if b'READY' in output and input_bytes is not None and not sent:
                        os.write(master, input_bytes)
                        sent = True
            self.assertIsNotNone(child.poll(), 'child failed to exit: ' + repr(output))
            while select.select([master], [], [], 0)[0]:
                output += os.read(master, 4096)
            self.assertEqual(child.returncode, 0, output.decode(errors='replace'))
            mask = termios.ICANON | termios.ECHO | termios.ISIG
            self.assertEqual(termios.tcgetattr(slave)[3] & mask, before[3] & mask)
            return output
        finally:
            if child.poll() is None:
                child.kill()
            child.wait()
            os.close(master)
            os.close(slave)

    def test_real_timeout_without_enter(self):
        output = self.exercise('''
import time
from math_blitz.terminal import Terminal
with Terminal() as terminal:
    print('READY', flush=True)
    started = time.monotonic()
    reply = terminal.read(started + 0.15)
    assert reply.timed_out
    assert 0.15 <= reply.at - started < 1
print('EXPIRED', flush=True)
''', b'12')
        self.assertIn(b'EXPIRED', output)

    def test_editing_and_multiline_paste_discard(self):
        output = self.exercise('''
import time
from math_blitz.terminal import Terminal
with Terminal() as terminal:
    print('READY', flush=True)
    reply = terminal.read(time.monotonic() + 1)
    assert reply.text == '13', repr(reply)
    terminal.clear_pending()
    assert terminal.read(time.monotonic() + 0.1).timed_out
print('CLEAN', flush=True)
''', b'12\x7f3\n999\n')
        self.assertIn(b'CLEAN', output)

    def test_restoration_on_exception(self):
        self.exercise('''
from math_blitz.terminal import Terminal
try:
    with Terminal():
        raise KeyboardInterrupt
except KeyboardInterrupt:
    print('RESTORED', flush=True)
''')


if __name__ == '__main__':
    unittest.main()
