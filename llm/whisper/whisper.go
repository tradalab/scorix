// Package whisper transcribes speech with whisper.cpp's command-line tool, one
// process per clip, and installs that tool from whisper.cpp's releases.
package whisper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/llm"
)

type Transcriber struct {
	id  string
	exe string
	// Passed as -t; zero leaves whisper-cli's own choice.
	Threads int
}

// exe is whisper-cli; Installer.Ensure returns one.
func New(id, exe string) *Transcriber { return &Transcriber{id: id, exe: exe} }

func (t *Transcriber) ID() string                     { return t.id }
func (t *Transcriber) Capabilities() []llm.Capability { return []llm.Capability{llm.Transcribe} }

// It runs here, on a file this process wrote.
func (t *Transcriber) Local() bool { return true }

// whisper-cli decodes through miniaudio, so more than WAV works.
var extensions = map[string]string{
	"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/wave": ".wav", "audio/vnd.wave": ".wav",
	"audio/mpeg": ".mp3", "audio/mp3": ".mp3", "audio/flac": ".flac", "audio/ogg": ".ogg",
}

var detected = regexp.MustCompile(`auto-detected language: ([a-z]{2,3})\b`)

// req.Model is the path of a ggml model file.
func (t *Transcriber) Transcribe(ctx context.Context, req llm.TranscribeRequest) (*llm.TranscribeResult, error) {
	mime, _, _ := strings.Cut(strings.ToLower(req.Audio.MIME), ";")
	ext, ok := extensions[strings.TrimSpace(mime)]
	if !ok {
		return nil, fmt.Errorf("whisper %s: audio %q: %w", t.id, req.Audio.MIME, llm.ErrUnsupported)
	}
	if req.Model == "" {
		return nil, fmt.Errorf("whisper %s: no model file given: %w", t.id, llm.ErrModelNotFound)
	}
	f, err := os.CreateTemp("", "scorix-whisper-*"+ext)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(req.Audio.Data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}

	lang := req.Language
	if lang == "" {
		lang = "auto"
	}
	// No -np: it would also silence the line that names the detected language.
	args := []string{"-m", req.Model, "-f", f.Name(), "-l", lang, "-nt"}
	if t.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(t.Threads))
	}
	cmd := exec.CommandContext(ctx, t.exe, args...)
	cmd.Dir = filepath.Dir(t.exe)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("whisper %s: %w", t.id, ctx.Err())
	}
	// Audio it cannot decode exits 0 with nothing on stdout, which reads like a
	// recording of silence; only stderr says otherwise.
	if msg := errorLines(stderr.String()); runErr != nil || msg != "" {
		if msg == "" {
			msg = runErr.Error()
		}
		kind := error(nil)
		if strings.Contains(msg, "failed to initialize whisper context") {
			kind = llm.ErrModelNotFound
		}
		return nil, &llm.ProviderError{Provider: t.id, Message: msg, Kind: kind}
	}
	res := &llm.TranscribeResult{Text: strings.TrimSpace(stdout.String()), Language: req.Language}
	if m := detected.FindStringSubmatch(stderr.String()); m != nil && req.Language == "" {
		res.Language = m[1]
	}
	return res, nil
}

func errorLines(stderr string) string {
	var out []string
	for line := range strings.Lines(stderr) {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "error:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(l, "error:")))
		}
	}
	return strings.Join(out, "; ")
}

func cliName() string {
	if runtime.GOOS == "windows" {
		return "whisper-cli.exe"
	}
	return "whisper-cli"
}

var errNoBuild = errors.New("whisper.cpp publishes no command-line build for this platform")
