// Package transfercfg provides an atomic, database-backed runtime snapshot for
// media transfer concurrency settings.
package transfercfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

const (
	// MinValue and MaxValue are the inclusive bounds for every transfer setting.
	MinValue = 1
	MaxValue = 16
)

// Key identifies one configurable transfer limit.
type Key string

const (
	DownloadThreads     Key = "download_threads"
	UploadThreads       Key = "upload_threads"
	DownloadConnections Key = "download_connections"
	UploadConnections   Key = "upload_connections"
)

var allKeys = [...]Key{DownloadThreads, UploadThreads, DownloadConnections, UploadConnections}

// Snapshot is an immutable effective transfer configuration.
type Snapshot struct {
	DownloadThreads     int
	UploadThreads       int
	DownloadConnections int
	UploadConnections   int
}

// Field describes one effective value and its immutable environment default.
type Field struct {
	Effective  int
	EnvDefault int
	Overridden bool
}

// View is a consistent configuration view suitable for an API response or
// audit snapshot. All four fields come from the same atomic publication.
type View struct {
	DownloadThreads     Field
	UploadThreads       Field
	DownloadConnections Field
	UploadConnections   Field
}

// Change is the result of a successful Apply operation.
type Change struct {
	Before  View
	After   View
	Changed bool
}

// Update errors are stable classifications for callers such as the Web API.
var (
	ErrUnknownKey       = errors.New("transfercfg: unknown setting")
	ErrInvalidValue     = errors.New("transfercfg: value must be an integer from 1 to 16")
	ErrSetClearConflict = errors.New("transfercfg: setting cannot be set and cleared together")
)

type state struct {
	snapshot Snapshot
	override [4]bool
}

// Runtime owns the one process-wide transfer configuration snapshot.
type Runtime struct {
	st       *store.Store
	defaults Snapshot
	mu       sync.Mutex
	current  atomic.Pointer[state]
}

// New creates a Runtime, loading valid per-key database overrides over defaults.
// Missing or malformed/out-of-range override values are ignored and fall back
// to the environment defaults; database read failures are returned.
func New(ctx context.Context, st *store.Store, defaults Snapshot) (*Runtime, error) {
	if st == nil {
		return nil, errors.New("transfercfg: Store is required")
	}
	if err := validateSnapshot(defaults); err != nil {
		return nil, fmt.Errorf("transfercfg: invalid defaults: %w", err)
	}
	r := &Runtime{st: st, defaults: defaults}
	cur := state{snapshot: defaults}
	for i, key := range allKeys {
		raw, ok, err := st.GetSetting(ctx, string(key))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var value int
		if json.Unmarshal([]byte(raw), &value) != nil || !validValue(value) {
			continue
		}
		cur.snapshot = setSnapshot(cur.snapshot, key, value)
		cur.override[i] = true
	}
	r.current.Store(&cur)
	return r, nil
}

// NewFromConfig constructs a Runtime from the four transfer defaults in cfg.
func NewFromConfig(ctx context.Context, st *store.Store, cfg config.Config) (*Runtime, error) {
	return New(ctx, st, Snapshot{
		DownloadThreads:     cfg.DownloadThreads,
		UploadThreads:       cfg.UploadThreads,
		DownloadConnections: cfg.DownloadConnections,
		UploadConnections:   cfg.UploadConnections,
	})
}

// Snapshot returns the current effective values without database I/O.
func (r *Runtime) Snapshot() Snapshot { return r.current.Load().snapshot }

// Current is an alias for Snapshot, useful for injected transfer consumers.
func (r *Runtime) Current() Snapshot { return r.Snapshot() }

// View returns one consistent effective/default/source snapshot.
func (r *Runtime) View() View {
	s := r.current.Load()
	return viewOf(r.defaults, s)
}

