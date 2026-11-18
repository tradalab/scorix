#!/usr/bin/env bash
set -euo pipefail
app=$1
udid=$(xcrun simctl list devices available -j | python3 -c '
import sys, json
for runtime, devs in json.load(sys.stdin)["devices"].items():
    if "iOS" in runtime:
        for d in devs:
            if d["name"].startswith("iPhone"):
                print(d["udid"]); raise SystemExit
')
xcrun simctl boot "$udid" || true
xcrun simctl bootstatus "$udid" -b
xcrun simctl install "$udid" "$app"
for run in 1 2; do
  xcrun simctl terminate "$udid" dev.scorix.spike 2>/dev/null || true
  data=$(xcrun simctl get_app_container "$udid" dev.scorix.spike data)
  rm -f "$data/Documents/spike.json"
  xcrun simctl launch "$udid" dev.scorix.spike
  for _ in $(seq 1 60); do [ -f "$data/Documents/spike.json" ] && break; sleep 1; done
  if [ ! -f "$data/Documents/spike.json" ]; then
    echo "::error title=ios spike::run $run produced no result in 60s"
    xcrun simctl spawn "$udid" log show --last 2m --predicate 'process == "Spike"' 2>/dev/null | tail -40
    exit 1
  fi
  result=$(cat "$data/Documents/spike.json")
  echo "run $run: $result"
  echo "::notice title=ios spike run $run::$result"
done
grep -q '"error"' "$data/Documents/spike.json" && exit 1 || true
