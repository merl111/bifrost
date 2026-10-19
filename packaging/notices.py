"""Collect redistribution notices without adding a runtime dependency."""
import json
import pathlib
import shutil
import subprocess
import sys

out = pathlib.Path(sys.argv[1])
out.mkdir(parents=True)
(out / 'README.txt').write_text('''Bifrost third-party notices

Bifrost source: https://github.com/merl111/bifrost
CUI source: https://github.com/merl111/cui/tree/1c05d5c43077af7524a171a127e60b3740c48775
CUI compatibility patches: patches/ in the Bifrost release source.
Go module versions and their upstream licenses are under go/.
Ubuntu package copyright notices are under ubuntu/. The package manifest records
binary/source package versions; source packages are available from
https://archive.ubuntu.com/ubuntu/pool/ and https://launchpad.net/ubuntu/+source/.
The manifest includes build-host packages as well as bundled runtime libraries.
AppImage runtime: https://github.com/AppImage/type2-runtime/tree/20251108 (MIT)
GTK deployment hook: https://github.com/linuxdeploy/linuxdeploy-plugin-gtk/tree/7a3fbc31a9e5075073ff8790f26effbac5f84453 (MIT)
These notices do not grant a license to Bifrost or CUI themselves.
''')
decoder = json.JSONDecoder()
for directory in ('.', 'desktop'):
    data = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=directory, text=True)
    while data.strip():
        module, end = decoder.raw_decode(data.lstrip())
        data = data.lstrip()[end:]
        if module.get('Main') or 'Replace' in module:
            continue
        dest = out / 'go' / (module['Path'] + '@' + module['Version'])
        dest.mkdir(parents=True, exist_ok=True)
        (dest / 'module.json').write_text(json.dumps(module, indent=2))
        if not module.get('Dir'):
            downloaded = subprocess.check_output(
                ['go', 'mod', 'download', '-json', module['Path'] + '@' + module['Version']],
                cwd=directory, text=True)
            module['Dir'] = json.loads(downloaded)['Dir']
        source = pathlib.Path(module['Dir'])
        for entry in source.iterdir():
            if entry.is_file() and entry.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE', 'AUTHORS')):
                shutil.copyfile(entry, dest / entry.name)
shutil.copyfile('/usr/local/go/LICENSE', out / 'Go-LICENSE')
for source in pathlib.Path('/usr/share/doc').glob('*/copyright'):
    dest = out / 'ubuntu' / source.parent.name
    dest.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, dest / 'copyright')
manifest = subprocess.check_output(['dpkg-query', '-W', '-f=${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n'])
(out / 'ubuntu-packages.tsv').write_bytes(manifest)
# Preserve the exact upstream license text for the two packaging components
# whose code is included in the delivered AppImage.
for name, url in [
    ('AppImage-runtime-LICENSE', 'https://raw.githubusercontent.com/AppImage/type2-runtime/20251108/LICENSE'),
    ('gtk-plugin-LICENSE', 'https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/7a3fbc31a9e5075073ff8790f26effbac5f84453/LICENSE.txt'),
]:
    subprocess.run(['curl', '-fsSL', '--retry', '3', '--max-time', '60', url, '-o', str(out / name)], check=True)
