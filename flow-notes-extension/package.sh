#!/bin/sh
# Builds the zip to upload to the Chrome Web Store.
#
#   ./package.sh
#
# Ships ONLY what the extension needs to run. README.md and check.mjs are
# development files: shipping them means unused code in the package, which
# reviewers flag, and check.mjs would look like a build script that never
# runs. .DS_Store and stray archives must never be included.
#
# NOTE: keep this file ASCII-only -- under a UTF-8 locale /bin/sh absorbs a
# multi-byte character following a variable reference into the variable
# NAME, which with `set -u` aborts the script.

set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
VERSION="$(python3 -c "import json;print(json.load(open('$HERE/manifest.json'))['version'])")"
OUT="$HERE/dist/flow-notes-extension-$VERSION.zip"

# Everything that must be in the package, and nothing else.
FILES="manifest.json background.js content.js popup.html popup.js icons"

# Refuse to build if the checks fail: a package that fails them will fail
# review or fail at runtime, and finding that out after upload is worse.
node "$HERE/check.mjs"
for f in "$HERE"/*.js; do node --check "$f"; done

rm -rf "$HERE/dist"
mkdir -p "$HERE/dist"

cd "$HERE"
for f in $FILES; do
    [ -e "$f" ] || { echo "missing: $f" >&2; exit 1; }
done

# -x excludes macOS metadata that zip would otherwise embed.
zip -qr "$OUT" $FILES -x '*.DS_Store' -x '__MACOSX/*'

echo "Built $OUT"
echo
echo "Contents:"
unzip -Z1 "$OUT" | sed 's/^/  /'
echo
echo "Size: $(($(wc -c < "$OUT") / 1024)) KB"

# A last look for anything that should never have made it in.
if unzip -Z1 "$OUT" | grep -Eiq 'README|check\.mjs|\.env|\.DS_Store|\.zip'; then
    echo >&2
    echo "WARNING: the package contains a file that should not ship" >&2
    exit 1
fi
echo "No development or stray files included."
