//go:build treesitter

package indexer

import "testing"

var (
	benchmarkParserSymbols []Symbol
	benchmarkParserName    string
)

func BenchmarkParserPilot(b *testing.B) {
	fixtures := []struct {
		name   string
		path   string
		source []byte
	}{
		{
			name: "python",
			path: "payment.py",
			source: []byte("class PaymentService:\n    def retry(self):\n        return True\n\ndef settle():\n    return 1\n"),
		},
		{
			name: "javascript",
			path: "payment.js",
			source: []byte("class PaymentService {}\nfunction settle() {}\nconst retry = async () => {};\n"),
		},
		{
			name: "typescript",
			path: "payment.ts",
			source: []byte("interface Payment { id: string }\ntype Retry = \"safe\" | \"fast\";\nfunction settle(): void {}\nconst retry = () => {};\n"),
		},
		{
			name: "tsx",
			path: "payment.tsx",
			source: []byte("const PaymentCard = () => <div />;\nfunction Checkout() { return <PaymentCard />; }\n"),
		},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				symbols, parserName := parseSymbols(fixture.source, fixture.path, "sha")
				if parserName == "" || parserName == "regex" {
					b.Fatalf("unexpected parser %q", parserName)
				}
				benchmarkParserSymbols = symbols
				benchmarkParserName = parserName
			}
		})
	}
}
