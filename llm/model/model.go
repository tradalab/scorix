// Package model finds model files in a hub, pins what it found to a revision
// that will not move, groups the files that make up one usable model, and keeps
// a verified copy on disk. Hubs are Sources; none is built in as the only one.
package model

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/fetch"
)

type File struct {
	// Slash-separated, relative to the snapshot.
	Path string
	// -1 when the source does not say.
	Size   int64
	Digest *fetch.Digest
	// Set when the source hands out a direct link rather than building one.
	URL string
}

type Snapshot struct {
	Source string
	Repo   string
	// What was asked for, such as "main" or "latest".
	Ref string
	// What that pointed at when resolved: a commit, a tag, a digest. Stored
	// files live under it, so a moved branch never mixes two versions.
	Revision string
	Files    []File
}

type Source interface {
	Name() string
	Resolve(ctx context.Context, ref string) (*Snapshot, error)
	// Called again when a transfer breaks, which is when an expired link gets
	// signed anew.
	Open(ctx context.Context, s *Snapshot, f File, offset int64) (*http.Response, error)
}

// A source whose files are already on this machine; the store uses them where
// they are instead of copying.
type LocalSource interface {
	Source
	LocalPath(s *Snapshot, f File) string
}

var (
	ErrNotFound = errors.New("model: not found")
	// Needs the user to accept terms in a browser; no API call can do it.
	ErrGated = errors.New("model: gated")
)

type Variant struct {
	// The quantization, such as "Q4_K_M"; the file name when there is none. Not
	// unique: a repo holding several models has a Q8_0 of each.
	Name string
	// Unique within the snapshot: the directory and file name, without the
	// shard suffix.
	Key string
	// One file, or every shard in order.
	Files []File
	// The vision projector llama.cpp loads next to it, when the repo has one.
	Projector *File
	Size      int64
	// False when the listing is missing shards: such a variant cannot load.
	Complete bool
}

var (
	shardName = regexp.MustCompile(`(?i)^(.+)-(\d{5})-of-(\d{5})\.gguf$`)
	quantName = regexp.MustCompile(`(?i)(?:^|[-_.])((?:IQ|Q)[1-8](?:_[0-9A-Z]+)*|BF16|F16|F32)(?:[-_.]|$)`)
)

// Hubs have no idea of a model made of several files; the naming convention
// does. Grouping here, once, is what stops a shard-1-only download.
func Variants(s *Snapshot) []Variant {
	type group struct {
		v      Variant
		want   int
		shards map[int]File
	}
	groups := map[string]*group{}
	var order []string
	var projectors []File
	for _, f := range s.Files {
		base := path.Base(f.Path)
		lower := strings.ToLower(base)
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		if strings.Contains(lower, "mmproj") {
			projectors = append(projectors, f)
			continue
		}
		key, idx, want := path.Join(path.Dir(f.Path), strings.TrimSuffix(base, path.Ext(base))), 1, 1
		if stem, i, n, ok := SplitName(base); ok {
			key, idx, want = path.Join(path.Dir(f.Path), stem), i, n
		}
		g := groups[key]
		if g == nil {
			g = &group{v: Variant{Name: variantName(path.Base(key)), Key: key}, want: want, shards: map[int]File{}}
			groups[key] = g
			order = append(order, key)
		}
		g.shards[idx] = f
	}
	out := make([]Variant, 0, len(order))
	for _, key := range order {
		g := groups[key]
		g.v.Complete = true
		for i := 1; i <= g.want; i++ {
			f, ok := g.shards[i]
			if !ok {
				g.v.Complete = false
				continue
			}
			g.v.Files = append(g.v.Files, f)
			g.v.Size += max(f.Size, 0)
		}
		g.v.Projector = pickProjector(projectors, key, order)
		out = append(out, g.v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// "m-00002-of-00003.gguf" -> "m", 2, 3. llama.cpp loads shard 1 and finds the
// others beside it by exactly this name, so an app keeping files on disk needs
// it to list and delete a split model as one.
func SplitName(file string) (stem string, index, count int, ok bool) {
	// Either separator: hub paths use '/', and a path on disk on Windows '\'.
	m := shardName.FindStringSubmatch(file[strings.LastIndexAny(file, `/\`)+1:])
	if m == nil {
		return "", 0, 0, false
	}
	index, _ = strconv.Atoi(m[2])
	count, _ = strconv.Atoi(m[3])
	return m[1], index, count, true
}

func variantName(base string) string {
	if m := quantName.FindStringSubmatch(base); m != nil {
		return strings.ToUpper(m[1])
	}
	return base
}

// A projector belongs to the model it was trained with. In a repo that holds
// several models, one attached to the wrong one loads and produces nonsense, so
// it has to sit in the variant's directory or above it, and a projector whose
// name (without "mmproj" and the quant) is part of some variant's goes with
// those variants only. One that names none of them, like ggml-org's
// "mmproj-model-f16.gguf" next to gemma, is the repo's and goes with any.
func projectorFits(p File, variantKey string, allKeys []string) bool {
	dir := path.Dir(p.Path)
	if dir != "." && path.Dir(variantKey) != dir && !strings.HasPrefix(path.Dir(variantKey)+"/", dir+"/") {
		return false
	}
	stem := strings.ToLower(strings.TrimSuffix(path.Base(p.Path), path.Ext(p.Path)))
	stem = strings.Replace(stem, "mmproj", "", 1)
	if m := quantName.FindStringSubmatchIndex(stem); m != nil {
		stem = stem[:m[2]] + stem[m[3]:]
	}
	stem = strings.Trim(stem, "-_.")
	names := func(key string) bool { return strings.Contains(strings.ToLower(path.Base(key)), stem) }
	if stem == "" || names(variantKey) {
		return true
	}
	for _, k := range allKeys {
		if names(k) {
			return false
		}
	}
	return true
}

// f16 first: the precision llama.cpp's own conversions ship, and half the size
// of f32 for no visible loss in a projector. bf16 is not f16.
func pickProjector(all []File, variantKey string, allKeys []string) *File {
	var fs []File
	for _, f := range all {
		if projectorFits(f, variantKey, allKeys) {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return nil
	}
	rank := func(f File) int {
		n := strings.ToLower(path.Base(f.Path))
		switch {
		case strings.Contains(n, "bf16"):
			return 1
		case strings.Contains(n, "f16"):
			return 0
		default:
			return 2
		}
	}
	best := fs[0]
	for _, f := range fs[1:] {
		if r, br := rank(f), rank(best); r < br || (r == br && len(f.Path) < len(best.Path)) {
			best = f
		}
	}
	return &best
}

func (v Variant) All() []File {
	if v.Projector == nil {
		return v.Files
	}
	return append(append([]File(nil), v.Files...), *v.Projector)
}
