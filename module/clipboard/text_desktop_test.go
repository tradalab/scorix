//go:build !android

package clipboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/nativeclip"
)

type fakeClip struct {
	mu   sync.Mutex
	text string
	err  error
}

func (f *fakeClip) ReadText() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text, f.err
}

func (f *fakeClip) WriteText(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.text = text
	return nil
}

type fakeTool struct {
	text  string
	err   error
	calls int
}

func stubTool(t *testing.T, err error) *fakeTool {
	t.Helper()
	ft := &fakeTool{err: err}
	oldRead, oldWrite := toolRead, toolWrite
	toolRead = func() (string, error) { ft.calls++; return ft.text, ft.err }
	toolWrite = func(s string) error {
		ft.calls++
		if ft.err != nil {
			return ft.err
		}
		ft.text = s
		return nil
	}
	t.Cleanup(func() { toolRead, toolWrite = oldRead, oldWrite })
	return ft
}

func installFake(t *testing.T) *fakeClip {
	t.Helper()
	f := &fakeClip{}
	nativeclip.Install(f)
	t.Cleanup(func() { nativeclip.Install(nil) })
	return f
}

func TestWriteAndReadGoThroughTheClipboardTheDriverInstalled(t *testing.T) {
	tool := stubTool(t, nil)
	f := installFake(t)
	m := New()
	if _, err := m.Write(context.Background(), WriteRequest{Text: "https://example.test/presigned"}); err != nil {
		t.Fatalf("Write with a native clipboard installed: %v", err)
	}
	if f.text != "https://example.test/presigned" {
		t.Fatalf("native clipboard holds %q", f.text)
	}
	got, err := m.Read(context.Background(), struct{}{})
	if err != nil || got != f.text {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if tool.calls != 0 {
		t.Fatalf("the external tool was called %d times while the native clipboard worked", tool.calls)
	}
}

func TestClearAfterEmptiesTheNativeClipboardOnlyIfNothingElseWasCopied(t *testing.T) {
	stubTool(t, nil)
	f := installFake(t)
	m := New()
	if _, err := m.Write(context.Background(), WriteRequest{Text: "secret", ClearAfterMs: 20}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got, _ := f.ReadText(); got == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the copied secret was never cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := m.Write(context.Background(), WriteRequest{Text: "secret", ClearAfterMs: 20}); err != nil {
		t.Fatal(err)
	}
	_ = f.WriteText("something else")
	time.Sleep(80 * time.Millisecond)
	if got, _ := f.ReadText(); got != "something else" {
		t.Fatalf("clipboard = %q: the clear wiped what the user copied afterwards", got)
	}
}

func TestANativeClipboardThatDoesNotAnswerFallsBackToTheExternalTool(t *testing.T) {
	tool := stubTool(t, nil)
	f := installFake(t)
	f.err = errors.New("the GTK clipboard did not answer")

	if _, err := New().Write(context.Background(), WriteRequest{Text: "copied"}); err != nil {
		t.Fatalf("Write = %v, want the tool to take over", err)
	}
	if tool.text != "copied" {
		t.Fatalf("tool holds %q, want copied", tool.text)
	}
	if got, err := New().Read(context.Background(), struct{}{}); err != nil || got != "copied" {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestWhenBothFailTheNativeErrorIsTheOneReported(t *testing.T) {
	stubTool(t, errors.New("No clipboard utilities available"))
	f := installFake(t)
	native := errors.New("the GTK clipboard did not answer")
	f.err = native

	if _, err := New().Write(context.Background(), WriteRequest{Text: "x"}); !errors.Is(err, native) {
		t.Fatalf("Write = %v, want the native error", err)
	}
	if _, err := New().Read(context.Background(), struct{}{}); !errors.Is(err, native) {
		t.Fatalf("Read = %v, want the native error", err)
	}
}

func TestWithoutANativeClipboardTheExternalToolIsUsed(t *testing.T) {
	tool := stubTool(t, nil)
	nativeclip.Install(nil)

	if _, err := New().Write(context.Background(), WriteRequest{Text: "headless"}); err != nil {
		t.Fatal(err)
	}
	if tool.text != "headless" {
		t.Fatalf("tool holds %q", tool.text)
	}
}
