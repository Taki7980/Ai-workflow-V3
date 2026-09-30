package indexer

import (
	"errors"
	"reflect"
	"testing"
)

type fakeSymbolParser struct {
	name      string
	ext       string
	symbols   []Symbol
	err       error
	calls     *int
}

func (p fakeSymbolParser) Name() string { return p.name }
func (p fakeSymbolParser) Supports(ext string) bool { return ext == p.ext }
func (p fakeSymbolParser) Parse(_ []byte, _, _ string) ([]Symbol, error) {
	if p.calls != nil {
		*p.calls++
	}
	return p.symbols, p.err
}

func TestGoParserPreservesFunctionAndTypeExtraction(t *testing.T) {
	source := []byte("package sample\n\ntype Payment struct{}\n\nfunc (Payment) Charge() {}\nfunc Refund() {}\n")
	got, parserName := parseSymbols(source, "service.go", "digest")
	if parserName != "go-ast" {
		t.Fatalf("parser=%q want go-ast", parserName)
	}
	want := []Symbol{
		{Name: "Payment", Kind: "type", Path: "service.go", Line: 3, EndLine: 3, SHA256: "digest"},
		{Name: "Charge", Kind: "function", Path: "service.go", Line: 5, EndLine: 5, SHA256: "digest"},
		{Name: "Refund", Kind: "function", Path: "service.go", Line: 6, EndLine: 6, SHA256: "digest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols=%#v want=%#v", got, want)
	}
}

func TestGoSyntaxErrorFallsBackToRegex(t *testing.T) {
	source := []byte("package sample\nfunc Broken(\n")
	got, parserName := parseSymbols(source, "broken.go", "digest")
	if parserName != "regex" {
		t.Fatalf("parser=%q want regex", parserName)
	}
	if len(got) != 1 || got[0].Name != "Broken" {
		t.Fatalf("symbols=%#v", got)
	}
}

func TestSuccessfulEmptyParseDoesNotFallThrough(t *testing.T) {
	firstCalls, fallbackCalls := 0, 0
	parsers := []symbolParser{
		fakeSymbolParser{name: "empty", ext: ".x", symbols: nil, calls: &firstCalls},
		fakeSymbolParser{name: "fallback", ext: ".x", symbols: []Symbol{{Name: "wrong"}}, calls: &fallbackCalls},
	}
	got, name := parseWithParsers([]byte("x"), "file.x", "digest", parsers)
	if name != "empty" || len(got) != 0 {
		t.Fatalf("parser=%q symbols=%#v", name, got)
	}
	if firstCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("calls first=%d fallback=%d", firstCalls, fallbackCalls)
	}
}

func TestParserFailureFallsThroughDeterministically(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	want := []Symbol{{Name: "selected", Path: "file.x"}}
	parsers := []symbolParser{
		fakeSymbolParser{name: "broken", ext: ".x", err: errors.New("broken"), calls: &firstCalls},
		fakeSymbolParser{name: "second", ext: ".x", symbols: want, calls: &secondCalls},
	}
	got, name := parseWithParsers([]byte("x"), "file.x", "digest", parsers)
	if name != "second" || !reflect.DeepEqual(got, want) {
		t.Fatalf("parser=%q symbols=%#v", name, got)
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("calls first=%d second=%d", firstCalls, secondCalls)
	}
}
