#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here/build}
target=arm64-apple-ios16.0-simulator
sdk=$(xcrun --sdk iphonesimulator --show-sdk-path)
clang=$(xcrun --sdk iphonesimulator -f clang)
rm -rf "$out" && mkdir -p "$out/Spike.app"
(cd "$here/go" && CGO_ENABLED=1 GOOS=ios GOARCH=arm64 CC="$clang" \
  CGO_CFLAGS="-isysroot $sdk -target $target" CGO_LDFLAGS="-isysroot $sdk -target $target" \
  go build -trimpath -ldflags="-s -w" -buildmode=c-archive -o "$out/libspike.a" .)
xcrun --sdk iphonesimulator swiftc -target "$target" -sdk "$sdk" -O \
  -import-objc-header "$out/libspike.h" "$here/app/main.swift" "$out/libspike.a" \
  -framework UIKit -framework WebKit -framework CoreFoundation -framework Security -lresolv \
  -o "$out/Spike.app/Spike"
cp "$here/app/Info.plist" "$out/Spike.app/Info.plist"
codesign --force --sign - "$out/Spike.app"
echo "::notice title=ios spike size::app $(du -sk "$out/Spike.app" | cut -f1) KiB, libspike.a $(du -sk "$out/libspike.a" | cut -f1) KiB"
