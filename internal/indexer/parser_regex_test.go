package indexer

import (
	"slices"
	"testing"
)

func TestRegexSymbolsAcrossLanguages(t *testing.T) {
	cases := []struct {
		path, src string
		want      []string
	}{
		{"a.py", "class Repo(Base):\n    async def fetch(self):\n        async with lock(x):\n            pass\ndef top(): pass\n", []string{"Repo", "fetch", "top"}},
		{"a.ts", "export const useCart = () => {\n}\nexport default async function handler(req) {}\nconst Card: React.FC<P> = ({ a }) => (\n" +
			"export class Store {\n  private async load(id: string): Promise<void> {\n  get total() {\n  render() {\n    if (x) {\n    useEffect(() => {\n" +
			"const total = (a + b) * 2\nexport interface Props {}\nexport type Id = string\nexport const enum Color { Red }\n" +
			"const filtered = xs.filter((c) => {\nconst t = window.setTimeout(() => stop(), 10)\nconst fetchStats = useCallback(async () => {\nconst ident = <T>(x: T): T => x\nconst setUp = (\n",
			[]string{"useCart", "handler", "Card", "Store", "load", "total", "render", "Props", "Id", "Color", "fetchStats", "ident", "setUp"}},
		{"A.java", "public final class Billing {\n  @Override\n  public List<User> findAll(int page) {\n  private static void main(String[] args) {\n" +
			"  private final Map<String, User> users = new HashMap<>();\n  public Billing() {\npublic record Point(int x) {}\n",
			[]string{"Billing", "findAll", "main", "Billing", "Point"}},
		{"A.cs", "namespace Shop.Api;\npublic sealed partial class OrdersController {\n  public async Task<IActionResult> Get(int id) {\n  public string Name { get; set; }\n",
			[]string{"Api", "OrdersController", "Get"}},
		{"a.kt", "data class User(val id: Int)\nsealed class Result\nenum class Color { RED }\nsuspend fun load(): User {\nfun String.slug(): String =\nobject Registry {\n",
			[]string{"User", "Result", "Color", "load", "slug", "Registry"}},
		{"a.swift", "public protocol Store {}\nextension Store {\n  @objc private func handleTap(_ s: Any) {\n  public init(name: String) {\n",
			[]string{"Store", "Store", "handleTap", "init"}},
		{"a.php", "final class Invoice {\n  public static function create(array $a) {\nfunction helper() {}\n", []string{"Invoice", "create", "helper"}},
		{"a.rb", "module Billing\n  class Invoice\n    def self.find_by(id)\n    private def total\n", []string{"Billing", "Invoice", "find_by", "total"}},
		{"a.rs", "pub(crate) async fn retry() {}\npub trait Store {}\npub enum Kind {}\nmod tests;\nimpl Store for X {}\nstatic FOO: Mutex<Vec<u8>> = Mutex::new(Vec::new());\n",
			[]string{"retry", "Store", "Kind", "tests"}},
		{"a.c", "static void *worker(void *arg) {\nint main(int argc, char **argv)\nint proto(int);\ntypedef struct {\nstruct node {\n#define MAX(a) (a)\n",
			[]string{"worker", "main", "node"}},
		{"a.cpp", "void Cache::evict(int n) const {\ntemplate <typename T>\nstd::vector<int> compute(int n) {\n", []string{"Cache::evict", "compute"}},
		{"broken.go", "package x\nfunc (s *S) Run(\n", []string{"Run"}},
	}
	for _, c := range cases {
		got := []string{}
		for _, s := range regexSymbols(c.src, c.path, "d") {
			got = append(got, s.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q want %q", c.path, got, c.want)
		}
	}
}

func TestRegexSymbolsSurviveLongLines(t *testing.T) {
	src := "var x = \"" + string(make([]byte, 100<<10)) + "\"\nfunction after() {}\n"
	if got := regexSymbols(src, "bundle.js", "d"); len(got) != 1 || got[0].Name != "after" || got[0].Line != 2 {
		t.Fatalf("symbols after a 100 KB line = %#v", got)
	}
}
