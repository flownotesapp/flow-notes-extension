#!/bin/sh
# Builds the zip to upload to the Chrome Web Store.
#
#   ./package.sh
#
# Two things this handles that a plain `zip` does not:
#
#   * It ships ONLY what the extension needs to run. README.md and
#     check.mjs are development files -- including them puts unused code in
#     the package, which reviewers flag.
#
#   * It strips the manifest's "key" field. That field pins the extension
#     ID when loading unpacked, which is what keeps the ID stable for the
#     OAuth client -- but the Web Store rejects any package containing it.
#     So it stays in the source manifest and is removed only from the
#     package.
#
# NOTE: keep this file ASCII-only -- under a UTF-8 locale /bin/sh absorbs a
# multi-byte character following a variable reference into the variable
# NAME, which with `set -u` aborts the script.

set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
VERSION="$(python3 -c "import json;print(json.load(open('$HERE/manifest.json'))['version'])")"
OUT="$HERE/dist/flow-notes-extension-$VERSION.zip"
STAGE="$HERE/dist/stage"

FILES="background.js content.js popup.html popup.js icons"

# Refuse to build if the checks fail: finding out after upload is worse.
node "$HERE/check.mjs"
for f in "$HERE"/*.js; do node --check "$f"; done

rm -rf "$HERE/dist"
mkdir -p "$STAGE"

cd "$HERE"
for f in $FILES; do
    [ -e "$f" ] || { echo "missing: $f" >&2; exit 1; }
    cp -R "$f" "$STAGE/"
done

# Fields the Web Store rejects in an uploaded package.
python3 - "$HERE/manifest.json" "$STAGE/manifest.json" <<'PY'
import json, sys

src, dst = sys.argv[1], sys.argv[2]
manifest = json.load(open(src))

# "key" pins the extension id for unpacked loading; the Web Store assigns
# the id itself and refuses a package that declares one.
removed = [f for f in ("key",) if manifest.pop(f, None) is not None]

with open(dst, "w") as out:
    json.dump(manifest, out, indent=2)
    out.write("\n")

print("  stripped from the packaged manifest:", ", ".join(removed) or "nothing")
PY

cd "$STAGE"
zip -qr "$OUT" . -x '*.DS_Store' -x '__MACOSX/*'
cd "$HERE"
rm -rf "$STAGE"

echo "Built $OUT"
echo
echo "Contents:"
unzip -Z1 "$OUT" | sed 's/^/  /'
echo
echo "Size: $(($(wc -c < "$OUT") / 1024)) KB"

if unzip -Z1 "$OUT" | grep -Eiq 'README|check\.mjs|\.env|\.DS_Store|\.zip'; then
    echo "WARNING: the package contains a file that should not ship" >&2
    exit 1
fi
if unzip -p "$OUT" manifest.json | grep -q '"key"'; then
    echo "WARNING: the packaged manifest still contains \"key\"" >&2
    exit 1
fi
echo "No development files, and no \"key\" in the packaged manifest."
