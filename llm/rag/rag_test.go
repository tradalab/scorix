package rag

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/llmtest"
)

func numbered(n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = "w" + strconv.Itoa(i)
	}
	return strings.Join(w, " ")
}

func TestSplitOverlapsItsWindows(t *testing.T) {
	chunks := Split(numbered(1000), 400, 50)
	// Windows at 0, 350 and 700.
	if len(chunks) != 3 {
		t.Fatalf("%d chunks", len(chunks))
	}
	for i, c := range chunks {
		first := strings.Fields(c.Content)[0]
		if c.Ord != i || c.Page != 0 || first != "w"+strconv.Itoa(i*350) {
			t.Errorf("chunk %d: ord %d page %d starts at %s", i, c.Ord, c.Page, first)
		}
	}
	if n := len(strings.Fields(chunks[2].Content)); n != 300 {
		t.Errorf("the last window holds %d words", n)
	}
}

func TestSplitDefaultsWhatCannotWork(t *testing.T) {
	if Split("  \n ", 10, 2) != nil {
		t.Error("blank text made chunks")
	}
	// An overlap as wide as the window would never move it: an eighth instead.
	if c := Split(numbered(20), 8, 8); len(c) != 3 || strings.Fields(c[1].Content)[0] != "w7" {
		t.Errorf("%+v", c)
	}
	if c := Split(numbered(20), 8, -1); len(c) != 3 || strings.Fields(c[1].Content)[0] != "w7" {
		t.Errorf("negative overlap: %+v", c)
	}
	// The window that reaches the end is the last: one more step would start
	// a chunk that the one before already holds.
	if c := Split(numbered(10), 8, 4); len(c) != 2 {
		t.Errorf("%d chunks for 10 words", len(c))
	}
	if c := Split(numbered(DefaultWords+1), 0, 0); len(c) != 2 || len(strings.Fields(c[0].Content)) != DefaultWords {
		t.Errorf("no window size: %d chunks", len(c))
	}
}

func TestSplitPagesCitesThePageAndCountsOn(t *testing.T) {
	pages := []string{strings.Repeat("alpha ", 10), strings.Repeat("beta ", 10), "", strings.Repeat("gamma ", 10)}
	got := SplitPages(pages, 4, 1)
	if len(got) == 0 {
		t.Fatal("no chunks")
	}
	for i, c := range got {
		want := map[string]int{"alpha": 1, "beta": 2, "gamma": 4}[strings.Fields(c.Content)[0]]
		if c.Ord != i || c.Page != want {
			t.Errorf("chunk %d: ord %d, page %d, want %d", i, c.Ord, c.Page, want)
		}
	}
	two := SplitPages([]string{"alpha alpha", "beta beta"}, 100, 10)
	if len(two) != 2 || strings.Contains(two[0].Content, "beta") {
		t.Errorf("a chunk spans pages: %+v", two)
	}
}

func TestVectorsSurviveTheRoundTrip(t *testing.T) {
	vec := []float32{0, 1, -1, 3.5, 0.001, 1234.5, float32(math.Inf(1))}
	got := Decode(Encode(vec))
	if len(got) != len(vec) {
		t.Fatalf("%d dimensions", len(got))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("[%d] = %v", i, got[i])
		}
	}
	if b := Encode([]float32{1}); len(b) != 4 || b[3] != 0x3f || b[0] != 0 {
		t.Errorf("1.0 encodes as % x, not little-endian", b)
	}
	if got := Decode([]byte{0, 0, 0x80, 0x3f, 9}); len(got) != 1 || got[0] != 1 {
		t.Errorf("a trailing partial value: %v", got)
	}
}

