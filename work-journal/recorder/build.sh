#!/usr/bin/env bash
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
app="$here/build/WorkJournalRecorder.app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
swiftc -O -swift-version 5 -o "$app/Contents/MacOS/recorder" "$here"/Sources/*.swift
cp "$here/Info.plist" "$app/Contents/Info.plist"
codesign --force --sign - "$app"
echo "built $app"
