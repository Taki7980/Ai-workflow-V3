package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeBenchmarkFixture(b *testing.B, root string) {
	b.Helper()
	for i := 0; i < 40; i++ {
		path := filepath.Join(root, fmt.Sprintf("file_%02d.go", i))
		content := []byte(fmt.Sprintf("package fixture\nfunc Symbol%02d() {}\n", i))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIndexFull(b *testing.B) {
	root := b.TempDir()
	repo := incrementalRepo()
	writeBenchmarkFixture(b, root)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIndexIncrementalNoChange(b *testing.B) {
	root := b.TempDir()
	repo := incrementalRepo()
	writeBenchmarkFixture(b, root)
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental); err != nil {
			b.Fatal(err)
		} else if stats.Reparsed != 0 {
			b.Fatalf("unexpected reparses: %#v", stats)
		}
	}
}

func BenchmarkIndexIncrementalOneChanged(b *testing.B) {
	root := b.TempDir()
	repo := incrementalRepo()
	writeBenchmarkFixture(b, root)
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		b.Fatal(err)
	}
	changed := filepath.Join(root, "file_00.go")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := os.WriteFile(changed, []byte(fmt.Sprintf("package fixture\nfunc Changed%06d() {}\n", i)), 0o644); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental); err != nil {
			b.Fatal(err)
		} else if stats.Reparsed != 1 || stats.Changed != 1 {
			b.Fatalf("unexpected changed-file stats: %#v", stats)
		}
	}
}
