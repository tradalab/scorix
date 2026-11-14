package e2e

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func silentWAV(samples int) []byte {
	const rate, bits = 8000, 16
	data := samples * bits / 8
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	b.Write(le.AppendUint32(nil, uint32(36+data)))
	b.WriteString("WAVEfmt ")
	b.Write(le.AppendUint32(nil, 16))
	b.Write(le.AppendUint16(nil, 1))
	b.Write(le.AppendUint16(nil, 1))
	b.Write(le.AppendUint32(nil, rate))
	b.Write(le.AppendUint32(nil, rate*bits/8))
	b.Write(le.AppendUint16(nil, bits/8))
	b.Write(le.AppendUint16(nil, bits))
	b.WriteString("data")
	b.Write(le.AppendUint32(nil, uint32(data)))
	b.Write(make([]byte, data))
	return b.Bytes()
}

func TestASchemeResponseCarriesItsContentTypeHeader(t *testing.T) {
	harness(t)
	got := askString(t, `fetch("scorix://app/hello.txt").then(function(r){return r.headers.get("content-type") || ""})`)
	if !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("fetch saw content-type %q: the driver dropped the handler's headers", got)
	}
}

func TestASchemeResponseKeepsTheHandlersStatus(t *testing.T) {
	harness(t)
	if got := askInt(t, `fetch("scorix://app/missing.txt").then(function(r){return r.status})`); got != 404 {
		t.Fatalf("a missing asset answered %d, want 404", got)
	}
}

func TestARangedRequestGetsItsSliceAsPartialContent(t *testing.T) {
	harness(t)
	got := askString(t, `fetch("scorix://app/tone.wav",{headers:{Range:"bytes=8-11"}}).then(function(r){return r.text().then(function(b){return r.status+" "+r.headers.get("content-range")+" "+b})})`)
	if got != "206 bytes 8-11/8044 WAVE" {
		t.Fatalf("a ranged fetch over the scheme answered %q", got)
	}
}
