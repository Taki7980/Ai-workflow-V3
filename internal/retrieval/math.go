package retrieval

import (
	"math"
	"slices"
	"sort"
	"strings"
)

// Tokenize splits text into lowercase ASCII alphanumeric tokens, additionally
// emitting camelCase-split sub-tokens for improved matching. Underscores and
// every non-ASCII byte separate tokens.
func Tokenize(text string) []string {
	return appendTokens(make([]string, 0, len(text)/4), text)
}

// appendTokens is Tokenize into a reusable buffer. A byte scanner instead of
// a regular expression: index builds tokenize every symbol on every query.
func appendTokens(out []string, text string) []string {
	for i := 0; i < len(text); {
		if !isAlnum(text[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isAlnum(text[j]) {
			j++
		}
		tok := text[i:j]
		i = j
		if len(tok) >= 2 {
			out = append(out, strings.ToLower(tok))
		}
		// camelCase parts are emitted only when the token splits at least once.
		start := 0
		for k := 1; k < len(tok); k++ {
			if isLower(tok[k-1]) && isUpper(tok[k]) || isUpper(tok[k-1]) && isUpper(tok[k]) && k+1 < len(tok) && isLower(tok[k+1]) {
				out = appendPart(out, tok[start:k])
				start = k
			}
		}
		if start > 0 {
			out = appendPart(out, tok[start:])
		}
	}
	return out
}

func appendPart(out []string, p string) []string {
	if len(p) >= 2 {
		out = append(out, strings.ToLower(p))
	}
	return out
}

func isLower(c byte) bool { return 'a' <= c && c <= 'z' }
func isUpper(c byte) bool { return 'A' <= c && c <= 'Z' }
func isAlnum(c byte) bool { return isLower(c) || isUpper(c) || '0' <= c && c <= '9' }

// stems maps tokens to a crude English stem so task prose ("gathering",
// "tiers") matches identifiers ("gather", "tier"). BM25 only: routing counts
// raw Tokenize output and must stay V2-identical.
func stems(toks []string) []string {
	for i, t := range toks {
		switch n := len(t); {
		case n > 5 && strings.HasSuffix(t, "ing"):
			t = t[:n-3]
		case n > 4 && strings.HasSuffix(t, "ed"):
			t = t[:n-2]
		case n > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss"):
			t = t[:n-1]
		}
		if len(t) > 4 && strings.HasSuffix(t, "e") {
			t = t[:len(t)-1]
		}
		toks[i] = t
	}
	return toks
}

type posting struct {
	doc, tf int32
}

// BM25 is an inverted index: Rank touches only documents that contain a
// query term, so query cost follows matches, not corpus size.
type BM25[T any] struct {
	K1       float64
	B        float64
	values   []T
	lengths  []int32
	postings map[string][]posting
	avgDL    float64
}

type Scored[T any] struct {
	Score float64
	Value T
}

// NewBM25 builds a BM25 index over texts, pairing each text with its
// corresponding value. Mismatched lengths yield an empty index.
func NewBM25[T any](texts []string, values []T) *BM25[T] {
	b := &BM25[T]{K1: 1.5, B: .75}
	if len(texts) != len(values) || len(texts) == 0 {
		return b
	}
	b.values = slices.Clone(values)
	b.lengths = make([]int32, len(texts))
	b.postings = make(map[string][]posting, len(texts))
	var toks []string
	total := 0
	for i, t := range texts {
		toks = stems(appendTokens(toks[:0], t))
		// Sorting groups repeats, so term frequency needs no per-document map.
		slices.Sort(toks)
		for j := 0; j < len(toks); {
			k := j + 1
			for k < len(toks) && toks[k] == toks[j] {
				k++
			}
			b.postings[toks[j]] = append(b.postings[toks[j]], posting{doc: int32(i), tf: int32(k - j)})
			j = k
		}
		b.lengths[i] = int32(len(toks))
		total += len(toks)
	}
	b.avgDL = float64(total) / float64(len(texts))
	return b
}

// Rank scores the documents in b against query using the BM25 formula and
// returns those matching at least one query term, sorted by descending score;
// ties keep document order.
func (b *BM25[T]) Rank(query string) []Scored[T] {
	if b.avgDL <= 0 {
		return nil
	}
	q := stems(Tokenize(query))
	slices.Sort(q)
	q = slices.Compact(q)
	n := float64(len(b.values))
	scores := make([]float64, len(b.values))
	for _, term := range q {
		ps := b.postings[term]
		if len(ps) == 0 {
			continue
		}
		df := float64(len(ps))
		idf := math.Max(math.Log(1+(n-df+.5)/(df+.5)), .01)
		for _, p := range ps {
			f := float64(p.tf)
			lenNorm := 1 - b.B + b.B*(float64(b.lengths[p.doc])/b.avgDL)
			scores[p.doc] += idf * (f * (b.K1 + 1)) / (f + b.K1*lenNorm)
		}
	}
	out := []Scored[T]{}
	for i, s := range scores {
		if s > 0 {
			out = append(out, Scored[T]{Score: s, Value: b.values[i]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

type Ranked[T any] struct {
	Value T
	Key   string
}

// RRF combines multiple ranked lists using Reciprocal Rank Fusion with
// smoothing constant k (defaulting to 60 when non-positive), returning the
// fused results sorted by descending score.
func RRF[T any](rankings [][]Ranked[T], k float64) []Scored[T] {
	if k <= 0 {
		k = 60
	}
	scores := map[string]float64{}
	values := map[string]T{}
	order := map[string]int{}
	seen := 0
	for _, ranking := range rankings {
		local := map[string]struct{}{}
		for i, item := range ranking {
			if _, ok := local[item.Key]; ok {
				continue
			}
			local[item.Key] = struct{}{}
			if _, ok := order[item.Key]; !ok {
				order[item.Key] = seen
				seen++
				values[item.Key] = item.Value
			}
			scores[item.Key] += 1 / (k + float64(i+1))
		}
	}
	keys := make([]string, 0, len(scores))
	for k := range scores {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if scores[keys[i]] == scores[keys[j]] {
			return order[keys[i]] < order[keys[j]]
		}
		return scores[keys[i]] > scores[keys[j]]
	})
	out := make([]Scored[T], 0, len(keys))
	for _, key := range keys {
		out = append(out, Scored[T]{Score: scores[key], Value: values[key]})
	}
	return out
}

// Jaccard returns the Jaccard similarity coefficient between sets a and b,
// or 0 if they have no elements in common.
func Jaccard(a, b map[string]struct{}) float64 {
	inter := 0
	for x := range a {
		if _, ok := b[x]; ok {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