func TestCosine(t *testing.T) {
	for name, c := range map[string]struct {
		a, b []float32
		want float64
	}{
		"same":       {[]float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		"orthogonal": {[]float32{1, 0}, []float32{0, 1}, 0},
		"parallel":   {[]float32{1, 2, 3}, []float32{2, 4, 6}, 1},
		"opposite":   {[]float32{1, 0}, []float32{-1, 0}, -1},
		"lengths":    {[]float32{1}, []float32{1, 2}, 0},
		"zero":       {[]float32{0, 0}, []float32{1, 1}, 0},
		"empty":      {nil, nil, 0},
		// A vector with an infinity or a NaN in it comes out of a provider
		// that misbehaved; the score has to stay comparable either way.
		"infinite":     {[]float32{float32(math.Inf(1)), 0}, []float32{1, 0}, 0},
		"not-a-number": {[]float32{float32(math.NaN()), 1}, []float32{1, 0}, 0},
	} {
		if got := Cosine(c.a, c.b); math.Abs(got-c.want) > 1e-9 || math.IsNaN(got) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestNearestKeepsOnlyWhatIsClose(t *testing.T) {
	q := []float32{1, 0}
	cands := []Vector{
		{"far", []float32{-1, 0}},
		{"b", []float32{1, 1}},
		{"exact", []float32{2, 0}},
		{"a", []float32{1, 1}},
		{"odd", []float32{1, 0, 0}},
		{"side", []float32{0, 1}},
	}
	got := Nearest(q, cands, 0)
	if strings.Join(IDs(got), ",") != "exact,a,b" || got[0].Score != 1 {
		t.Errorf("%+v: unrelated, opposite and other-sized vectors are left out, ties go by id", got)
	}
	if top := Nearest(q, cands, 2); len(top) != 2 || top[1].ID != "a" {
		t.Errorf("%+v", top)
	}
}

func TestFuseRewardsWhatBothListsFound(t *testing.T) {
	got := Fuse([][]string{{"a", "b", "c"}, {"c", "a", "d"}}, 3)
	if strings.Join(IDs(got), ",") != "a,c,b" {
		t.Errorf("%+v", got)
	}
	// 1/61 + 1/62, compared with a tolerance: the sum runs in map order.
	if want := 1.0/61 + 1.0/62; math.Abs(got[0].Score-want) > 1e-12 {
		t.Errorf("top score %v, want %v", got[0].Score, want)
	}
	if got := Fuse(nil, 3); len(got) != 0 {
		t.Errorf("%v", got)
	}
	// One live list is the common case, a store with no model configured or
	// no keyword hit, and it must come back as it went in.
	in := []string{"first", "second", "third"}
	if got := IDs(Fuse([][]string{in, nil}, 0)); strings.Join(got, ",") != "first,second,third" {
		t.Errorf("%v", got)
	}
}

type batches struct {
	sizes  []int
	models []string
	drop   int // the call that loses one vector, 1-based
	extra  int // the call that gains one
	fail   error
}

func (b *batches) embed(_ context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error) {
	b.sizes = append(b.sizes, len(req.Input))
	b.models = append(b.models, req.Model)
	call := len(b.sizes)
	if b.fail != nil {
		return nil, b.fail
	}
	res := &llm.EmbedResult{}
	for _, text := range req.Input {
		n, _ := strconv.Atoi(strings.TrimPrefix(text, "t"))
		res.Vectors = append(res.Vectors, []float32{float32(n)})
	}
	if call == b.drop {
		res.Vectors = res.Vectors[1:]
	}
	if call == b.extra {
		res.Vectors = append(res.Vectors, []float32{-1})
	}
	return res, nil
}

func texts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "t" + strconv.Itoa(i)
	}
	return out
}

func TestEmbedAllKeepsEveryVectorOnItsText(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 200} {
		b := &batches{}
		vecs, err := EmbedAll(context.Background(), b.embed, "m", texts(n), 0)
		if err != nil || len(vecs) != n {
			t.Fatalf("%d texts: %d vectors, %v", n, len(vecs), err)
		}
		for i, v := range vecs {
			if v[0] != float32(i) {
				t.Fatalf("%d texts: vector %d belongs to text %v", n, i, v[0])
			}
		}
		for _, s := range b.sizes {
			if s == 0 || s > 64 {
				t.Errorf("%d texts: a batch of %d", n, s)
			}
		}
	}
	b := &batches{}
	if _, err := EmbedAll(context.Background(), b.embed, "m", texts(10), 4); err != nil || fmt.Sprint(b.sizes) != "[4 4 2]" || fmt.Sprint(b.models) != "[m m m]" {
		t.Errorf("sizes %v, models %v, %v", b.sizes, b.models, err)
	}
}

// A batch that comes back short is caught in that batch, not at the end where
// a later long one would make the total add up again.
func TestAShortBatchIsCaughtWhereItHappens(t *testing.T) {
	b := &batches{drop: 1, extra: 2}
	if _, err := EmbedAll(context.Background(), b.embed, "m", texts(8), 4); err == nil || !strings.Contains(err.Error(), "3 vectors for 4 texts") {
		t.Errorf("%v", err)
	}
	down := errors.New("down")
	if _, err := EmbedAll(context.Background(), (&batches{fail: down}).embed, "m", texts(3), 2); !errors.Is(err, down) {
		t.Errorf("%v", err)
	}
}

func TestSlotEmbedsThroughTheRegistry(t *testing.T) {
	p := llmtest.New("fake")
	reg := llm.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind("search", llm.Binding{Provider: "fake", Model: "e"}); err != nil {
		t.Fatal(err)
	}
	vecs, err := EmbedAll(context.Background(), Slot(reg, "search"), "", []string{"a", "b"}, 1)
	if err != nil || len(vecs) != 2 || Cosine(vecs[0], vecs[1]) == 1 {
		t.Errorf("%v, %v", vecs, err)
	}
	if _, err := EmbedAll(context.Background(), Slot(reg, "nowhere"), "", []string{"a"}, 1); !errors.Is(err, llm.ErrNoBinding) {
		t.Errorf("%v", err)
	}
}
