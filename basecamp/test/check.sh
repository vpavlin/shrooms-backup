#!/usr/bin/env bash
# Load the view offscreen against a fake core; print what it understood and
# save a picture of each tab into $OUT (default: a temporary directory).
set -euo pipefail
cd "$(dirname "$0")"
QML=${QML:-$(command -v qml6 || command -v qml || ls /nix/store/*-qtdeclarative-6.9*/bin/qml 2>/dev/null | head -1)}
[ -x "$QML" ] || { echo "no qml runtime; set QML" >&2; exit 1; }
OUT=${OUT:-$(mktemp -d)}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp ../view/Main.qml Harness.qml "$work/"
QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt-6/qml
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/qml
# Without QT_ASSUME_STDERR_HAS_CONSOLE the runtime says nothing at all, not
# even why it loaded nothing.
QT_QPA_PLATFORM=offscreen QT_ASSUME_STDERR_HAS_CONSOLE=1 QML_IMPORT_PATH="$QMLDIR" \
    timeout 60 "$QML" -I "$work" "$work/Harness.qml" -- "$OUT" 2>&1 | grep -v '^qt\.\|propagateSizeHints' || true
echo "pictures: $OUT"
