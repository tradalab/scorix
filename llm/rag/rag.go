// Package rag holds the parts of retrieval that do not depend on where an app
// keeps its documents: splitting text, embedding it in batches, comparing
// vectors and fusing rankings. The store is the app's, full text index and
// all, so nothing here reads or writes one.
package rag

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tradalab/scorix/llm"
)

type Chunk struct {
	Ord     int
	Content string
	// 1-based; zero for text that had no pages.
	Page int
}

// What loom has shipped with: a passage long enough to answer from, with
// overlap so a sentence cut at a boundary is whole in one of the two chunks.
const (
	DefaultWords   = 400
	DefaultOverlap = 50
)

// Windows of words; an overlap that would never let the window advance falls
// back to an eighth of it.
func Split(text string, words, overlap int) []Chunk {
	fields := strings.Fields(text)
	if words <= 0 {
		words = DefaultWords
	}
	if overlap < 0 || overlap >= words {
		overlap = words / 8
	}
	step := words - overlap
	var out []Chunk
	for start, ord := 0, 0; start < len(fields); start += step {
		end := min(start+words, len(fields))
		out = append(out, Chunk{Ord: ord, Content: strings.Join(fields[start:end], " ")})
		ord++
		if end == len(fields) {
			break
		}
	}
	return out
}

// Page by page, so no chunk spans two and each cites the page it came from;
// Ord runs on across pages.
func SplitPages(pages []string, words, overlap int) []Chunk {
	var out []Chunk
	for i, page := range pages {
		for _, c := range Split(page, words, overlap) {
			c.Ord, c.Page = len(out), i+1
			out = append(out, c)
		}
	}
	return out
}

// Little-endian float32s: four bytes a dimension, the same on every machine
// the file is copied to.
func Encode(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func Decode(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// Zero on a length mismatch, and on anything that would come out NaN.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	s := dot / (math.Sqrt(na) * math.Sqrt(nb))
	// Zero rather than NaN, which is what a zero vector divides into and what
	// an infinity from a provider that misbehaved produces: NaN slips through
	// every comparison a caller would filter on, and in a sort comparator it
	// leaves the order arbitrary.
	if math.IsNaN(s) {
		return 0
	}
	return s
}

type Vector struct {
	ID     string
	Values []float32
}

type Scored struct {
	ID    string
	Score float64
}

func sortScored(s []Scored, k int) []Scored {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Score != s[j].Score {
			return s[i].Score > s[j].Score
		}
		return s[i].ID < s[j].ID
	})
	if k > 0 && len(s) > k {
		s = s[:k]
	}
	return s
}

// The k closest by cosine. Nothing at or below zero: an unrelated passage
// ranked anyway would reach the model as context.
func Nearest(query []float32, candidates []Vector, k int) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		if s := Cosine(query, c.Values); s > 0 {
			out = append(out, Scored{ID: c.ID, Score: s})
		}
	}
	return sortScored(out, k)
}

// Reciprocal rank fusion. Rank, not score: a cosine and a full-text relevance
// have no common scale and no honest way to be added, and position is the one
// thing they agree on. Not normalized either, since the top score depends on
// how many lists held the id.
func Fuse(lists [][]string, k int) []Scored {
	const damping = 60
	acc := map[string]float64{}
	for _, list := range lists {
		for rank, id := range list {
			acc[id] += 1.0 / float64(damping+rank+1)
		}
	}
	out := make([]Scored, 0, len(acc))
	for id, s := range acc {
		out = append(out, Scored{ID: id, Score: s})
	}
	return sortScored(out, k)
}

func IDs(s []Scored) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.ID
	}
	return out
}

type EmbedFunc func(ctx context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error)

func Slot(reg *llm.Registry, slot string) EmbedFunc {
	return func(ctx context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error) {
		return reg.Embed(ctx, slot, req)
	}
}

// One vector per text, in order. Each batch is checked on its own: a provider
// that drops one input would otherwise shift every vector after it onto the
// wrong text, with the total still adding up.
func EmbedAll(ctx context.Context, embed EmbedFunc, model string, texts []string, batch int) ([][]float32, error) {
	if batch <= 0 {
		batch = 64
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		res, err := embed(ctx, llm.EmbedRequest{Model: model, Input: texts[start:end]})
		if err != nil {
			return nil, err
		}
		if len(res.Vectors) != end-start {
			return nil, fmt.Errorf("rag: %d vectors for %d texts (%d to %d)", len(res.Vectors), end-start, start, end-1)
		}
		out = append(out, res.Vectors...)
	}
	return out, nil
}
