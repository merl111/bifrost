"""Inject real X11 key events into the isolated native test window (Xvfb only)."""
import ctypes as C
import sys
import subprocess
x = C.CDLL('libX11.so.6')
t = C.CDLL('libXtst.so.6')
x.XOpenDisplay.restype = C.c_void_p
x.XDefaultRootWindow.argtypes = [C.c_void_p]
x.XDefaultRootWindow.restype = C.c_ulong
x.XQueryTree.argtypes = [C.c_void_p, C.c_ulong, C.POINTER(C.c_ulong), C.POINTER(C.c_ulong), C.POINTER(C.POINTER(C.c_ulong)), C.POINTER(C.c_uint)]
x.XFetchName.argtypes = [C.c_void_p, C.c_ulong, C.POINTER(C.c_char_p)]
x.XSetInputFocus.argtypes = [C.c_void_p, C.c_ulong, C.c_int, C.c_ulong]
x.XStringToKeysym.argtypes = [C.c_char_p]
x.XStringToKeysym.restype = C.c_ulong
x.XKeysymToKeycode.argtypes = [C.c_void_p, C.c_ulong]
x.XKeysymToKeycode.restype = C.c_ubyte
x.XFlush.argtypes = [C.c_void_p]
x.XCloseDisplay.argtypes = [C.c_void_p]
x.XFree.argtypes = [C.c_void_p]
t.XTestFakeKeyEvent.argtypes = [C.c_void_p, C.c_uint, C.c_int, C.c_ulong]
d = x.XOpenDisplay(None)
assert d
root, parent, children, count = C.c_ulong(), C.c_ulong(), C.POINTER(C.c_ulong)(), C.c_uint()
x.XQueryTree(d, x.XDefaultRootWindow(d), C.byref(root), C.byref(parent), C.byref(children), C.byref(count))
found = False
for i in range(count.value):
    name = C.c_char_p()
    native_name = subprocess.run(['xprop', '-id', hex(children[i]), '_NET_WM_NAME'], capture_output=True, text=True).stdout
    if 'Bifrost' in native_name:
        x.XSetInputFocus(d, children[i], 1, 0)
        found = True
    if x.XFetchName(d, children[i], C.byref(name)) and name.value:
        if name.value.startswith(b'Bifrost'):
            x.XSetInputFocus(d, children[i], 1, 0)
            found = True
        x.XFree(name)
x.XFree(children)
assert found, 'native test window missing'
for arg in sys.argv[1:]:
    modifiers = []
    while '+' in arg:
        mod, arg = arg.split('+', 1)
        modifiers.append(x.XKeysymToKeycode(d, x.XStringToKeysym((mod + '_L').encode())))
    code = x.XKeysymToKeycode(d, x.XStringToKeysym(arg.encode()))
    assert code, arg
    for mod in modifiers:
        t.XTestFakeKeyEvent(d, mod, 1, 0)
    t.XTestFakeKeyEvent(d, code, 1, 0)
    t.XTestFakeKeyEvent(d, code, 0, 0)
    for mod in reversed(modifiers):
        t.XTestFakeKeyEvent(d, mod, 0, 0)
x.XFlush(d)
x.XCloseDisplay(d)
