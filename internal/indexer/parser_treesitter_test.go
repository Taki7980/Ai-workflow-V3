//go:build treesitter

package indexer

import (
	"sort"
	"testing"
)

func symbolByName(t *testing.T, symbols []Symbol, name string) Symbol {
	t.Helper()
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol
		}
	}
	t.Fatalf("missing symbol %q in %#v", name, symbols)
	return Symbol{}
}

func symbolNames(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, symbol.Name)
	}
	sort.Strings(out)
	return out
}

func TestTreeSitterPythonSymbols(t *testing.T) {
	source := []byte("@trace\nasync def retry_payment():\n    return True\n\nclass PaymentService:\n    pass\n")
	got, parserName := parseSymbols(source, "payment.py", "sha")
	if parserName != "tree-sitter-python" {
		t.Fatalf("parser=%q", parserName)
	}
	fn := symbolByName(t, got, "retry_payment")
	if fn.Kind != "function" || fn.Line != 2 || fn.EndLine < fn.Line || fn.SHA256 != "sha" {
		t.Fatalf("function=%+v", fn)
	}
	class := symbolByName(t, got, "PaymentService")
	if class.Kind != "type" || class.Line != 5 || class.EndLine < class.Line {
		t.Fatalf("class=%+v", class)
	}
}

func TestTreeSitterPythonRecoversFromSyntaxError(t *testing.T) {
	source := []byte("def good():\n    return 1\n\ndef broken(\n\ndef later():\n    return 2\n")
	got, parserName := parseSymbols(source, "broken.py", "sha")
	if parserName != "tree-sitter-python" {
		t.Fatalf("parser=%q", parserName)
	}
	if symbolByName(t, got, "good").Kind != "function" {
		t.Fatalf("symbols=%#v", got)
	}
}

func TestTreeSitterJavaScriptSymbols(t *testing.T) {
	source := []byte("function processPayment() {}\nclass PaymentService {}\nconst retryPayment = async () => {};\n")
	got, parserName := parseSymbols(source, "payment.js", "sha")
	if parserName != "tree-sitter-javascript" {
		t.Fatalf("parser=%q", parserName)
	}
	for name, kind := range map[string]string{
		"processPayment": "function",
		"PaymentService": "type",
		"retryPayment":   "function",
	} {
		symbol := symbolByName(t, got, name)
		if symbol.Kind != kind {
			t.Fatalf("%s kind=%q want=%q", name, symbol.Kind, kind)
		}
	}
}

func TestTreeSitterJSXUsesJavaScriptGrammar(t *testing.T) {
	source := []byte("const PaymentCard = () => <div />;\n")
	got, parserName := parseSymbols(source, "card.jsx", "sha")
	if parserName != "tree-sitter-javascript" {
		t.Fatalf("parser=%q", parserName)
	}
	if symbolByName(t, got, "PaymentCard").Kind != "function" {
		t.Fatalf("symbols=%#v", got)
	}
}

func TestTreeSitterTypeScriptSymbols(t *testing.T) {
	source := []byte("interface Payment { id: string }\ntype RetryPolicy = \"safe\" | \"fast\";\nenum State { Ready }\nabstract class BaseProcessor {}\nfunction settle(): void {}\nconst retry = () => {};\n")
	got, parserName := parseSymbols(source, "payment.ts", "sha")
	if parserName != "tree-sitter-typescript" {
		t.Fatalf("parser=%q", parserName)
	}
	for name, kind := range map[string]string{
		"Payment":       "type",
		"RetryPolicy":   "type",
		"State":         "type",
		"BaseProcessor": "type",
		"settle":        "function",
		"retry":         "function",
	} {
		symbol := symbolByName(t, got, name)
		if symbol.Kind != kind {
			t.Fatalf("%s kind=%q want=%q", name, symbol.Kind, kind)
		}
	}
}

func TestTreeSitterTSXSymbols(t *testing.T) {
	source := []byte("const PaymentCard = () => <div />;\nfunction Checkout() { return <PaymentCard />; }\n")
	got, parserName := parseSymbols(source, "card.tsx", "sha")
	if parserName != "tree-sitter-tsx" {
		t.Fatalf("parser=%q", parserName)
	}
	for _, name := range []string{"PaymentCard", "Checkout"} {
		if symbolByName(t, got, name).Kind != "function" {
			t.Fatalf("symbols=%#v", got)
		}
	}
}
