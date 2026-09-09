#!/bin/sh
# Installs the Flow Notes local helper as a Chrome native messaging host.
#
#   ./install.sh chrome-extension://<your-extension-id>
#   ./install.sh --uninstall
#
# Chrome launches the helper on demand, so there is nothing to start and
# nothing left running. Re-run any time to update.
#
# NOTE: keep this file ASCII-only. Under a UTF-8 locale /bin/sh absorbs a
# multi-byte character that immediately follows a variable reference into
# the variable NAME, and with `set -u` that aborts the script. `sh -n` does
# not catch it -- it is an expansion-time fault, not a syntax error.

set -eu

HOST_NAME="com.flownotes.local"
BIN_DIR="$HOME/.local/bin"
BIN="$BIN_DIR/flow-notes-local"
SUPPORT="$HOME/Library/Application Support"

# Every Chromium-family browser that might be installed. The manifest is
# written to each one that is actually present.
BROWSER_DIRS="
$SUPPORT/Google/Chrome
$SUPPORT/Google/Chrome Beta
$SUPPORT/Google/Chrome Canary
$SUPPORT/Chromium
$SUPPORT/BraveSoftware/Brave-Browser
$SUPPORT/Microsoft Edge
$SUPPORT/Arc/User Data
"

# Earlier versions ran as a launchd agent with an HTTP server. Native
# messaging replaces it entirely, so remove it rather than leaving a second
# copy holding a port.
remove_legacy_agent() {
    legacy_plist="$HOME/Library/LaunchAgents/com.flownotes.local.plist"
    if [ -f "$legacy_plist" ]; then
        echo "Removing the old background agent (no longer needed)..."
        launchctl bootout "gui/$(id -u)/com.flownotes.local" 2>/dev/null || true
        rm -f "$legacy_plist"
    fi
}

if [ "${1:-}" = "--uninstall" ]; then
    remove_legacy_agent
    echo "$BROWSER_DIRS" | while IFS= read -r dir; do
        [ -n "$dir" ] || continue
        rm -f "$dir/NativeMessagingHosts/$HOST_NAME.json"
    done
    rm -f "$BIN"
    echo "Removed the helper and its browser registration."
    echo "Your notes were left alone."
    exit 0
fi

ORIGIN="${1:-}"
case "$ORIGIN" in
    chrome-extension://*) ;;
    *)
        echo "usage: $0 chrome-extension://<your-extension-id>" >&2
        echo "       $0 --uninstall" >&2
        echo >&2
        echo "The extension shows you this exact command. Without the id," >&2
        echo "any installed extension could drive this helper." >&2
        exit 1
        ;;
esac

# Chrome requires a trailing slash on each allowed origin.
case "$ORIGIN" in
    */) ALLOWED="$ORIGIN" ;;
    *)  ALLOWED="$ORIGIN/" ;;
esac

HERE="$(cd "$(dirname "$0")" && pwd)"
ARCH="$(uname -m)"
PREBUILT="$HERE/bin/flow-notes-local-darwin-$ARCH"

mkdir -p "$BIN_DIR"

# A fixed install path matters: macOS ties the Notes automation permission
# to the binary's location, so putting it somewhere new means granting it
# again.
if [ -f "$PREBUILT" ]; then
    echo "Installing the bundled $ARCH binary..."
    cp "$PREBUILT" "$BIN"
    chmod +x "$BIN"
    # Unsigned downloads are quarantined by macOS and refused. A signed and
    # notarized build would not need this.
    xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true
elif command -v go >/dev/null 2>&1; then
    echo "Building from source..."
    go build -o "$BIN" "$HERE"
else
    echo "No prebuilt binary for $ARCH and no Go toolchain found." >&2
    echo "Install Go from https://go.dev/dl/, or use a release that" >&2
    echo "includes bin/flow-notes-local-darwin-$ARCH." >&2
    exit 1
fi

remove_legacy_agent

installed=0
echo "$BROWSER_DIRS" | while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    [ -d "$dir" ] || continue

    mkdir -p "$dir/NativeMessagingHosts"
    manifest="$dir/NativeMessagingHosts/$HOST_NAME.json"
    cat > "$manifest" <<JSON
{
  "name": "$HOST_NAME",
  "description": "Flow Notes local helper",
  "path": "$BIN",
  "type": "stdio",
  "allowed_origins": ["$ALLOWED"]
}
JSON
    echo "  registered with $(basename "$dir")"
done

# The while loop above runs in a subshell, so count separately.
for dir in "$SUPPORT/Google/Chrome" "$SUPPORT/Chromium" "$SUPPORT/BraveSoftware/Brave-Browser" "$SUPPORT/Microsoft Edge"; do
    [ -f "$dir/NativeMessagingHosts/$HOST_NAME.json" ] && installed=$((installed + 1))
done

if [ "$installed" -eq 0 ]; then
    echo >&2
    echo "No Chromium-family browser profile was found to register with." >&2
    echo "Open Chrome once, then run this again." >&2
    exit 1
fi

echo
echo "Done. Chrome starts the helper by itself when the extension needs it,"
echo "so there is nothing to leave running."
echo
echo "Restart Chrome, then reload the extension."
echo
echo "The first time you save to Apple Notes, macOS asks for permission to"
echo "control Notes -- it will name Google Chrome, because Chrome is what"
echo "launches this helper. Approve it once."
