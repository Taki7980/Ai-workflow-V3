package retrieval

import (
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// regexpTokenize is the original regular-expression tokenizer, kept as the
// oracle that the byte scanner must match exactly.
func regexpTokenize(text string) []string {
	raw := regexp.MustCompile(`[[:alnum:]]+`).FindAllString(strings.ReplaceAll(text, "_", " "), -1)
	out := []string{}
	for _, token := range raw {
		if lower := strings.ToLower(token); len([]rune(lower)) >= 2 {
			out = append(out, lower)
		}
		r := []rune(token)
		parts, start := []string{}, 0
		for i := 1; i < len(r); i++ {
			if unicode.IsLower(r[i-1]) && unicode.IsUpper(r[i]) || (unicode.IsUpper(r[i-1]) && unicode.IsUpper(r[i]) && i+1 < len(r) && unicode.IsLower(r[i+1])) {
				parts = append(parts, string(r[start:i]))
				start = i
			}
		}
		if parts = append(parts, string(r[start:])); len(parts) > 1 {
			for _, p := range parts {
				if len([]rune(p)) >= 2 {
					out = append(out, strings.ToLower(p))
				}
			}
		}
	}
	return out
}

func FuzzTokenizeMatchesRegexp(f *testing.F) {
	for _, s := range []string{"", "payment_guard DuplicateCharge", "HTTPServer parseURLv2 x", "a1B2cD __init__", "naïve façade ÄBC Δelta", "\xff\xfeAbC", "getHTTPResponseCode ID IDs"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := Tokenize(s), regexpTokenize(s); !slices.Equal(got, want) {
			t.Fatalf("Tokenize(%q) = %q, want %q", s, got, want)
		}
	})
}

// TestBM25MatchesBruteForce checks the inverted index against the textbook
// formula computed document by document.
func TestBM25MatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	words := []string{"payment", "retry", "Handler", "parseConfig", "tests", "gathering", "HTTPServer", "cache_key", "index", "brief"}
	texts := make([]string, 300)
	ids := make([]int, len(texts))
	for i := range texts {
		for range 1 + rng.IntN(8) {
			texts[i] += words[rng.IntN(len(words))] + " "
		}
		ids[i] = i
	}
	b := NewBM25(texts, ids)
	b.B = 0.3
	docs := make([][]string, len(texts))
	df := map[string]int{}
	total := 0
	for i, s := range texts {
		docs[i] = stems(Tokenize(s))
		total += len(docs[i])
		seen := map[string]bool{}
		for _, w := range docs[i] {
			if !seen[w] {
				seen[w] = true
				df[w]++
			}
		}
	}
	avg, n := float64(total)/float64(len(texts)), float64(len(texts))
	for _, query := range []string{"payment retries", "parse config handler", "http server cache", "unknown words", "tests tests index"} {
		want := map[int]float64{}
		q := stems(Tokenize(query))
		slices.Sort(q)
		for i, d := range docs {
			for _, w := range slices.Compact(q) {
				f := 0.0
				for _, x := range d {
					if x == w {
						f++
					}
				}
				if f == 0 {
					continue
				}
				idf := math.Max(math.Log(1+(n-float64(df[w])+.5)/(float64(df[w])+.5)), .01)
				want[i] += idf * f * (b.K1 + 1) / (f + b.K1*(1-b.B+b.B*float64(len(d))/avg))
			}
		}
		got := b.Rank(query)
		if len(got) != len(want) {
			t.Fatalf("%q: %d hits, want %d", query, len(got), len(want))
		}
		for i, g := range got {
			if math.Abs(g.Score-want[g.Value]) > 1e-9 || i > 0 && g.Score > got[i-1].Score {
				t.Fatalf("%q: doc %d score %v, want %v (or out of order)", query, g.Value, g.Score, want[g.Value])
			}
		}
	}
}
