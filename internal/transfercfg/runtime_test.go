package transfercfg

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func testRuntime(t *testing.T) (*Runtime, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, err := New(context.Background(), st, Snapshot{DownloadThreads: 4, UploadThreads: 4, DownloadConnections: 4, UploadConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	return r, st
}

func TestRuntimeOverrideAndClear(t *testing.T) {
	r, st := testRuntime(t)
	before := r.View()
	if before.DownloadThreads.Effective != 4 || before.DownloadThreads.Overridden {
		t.Fatalf("unexpected defaults: %+v", before)
	}
	if _, err := r.Set(context.Background(), DownloadThreads, 16); err != nil {
		t.Fatal(err)
	}
	got := r.View()
	if got.DownloadThreads.Effective != 16 || !got.DownloadThreads.Overridden {
		t.Fatalf("override not published: %+v", got)
	}
	r2, err := New(context.Background(), st, Snapshot{DownloadThreads: 4, UploadThreads: 4, DownloadConnections: 4, UploadConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Snapshot().DownloadThreads != 16 {
		t.Fatalf("override not persisted: %+v", r2.Snapshot())
	}
	if _, err := r.Clear(context.Background(), DownloadThreads); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot().DownloadThreads; got != 4 {
		t.Fatalf("clear did not fall back to env: %d", got)
	}
	raw, ok, err := st.GetSetting(context.Background(), string(DownloadThreads))
	if err != nil || ok || raw != "" {
		t.Fatalf("clear should delete setting: raw=%q ok=%v err=%v", raw, ok, err)
	}
}

func TestRuntimeRejectsInvalidBatchWithoutPublish(t *testing.T) {
	r, _ := testRuntime(t)
	before := r.View()
	if _, err := r.Apply(context.Background(), map[Key]int{DownloadThreads: 0, UploadThreads: 8}, nil); err == nil {
		t.Fatal("expected invalid value")
	}
	if after := r.View(); after != before {
		t.Fatalf("invalid batch changed view: before=%+v after=%+v", before, after)
	}
	if _, err := r.Apply(context.Background(), map[Key]int{DownloadThreads: 8}, []Key{DownloadThreads}); err == nil {
		t.Fatal("expected set/clear conflict")
	}
}

func TestRuntimeConcurrentReaders(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if i == 0 {
					_, _ = r.Set(ctx, UploadConnections, 1+n%16)
				} else if got := r.Snapshot().UploadConnections; got < 1 || got > 16 {
					t.Errorf("invalid snapshot: %d", got)
				}
			}
		}(i)
	}
	wg.Wait()
}
