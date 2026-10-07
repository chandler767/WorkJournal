#!/usr/bin/env bash
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
app="$here/build/WorkJournalRecorder.app"
# A stable local identity keeps macOS permission grants across rebuilds; ad-hoc
# signatures change with every build and reset them.
identity="${WORKJOURNAL_SIGN_IDENTITY:-WorkJournal Local Signing}"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
swiftc -O -swift-version 5 -o "$app/Contents/MacOS/recorder" "$here"/Sources/*.swift
cp "$here/Info.plist" "$app/Contents/Info.plist"
if security find-identity -p codesigning 2>/dev/null | grep -qF "\"$identity\""; then
  codesign --force --sign "$identity" "$app"
  echo "signed with \"$identity\""
else
  codesign --force --sign - "$app"
  echo "ad-hoc signed (create a \"$identity\" code-signing certificate to keep permissions across rebuilds)"
fi
echo "built $app"
