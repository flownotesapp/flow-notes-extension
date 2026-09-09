#!/bin/sh
# Builds a distributable zip: prebuilt macOS binaries plus the installer,
# so a user needs no Go toolchain.
#
#   ./make-release.sh          -> dist/flow-notes-local-<version>-macos.zip
#
# The binaries are UNSIGNED. macOS will warn that the developer cannot be
# verified; install-agent.sh clears the quarantine flag, and SETUP.txt tells
# the user what to expect. Signing and notarizing (Apple Developer Program,
# $99/yr) is what removes the warning.

set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
VERSION="$(sed -n 's/^const Version = "\(.*\)"$/\1/p' "$HERE/main.go")"
[ -n "$VERSION" ] || { echo "Could not read Version from main.go" >&2; exit 1; }

STAGE="$HERE/dist/flow-notes-local"
ZIP="$HERE/dist/flow-notes-local-$VERSION-macos.zip"

rm -rf "$HERE/dist"
mkdir -p "$STAGE/bin"

for arch in arm64 amd64; do
    echo "Building darwin/$arch..."
    GOOS=darwin GOARCH="$arch" go build -trimpath \
        -o "$STAGE/bin/flow-notes-local-darwin-$arch" "$HERE"
done

cp "$HERE/install.sh" "$STAGE/"
cp "$HERE/README.md" "$STAGE/"
chmod +x "$STAGE/install.sh"

cat > "$STAGE/SETUP.txt" <<TXT
Flow Notes -- local helper $VERSION
==================================

This small program lets the Flow Notes extension save notes into Apple
Notes or into Markdown files on this Mac. Notes stored in Google Drive do
not need it.

TO INSTALL
----------
1. Open Terminal.
2. Drag this folder into the Terminal window after typing:  cd
   then press Return.
3. Paste this, replacing the id with the one the extension showed you:

   ./install.sh chrome-extension://YOUR-EXTENSION-ID

4. Restart Chrome.

That is it. There is nothing to keep running: Chrome starts the helper
by itself whenever the extension needs it, and closes it afterwards.

TWO THINGS TO EXPECT
--------------------
* macOS may say the program is from an unidentified developer. That is
  because this build is not signed with a paid Apple certificate -- not
  because anything is wrong with it. The installer clears the flag for
  you. If macOS still blocks it, right-click the file, choose Open, and
  confirm once.

* The first time you save a highlight to Apple Notes, macOS asks for
  permission to control Notes. The dialog names Google Chrome, because
  Chrome is what launches this helper. Approve it. You can change your
  mind later in System Settings > Privacy & Security > Automation.

TO REMOVE
---------
   ./install.sh --uninstall

That removes the helper and unregisters it from your browsers. Your notes
are left exactly where they are.

WHERE THINGS GO
---------------
   Program:  ~/.local/bin/flow-notes-local
   Notes:    ~/FlowNotes          (Markdown notes only)
TXT

( cd "$HERE/dist" && zip -qr "$(basename "$ZIP")" flow-notes-local )
rm -rf "$STAGE"

echo
echo "Built $ZIP"
echo "Upload it to GitHub Releases and point HELPER_DOWNLOAD_URL in the"
echo "extension's background.js at the release asset."
