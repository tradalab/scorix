#!/usr/bin/env bash
set -u
log=$1
label=$2
[ -f "$log" ] || exit 0
awk '
  /^ *--- FAIL: / { if (name) print "::error title=" name "::" msg; name = $3; msg = ""; next }
  /^ *--- (PASS|SKIP): / { if (name) print "::error title=" name "::" msg; name = ""; next }
  /^(FAIL|ok)[ \t]/ { if (name) print "::error title=" name "::" msg; name = "" }
  /^FAIL[ \t]/ && /\[build failed\]|\[setup failed\]/ { print "::error title=" $2 "::" $0 }
  /^panic: / { print "::error title=panic::" $0 }
  name && /^    / { sub(/^ +/, ""); if (length(msg) < 900) msg = msg (msg ? " | " : "") $0 }
  END { if (name) print "::error title=" name "::" msg }
' "$log" | head -10
failed=$(grep -cE -- '^ *--- FAIL: ' "$log" || true)
passed=$(grep -E -- '^ *--- PASS: ' "$log" | awk '{print $3}' | paste -sd ' ' -)
echo "::notice title=$label::failed: $failed; passed: ${passed:-see log}"
