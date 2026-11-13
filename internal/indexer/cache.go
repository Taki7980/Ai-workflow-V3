package indexer

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"sync"

	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
)

// The MCP server answers every request in one long-running process, so it
// would otherwise re-read each index file and rebuild both BM25 indexes per
// query. These caches skip both while the inputs are unchanged. Cached
// indexes are shared between callers and must be treated as read-only.

type cachedFile struct {
	info os.FileInfo
	idx  Index
}

var fileCache = struct {
	sync.Mutex
	m map[string]cachedFile
}{m: map[string]cachedFile{}}

// readIndexFile decodes the index at path, reusing the last decode while the
// file is the same one with the same size and modification time. Index
// writes replace the file by rename, so a rebuild is a different file even
// where modification times are coarse.
func readIndexFile(path string) (Index, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Index{}, err
	}
	// On Windows Stat loads the file ID lazily from the path on first
	// comparison; load it now, or a cached FileInfo would always match the
	// file currently at path.
	os.SameFile(st, st)
	fileCache.Lock()
	c, ok := fileCache.m[path]
	fileCache.Unlock()
	if ok && os.SameFile(c.info, st) && c.info.Size() == st.Size() && c.info.ModTime().Equal(st.ModTime()) {
		return c.idx, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return Index{}, err
	}
	fileCache.Lock()
	fileCache.m[path] = cachedFile{info: st, idx: idx}
	fileCache.Unlock()
	return idx, nil
}

// searchKey identifies one repository's symbols by the identity of their
// backing array, which readIndexFile keeps stable for an unchanged file.
type searchKey struct {
	repo  string
	first *Symbol
	n     int
}

type searchIndex struct {
	keys    []searchKey
	symbols *retrieval.BM25[Hit]
	files   *retrieval.BM25[string]
}

var lastSearch = struct {
	sync.Mutex
	idx *searchIndex
}{}

// searchIndexes returns the symbol and file BM25 indexes over indexes,
// rebuilding them only when the set of symbol slices changed since the last
// call. One entry suffices: a server serves one workspace.
func searchIndexes(indexes map[string]Index) *searchIndex {
	repos := slices.Sorted(maps.Keys(indexes))
	keys := make([]searchKey, len(repos))
	for i, repo := range repos {
		syms := indexes[repo].Symbols
		keys[i] = searchKey{repo: repo, n: len(syms)}
		if len(syms) > 0 {
			keys[i].first = &syms[0]
		}
	}
	lastSearch.Lock()
	cached := lastSearch.idx
	lastSearch.Unlock()
	if cached != nil && slices.Equal(cached.keys, keys) {
		return cached
	}
	s := buildSearchIndex(repos, indexes)
	s.keys = keys
	lastSearch.Lock()
	lastSearch.idx = s
	lastSearch.Unlock()
	return s
}
