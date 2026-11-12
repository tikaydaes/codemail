#!/bin/sh
# Builds the Safari version: Apple's converter wraps the extension in the
# macOS app Safari requires, and Xcode builds it. Needs a Mac with Xcode.
#
# The result is signed only for local use. To load it, enable "Allow
# unsigned extensions" in Safari's Develop menu (Safari turns that off
# again each time it quits). A build others can simply install needs
# Developer ID signing and notarisation, which need a paid Apple account.
set -e
cd "$(dirname "$0")"
node build.mjs >/dev/null
ID=io.github.tikaydaes.codemail.safari
rm -rf dist/safari-xcode
xcrun safari-web-extension-converter dist/safari --project-location dist/safari-xcode \
  --app-name Codemail --bundle-identifier "$ID" --macos-only --no-open --no-prompt --force >/dev/null
# The extension's identifier must start with the app's. Some versions of
# the converter name the app differently, so set it explicitly.
for f in dist/safari-xcode/Codemail/Codemail.xcodeproj/project.*; do
  [ -f "$f" ] && sed -i.bak -E "s/io\.github\.tikaydaes\.codemail\.Codemail/$ID/g" "$f" && rm -f "$f.bak"
done
xcodebuild -project dist/safari-xcode/Codemail/Codemail.xcodeproj -scheme Codemail -configuration Release \
  -derivedDataPath dist/safari-build CODE_SIGN_IDENTITY=- DEVELOPMENT_TEAM= build 2>&1 |
  grep -E "error:|BUILD (SUCCEEDED|FAILED)" || true
APP=dist/safari-build/Build/Products/Release/Codemail.app
test -d "$APP/Contents/PlugIns/Codemail Extension.appex" || { echo "Safari build failed"; exit 1; }
VERSION=$(node -p "require('./manifest.json').version")
ditto -c -k --keepParent "$APP" "dist/codemail-safari-$VERSION-unsigned.zip"
rm -f "dist/codemail-safari-$VERSION.zip"
echo "built $APP and dist/codemail-safari-$VERSION-unsigned.zip"
