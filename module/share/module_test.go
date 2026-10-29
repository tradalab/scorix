package share

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestShare(t *testing.T) {
	m := New()
	if _, err := m.Share(context.Background(), Request{}); fault.CodeOf(err) != "invalid_request" {
		t.Fatalf("empty request err = %v, want invalid_request", err)
	}
	if _, err := m.Share(context.Background(), Request{URL: "https://example.com"}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Share err = %v, want code unavailable", err)
	}
}

func TestShareFileValidation(t *testing.T) {
	m := New()
	png := []byte{0x89, 'P', 'N', 'G'}
	for _, r := range []Request{
		{Data: png, Mime: "image/png"},
		{Data: png, FileName: "chart.png"},
		{Data: png, Mime: "image/png", FileName: "../chart.png"},
		{Data: png, Mime: "image/png", FileName: ".."},
		{Data: make([]byte, maxFile+1), Mime: "image/png", FileName: "big.png"},
	} {
		if _, err := m.Share(context.Background(), r); fault.CodeOf(err) != "invalid_request" {
			t.Errorf("Share(%q, %q, %d bytes) err = %v, want invalid_request", r.FileName, r.Mime, len(r.Data), err)
		}
	}
	if _, err := m.Share(context.Background(), Request{Data: png, Mime: "image/png", FileName: "chart.png"}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("file Share err = %v, want code unavailable", err)
	}
}