// Apply validates, persists, and atomically publishes a set/clear batch. The
// maps are copied before locking, and no in-memory state changes on failure.
func (r *Runtime) Apply(ctx context.Context, set map[Key]int, clear []Key) (Change, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	beforeState := r.current.Load()
	before := viewOf(r.defaults, beforeState)
	clearSet := make(map[Key]struct{}, len(clear))
	for _, key := range clear {
		if !isKnown(key) {
			return Change{Before: before, After: before}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
		if _, exists := clearSet[key]; exists {
			continue
		}
		clearSet[key] = struct{}{}
	}
	for key, value := range set {
		if !isKnown(key) {
			return Change{Before: before, After: before}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
		if _, exists := clearSet[key]; exists {
			return Change{Before: before, After: before}, fmt.Errorf("%w: %q", ErrSetClearConflict, key)
		}
		if !validValue(value) {
			return Change{Before: before, After: before}, fmt.Errorf("%w: %q=%d", ErrInvalidValue, key, value)
		}
	}

	next := *beforeState
	for key, value := range set {
		next.snapshot = setSnapshot(next.snapshot, key, value)
		next.override[indexOf(key)] = true
	}
	for key := range clearSet {
		next.snapshot = setSnapshot(next.snapshot, key, defaultValue(r.defaults, key))
		next.override[indexOf(key)] = false
	}
	after := viewOf(r.defaults, &next)
	if before == after {
		return Change{Before: before, After: after}, nil
	}

	if err := r.st.Tx(ctx, func(tx *store.Store) error {
		for key, value := range set {
			raw, _ := json.Marshal(value)
			if err := tx.SetSetting(ctx, string(key), string(raw)); err != nil {
				return err
			}
		}
		for key := range clearSet {
			if err := tx.DeleteSetting(ctx, string(key)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return Change{Before: before, After: before}, err
	}
	r.current.Store(&next)
	return Change{Before: before, After: after, Changed: true}, nil
}

// Set persists one database override and publishes it atomically.
func (r *Runtime) Set(ctx context.Context, key Key, value int) (Change, error) {
	return r.Apply(ctx, map[Key]int{key: value}, nil)
}

// Clear removes one database override and falls back to the environment value.
func (r *Runtime) Clear(ctx context.Context, key Key) (Change, error) {
	return r.Apply(ctx, nil, []Key{key})
}

// ValidateKey returns whether key names a supported setting.
func ValidateKey(key Key) bool { return isKnown(key) }

// ValidateValue returns whether value is in the supported range.
func ValidateValue(value int) bool { return validValue(value) }

func viewOf(defaults Snapshot, s *state) View {
	return View{
		DownloadThreads:     Field{s.snapshot.DownloadThreads, defaults.DownloadThreads, s.override[0]},
		UploadThreads:       Field{s.snapshot.UploadThreads, defaults.UploadThreads, s.override[1]},
		DownloadConnections: Field{s.snapshot.DownloadConnections, defaults.DownloadConnections, s.override[2]},
		UploadConnections:   Field{s.snapshot.UploadConnections, defaults.UploadConnections, s.override[3]},
	}
}

func validateSnapshot(s Snapshot) error {
	for _, value := range []int{s.DownloadThreads, s.UploadThreads, s.DownloadConnections, s.UploadConnections} {
		if !validValue(value) {
			return ErrInvalidValue
		}
	}
	return nil
}

func validValue(value int) bool { return value >= MinValue && value <= MaxValue }

func isKnown(key Key) bool {
	for _, known := range allKeys {
		if key == known {
			return true
		}
	}
	return false
}

func indexOf(key Key) int {
	for i, known := range allKeys {
		if key == known {
			return i
		}
	}
	return -1
}

func defaultValue(s Snapshot, key Key) int { return getSnapshot(s, key) }

func getSnapshot(s Snapshot, key Key) int {
	switch key {
	case DownloadThreads:
		return s.DownloadThreads
	case UploadThreads:
		return s.UploadThreads
	case DownloadConnections:
		return s.DownloadConnections
	case UploadConnections:
		return s.UploadConnections
	default:
		return 0
	}
}

func setSnapshot(s Snapshot, key Key, value int) Snapshot {
	switch key {
	case DownloadThreads:
		s.DownloadThreads = value
	case UploadThreads:
		s.UploadThreads = value
	case DownloadConnections:
		s.DownloadConnections = value
	case UploadConnections:
		s.UploadConnections = value
	}
	return s
}
