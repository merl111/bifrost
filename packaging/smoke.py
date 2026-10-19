"""Exercise the delivered files using disposable state, without model requests."""
import ctypes as C
import fcntl
import os
import pathlib
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time

mode, binary = sys.argv[1:]
binary = str(pathlib.Path(binary).resolve())


def tui(base, env):
    master, slave = pty.openpty()
    output = bytearray()

    def resize(cols, rows):
        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))

    def drain(seconds):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if not select.select([master], [], [], .05)[0]:
                continue
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            output.extend(data)
            if b'\x1b[6n' in data:
                os.write(master, b'\x1b[1;1R')
            if b'\x1b[c' in data:
                os.write(master, b'\x1b[?1;2c')

    resize(120, 36)
    args = [binary, '--root', str(base / 'project'), '--state-dir', str(base / 'state'),
            'tui', '--model', 'fixture', '--base-url', 'http://127.0.0.1:1', '--agents', '0']
    process = subprocess.Popen(args, stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
    os.close(slave)
    try:
        drain(3)
        for cols, rows in [(80, 24), (160, 48), (60, 20), (120, 36)]:
            assert process.poll() is None, ('TUI exited early', bytes(output[-2000:]))
            resize(cols, rows)
            os.kill(process.pid, signal.SIGWINCH)
            drain(.4)
        os.write(master, b'\x10help\r')
        drain(.5)
        os.write(master, b'\x1b')
        drain(.2)
        os.write(master, b'\x03')
        drain(.5)
        if process.poll() is None:
            os.write(master, b'\x03')
            drain(.5)
        process.wait(timeout=10)
        plain = re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]', b'', output)
        assert b'Bifrost' in plain and len(output) > 2000, 'Missing TUI render'
        assert b'panic:' not in plain and process.returncode == 0, plain[-1000:]
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        os.close(master)


def wait_for_window(process):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline and process.poll() is None:
        tree = subprocess.check_output(['xwininfo', '-root', '-tree'], text=True)
        match = re.search(r'(0x[0-9a-f]+) "Bifrost[^\n]*', tree)
        if match:
            return int(match[1], 16)
        time.sleep(.2)
    raise AssertionError('Packaged GUI did not open a window')


def gui(base, env):
    x = C.CDLL('libX11.so.6')
    x.XOpenDisplay.restype = C.c_void_p
    x.XCloseDisplay.argtypes = [C.c_void_p]
    x.XInternAtom.argtypes = [C.c_void_p, C.c_char_p, C.c_int]
    x.XInternAtom.restype = C.c_ulong
    x.XFlush.argtypes = [C.c_void_p]
    x.XSendEvent.argtypes = [C.c_void_p, C.c_ulong, C.c_int, C.c_long, C.c_void_p]
    display = x.XOpenDisplay(None)
    assert display, 'No X11 display'

    class ClientMessage(C.Structure):
        _fields_ = [('type', C.c_int), ('serial', C.c_ulong), ('send_event', C.c_int),
                    ('display', C.c_void_p), ('window', C.c_ulong), ('message_type', C.c_ulong),
                    ('format', C.c_int), ('data', C.c_long * 5)]

    class Event(C.Union):
        _fields_ = [('client', ClientMessage), ('padding', C.c_long * 24)]

    try:
        # Verify startup chooser and direct workspace launch from the actual image.
        for chooser in (True, False):
            args = [binary, '--state-dir', str(base / ('chooser' if chooser else 'workspace')),
                    '--model', 'fixture', '--base-url', 'http://127.0.0.1:1', '--agents', '0']
            if not chooser:
                args += ['--root', str(base / 'project')]
            with tempfile.TemporaryFile() as log:
                process = subprocess.Popen(args, stdout=log, stderr=log, env=env)
                try:
                    window = wait_for_window(process)
                    time.sleep(2)
                    info = subprocess.check_output(['xwininfo', '-id', hex(window)], text=True)
                    assert 'IsViewable' in info and process.poll() is None, info
                    event = Event()
                    event.client.type = 33  # ClientMessage / WM_DELETE_WINDOW
                    event.client.display = display
                    event.client.window = window
                    event.client.message_type = x.XInternAtom(display, b'WM_PROTOCOLS', False)
                    event.client.format = 32
                    event.client.data[0] = x.XInternAtom(display, b'WM_DELETE_WINDOW', False)
                    assert x.XSendEvent(display, window, False, 0, C.byref(event))
                    x.XFlush(display)
                    process.wait(timeout=15)
                    log.seek(0)
                    text = log.read().decode(errors='replace')
                    assert process.returncode == 0, text
                    assert not any(s in text for s in ('panic:', 'SIGSEGV', 'Gtk-CRITICAL', 'GLib-GIO-ERROR')), text
                except Exception:
                    log.seek(0)
                    print(log.read().decode(errors='replace'), file=sys.stderr)
                    raise
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
    finally:
        x.XCloseDisplay(display)


with tempfile.TemporaryDirectory(prefix='bifrost-package-smoke-') as directory:
    base = pathlib.Path(directory)
    (base / 'project').mkdir()
    (base / 'project' / 'main.go').write_text('package main\nfunc main() {}\n')
    env = dict(os.environ, HOME=directory, XDG_CONFIG_HOME=str(base / 'config'),
               XDG_STATE_HOME=str(base / 'state-home'), XDG_CACHE_HOME=str(base / 'cache'),
               TERM='xterm-256color', SHELL='/bin/sh', APPIMAGE_EXTRACT_AND_RUN='1',
               GDK_BACKEND='x11', GSK_RENDERER='cairo')
    headless = dict(env)
    headless.pop('DISPLAY', None)
    version = subprocess.check_output([binary, '--version'], env=headless, text=True, timeout=30)
    assert re.fullmatch(r'bifrost(?:-desktop)? \d+\.\d+\.\d+\n', version), version
    subprocess.run([binary, '--help'], env=headless, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
    (tui if mode == 'tui' else gui)(base, env)
    print(f'PASS: packaged {mode}, version/help, real window or PTY, clean exit')
