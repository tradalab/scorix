#!/bin/sh
# Fills the bind-mounted SDK package by package: one multi-GB sdkmanager download inside
# `docker build` cannot resume. Safe to re-run; the NDK zip download resumes.
set -eu
SDK=/opt/android-sdk
NDK_ZIP=$SDK/.dl/android-ndk-$NDK_REL-linux.zip

install() {
  [ -e "$SDK/$2" ] && return 0
  n=1
  while [ "$n" -le 3 ]; do
    if timeout 1800 sdkmanager --sdk_root="$SDK" --install "$1" > /dev/null; then return 0; fi
    echo "retry $1 ($n)"
    n=$((n + 1))
  done
  return 1
}

# Tools resolve the SDK root from their own location, so they must sit inside it.
mkdir -p "$SDK/cmdline-tools"
[ -e "$SDK/cmdline-tools/latest" ] || ln -s /opt/cmdline-tools "$SDK/cmdline-tools/latest"
PATH=$SDK/cmdline-tools/latest/bin:$PATH

# Gradle fetches any other build-tools or platform it wants, which needs the licenses accepted here.
yes | sdkmanager --sdk_root="$SDK" --licenses > /dev/null
install "platforms;$PLATFORM" "platforms/$PLATFORM"
install "build-tools;35.0.0" build-tools/35.0.0

if [ ! -e "$SDK/ndk/$NDK_VER/source.properties" ]; then
  mkdir -p "$SDK/.dl" "$SDK/ndk"
  curl -fL --retry 10 --retry-all-errors -C - -o "$NDK_ZIP" "https://dl.google.com/android/repository/android-ndk-$NDK_REL-linux.zip"
  rm -rf "$SDK/.dl/unz"
  unzip -q "$NDK_ZIP" -d "$SDK/.dl/unz"
  mv "$SDK/.dl/unz/android-ndk-$NDK_REL" "$SDK/ndk/$NDK_VER"
  rm -rf "$SDK/.dl"
fi
echo sdk ready
