package retrieval

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var wordRE = regexp.MustCompile(`[[:alnum:]]+`)

func Tokenize(text string) []string {
	raw := wordRE.FindAllString(strings.ReplaceAll(text, "_", " "), -1)
	out := make([]string, 0, len(raw)*2)
	for _, token := range raw {
		lower := strings.ToLower(token)
		if len([]rune(lower)) >= 2 {
			out = append(out, lower)
		}
		parts := camelParts(token)
		if len(parts) > 1 {
			for _, p := range parts {
				if len([]rune(p)) >= 2 {
					out = append(out, strings.ToLower(p))
				}
			}
		}
	}
	return out
}

func camelParts(s string) []string {
	r := []rune(s)
	if len(r) == 0 {
		return nil
	}
	start := 0
	out := []string{}
	for i := 1; i < len(r); i++ {
		boundary := unicode.IsLower(r[i-1]) && unicode.IsUpper(r[i]) || (unicode.IsUpper(r[i-1]) && unicode.IsUpper(r[i]) && i+1 < len(r) && unicode.IsLower(r[i+1]))
		if boundary {
			out = append(out, string(r[start:i]))
			start = i
		}
	}
	out = append(out, string(r[start:]))
	return out
}

type Document[T any] struct {
	Value  T
	Tokens []string
	Term   map[string]int
	Set    map[string]struct{}
	Length int
}

type BM25[T any] struct {
	K1    float64
	B     float64
	Docs  []Document[T]
	IDF   map[string]float64
	AvgDL float64
}

type Scored[T any] struct {
	Score float64
	Value T
}

func NewBM25[T any](texts []string, values []T) *BM25[T] {
	b := &BM25[T]{K1: 1.5, B: .75, IDF: map[string]float64{}}
	if len(texts) != len(values) {
		return b
	}
	df := map[string]int{}
	total := 0
	for i, t := range texts {
		toks := Tokenize(t)
		term := map[string]int{}
		set := map[string]struct{}{}
		for _, x := range toks {
			term[x]++
			set[x] = struct{}{}
		}
		for x := range set {
			df[x]++
		}
		b.Docs = append(b.Docs, Document[T]{Value: values[i], Tokens: toks, Term: term, Set: set, Length: len(toks)})
		total += len(toks)
	}
	if len(b.Docs) > 0 {
		b.AvgDL = float64(total) / float64(len(b.Docs))
	}
	n := float64(len(b.Docs))
	for token, f := range df {
		idf := math.Log(1 + (n-float64(f)+.5)/(float64(f)+.5))
		if idf < .01 {
			idf = .01
		}
		b.IDF[token] = idf
	}
	return b
}

func (b *BM25[T]) Rank(query string) []Scored[T] {
	if b.AvgDL <= 0 {
		return nil
	}
	qset := map[string]struct{}{}
	for _, t := range Tokenize(query) {
		qset[t] = struct{}{}
	}
	out := []Scored[T]{}
	for _, d := range b.Docs {
		score := 0.0
		lenNorm := 1 - b.B + b.B*(float64(d.Length)/b.AvgDL)
		for q := range qset {
			f := d.Term[q]
			if f == 0 {
				continue
			}
			idf := b.IDF[q]
			if idf == 0 {
				idf = .01
			}
			score += idf * (float64(f) * (b.K1 + 1)) / (float64(f) + b.K1*lenNorm)
		}
		if score > 0 {
			out = append(out, Scored[T]{Score: score, Value: d.Value})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

type Ranked[T any] struct {
	Value T
	Key   string
}

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
