//go:build ios

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"strings"
	"unsafe"
)

const page = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>spike</title></head>
<body><p id="s">running</p>
<script>
(async function () {
  const go = (m) => window.webkit.messageHandlers.go.postMessage(m);
  await go({kind: "ready", at: performance.now()});
  const n = 1000, times = [];
  for (let i = 0; i < n; i++) {
    const t = performance.now();
    const r = await go({kind: "echo", i: i});
    if (!r || r.echo !== i) { await go({kind: "error", detail: "bad echo " + JSON.stringify(r)}); return; }
    times.push(performance.now() - t);
  }
  times.sort((a, b) => a - b);
  const big = "x".repeat(1 << 20);
  const tb = performance.now();
  const rb = await go({kind: "size", data: big});
  const bigMs = performance.now() - tb;
  const ta = performance.now();
  const asset = await fetch("scorix://app/hello.txt").then((r) => r.text());
  const assetMs = performance.now() - ta;
  await go({kind: "done",
    avgUs: Math.round(times.reduce((a, b) => a + b, 0) / n * 1000),
    p50Us: Math.round(times[n >> 1] * 1000),
    p95Us: Math.round(times[Math.floor(n * 0.95)] * 1000),
    oneMbMs: Math.round(bigMs * 10) / 10, oneMbBack: rb && rb.size,
    assetMs: Math.round(assetMs * 10) / 10, asset: asset,
    secureContext: window.isSecureContext, subtle: !!(window.crypto && window.crypto.subtle),
    clipboard: !!navigator.clipboard, origin: location.origin});
  document.getElementById("s").textContent = "done";
})();
</script></body></html>`

//export ScorixCall
func ScorixCall(in *C.char) *C.char {
	var msg map[string]any
	if err := json.Unmarshal([]byte(C.GoString(in)), &msg); err != nil {
		return C.CString(`{"error":"bad json"}`)
	}
	var out any
	switch msg["kind"] {
	case "echo":
		out = map[string]any{"echo": msg["i"]}
	case "size":
		s, _ := msg["data"].(string)
		out = map[string]any{"size": len(s)}
	default:
		out = map[string]any{"ok": true}
	}
	b, _ := json.Marshal(out)
	return C.CString(string(b))
}

//export ScorixAsset
func ScorixAsset(path *C.char, size *C.int) unsafe.Pointer {
	var body string
	switch strings.TrimPrefix(C.GoString(path), "/") {
	case "", "index.html":
		body = page
	case "hello.txt":
		body = "served-by-go"
	default:
		*size = -1
		return nil
	}
	*size = C.int(len(body))
	return C.CBytes([]byte(body))
}

//export ScorixFree
func ScorixFree(p unsafe.Pointer) { C.free(p) }

func main() {}
