//go:build windows

package notification

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestToastXMLCarriesProtocolActivation(t *testing.T) {
	doc, err := toastXML(NotifyRequest{
		Title: "Build done", Text: "All green", ID: "b7",
		Actions: []Action{{Key: "open", Label: "Open"}, {Key: "log", Label: "View log"}},
	}, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`activationType="protocol"`,
		`launch="myapp://notify?id=b7"`,
		`arguments="myapp://notify?action=open&amp;id=b7"`,
		`content="View log"`,
		"<text>Build done</text>",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("toast missing %q:\n%s", want, doc)
		}
	}
}

// Without an id there is nothing to report back, so the toast must not claim to
// be activatable - a launch attribute pointing nowhere opens a dead URL.
func TestToastXMLWithoutIDHasNoLaunch(t *testing.T) {
	doc, err := toastXML(NotifyRequest{Title: "t", Text: "x",
		Actions: []Action{{Key: "open", Label: "Open"}}}, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "launch=") || strings.Contains(doc, "<actions>") {
		t.Fatalf("idless toast still advertises activation:\n%s", doc)
	}
}

func TestToastXMLWithoutSchemeHasNoLaunch(t *testing.T) {
	doc, err := toastXML(NotifyRequest{Title: "t", Text: "x", ID: "b7"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "launch=") {
		t.Fatalf("no declared scheme, yet the toast promises a callback:\n%s", doc)
	}
}

// The XML is full of quotes and ampersands; -EncodedCommand is what keeps them
// away from every shell layer between here and powershell.exe.
func TestEncodeCommandIsUTF16LEBase64(t *testing.T) {
	got := encodeCommand("héllo")
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("not UTF-16: %d bytes", len(raw))
	}
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		u = append(u, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	if string(utf16.Decode(u)) != "héllo" {
		t.Fatalf("round trip = %q", string(utf16.Decode(u)))
	}
}

func TestPSQuoteEscapesSingleQuotes(t *testing.T) {
	if got := psQuote("it's"); got != "'it''s'" {
		t.Fatalf("psQuote = %q", got)
	}
}
