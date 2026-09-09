package fact

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"
	"time"
)

// Observation is one recorded fact, already bounded and already typed. A
// projection reads Kind and takes the matching field; it never switches on Key,
// so it cannot acquire its own opinion about a value.
type Observation struct {
	Key  Key
	Kind Kind

	Text  string
	Int   int64
	Float float64
	Bool  bool
	List  []string

	// Truncated is true when the Definition's MaxEntries or MaxRunes bound was
	// hit. The projection turns it into the Definition's TruncationFlag.
	Truncated bool

	// Time is the moment of the write, and it is what the span events are built
	// out of: an event is anchored at the earliest of the facts belonging to it,
	// so request_info sits where the envelope parsed and retrieval_info where the
	// store answered. Stamping at projection time instead collapses every event
	// onto the span's end, silently — the total stays right and only the
	// breakdown becomes zeros.
	//
	// It moves on a re-observation, because the value does. See put.
	Time time.Time
}

// Record is one request's observed facts.
//
// Every method tolerates a nil receiver, and that is load-bearing rather than
// convenient: the probes chain in router.go allocates no record, so a panic in
// /healthz reaches logNack with none in context and must answer 500 rather than
// panic inside the recovery. The acceptance and dbtest suites call controllers
// with no middleware at all and rely on the same property.
//
// The mutex is real work, not ceremony: Record is exported and written from both
// controllers, the response writer and any middleware below Trace, and a handler
// that fans out across goroutines is a thing this type cannot prevent.
type Record struct {
	mutex    sync.Mutex
	observed []Observation
}

// recordKey is unexported, so no other package's context value can collide.
type recordKey struct{}

// New returns a context carrying a fresh Record, and the Record itself so the
// caller can read it back without a second lookup.
//
// Trace calls this unconditionally, including under OTEL_EXPORTER=none. Making
// allocation conditional on a live tracer is the tempting optimisation and it is
// the bug: the record's lifetime would then vary by environment variable.
func New(ctx context.Context) (context.Context, *Record) {
	record := &Record{}
	return context.WithValue(ctx, recordKey{}, record), record
}

// From returns the Record to fill, or nil when nothing above this point in the
// chain is collecting one. It does not allocate: a lookup that allocates makes
// the record's lifetime depend on who happened to look.
func From(ctx context.Context) *Record {
	if record, ok := ctx.Value(recordKey{}).(*Record); ok {
		return record
	}
	return nil
}

// The context forms, which are what a controller calls. The method forms exist
// for the two holders that already have the pointer: Trace, which allocated it,
// and responseRecorder, which records a status from WriteHeader.

// ObserveString records a KindString fact on the context's record, if there is
// one. Each of the five forwards to the method of the same name, where the
// behaviour is documented.
func ObserveString(ctx context.Context, k Key, v string) { From(ctx).ObserveString(k, v) }

// ObserveInt64 records a KindInt64 fact on the context's record, if there is one.
func ObserveInt64(ctx context.Context, k Key, v int64) { From(ctx).ObserveInt64(k, v) }

// ObserveFloat64 records a KindFloat64 fact on the context's record, if there is one.
func ObserveFloat64(ctx context.Context, k Key, v float64) { From(ctx).ObserveFloat64(k, v) }

// ObserveBool records a KindBool fact on the context's record, if there is one.
func ObserveBool(ctx context.Context, k Key, v bool) { From(ctx).ObserveBool(k, v) }

// ObserveStrings records a KindStrings fact on the context's record, if there is one.
func ObserveStrings(ctx context.Context, k Key, v []string) { From(ctx).ObserveStrings(k, v) }

// ObserveString records a KindString fact.
func (r *Record) ObserveString(k Key, v string) {
	def := requireKind(k, KindString)

	text, truncated := clampRunes(v, def.MaxRunes)
	r.put(Observation{Key: k, Kind: KindString, Text: text, Truncated: truncated})
}

// ObserveInt64 records a KindInt64 fact.
func (r *Record) ObserveInt64(k Key, v int64) {
	def := requireKind(k, KindInt64)
	if def.ZeroIsAbsent && v == 0 {
		return
	}
	r.put(Observation{Key: k, Kind: KindInt64, Int: v})
}

// ObserveFloat64 records a KindFloat64 fact.
func (r *Record) ObserveFloat64(k Key, v float64) {
	def := requireKind(k, KindFloat64)
	if def.ZeroIsAbsent && v == 0 {
		return
	}
	r.put(Observation{Key: k, Kind: KindFloat64, Float: v})
}

// ObserveBool records a KindBool fact.
func (r *Record) ObserveBool(k Key, v bool) {
	requireKind(k, KindBool)
	r.put(Observation{Key: k, Kind: KindBool, Bool: v})
}

// ObserveStrings records a KindStrings fact.
//
// An explicitly empty list is recorded rather than dropped: absent and empty are
// different answers. beckn.schemaContext absent means no schema predicate at all
// — the seeking-anything bucket, and a large one — where empty means a seeker
// who sent an empty array.
func (r *Record) ObserveStrings(k Key, v []string) {
	def := requireKind(k, KindStrings)

	list, truncated := clampEntries(v, def.MaxEntries)
	if def.MaxRunes > 0 {
		clamped := make([]string, len(list))
		for index, entry := range list {
			var cut bool
			clamped[index], cut = clampRunes(entry, def.MaxRunes)
			truncated = truncated || cut
		}
		list = clamped
	} else {
		list = slices.Clone(list)
	}
	if list == nil {
		// Clone of an empty non-nil slice returns nil, and nil would read as
		// "never observed" to a projection that checks for it.
		list = []string{}
	}

	r.put(Observation{Key: k, Kind: KindStrings, List: list, Truncated: truncated})
}

// Lookup returns one observation. Safe on a nil receiver.
func (r *Record) Lookup(k Key) (Observation, bool) {
	if r == nil {
		return Observation{}, false
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if index := r.indexOf(k); index >= 0 {
		return r.observed[index], true
	}
	return Observation{}, false
}

// All iterates what was observed, in the order it was first observed. Safe on a
// nil receiver, so a projection can run over a probe's absent record without
// asking first. It yields from a snapshot, so a slow consumer neither holds the
// lock nor deadlocks against an Observe on another goroutine.
func (r *Record) All() iter.Seq[Observation] {
	return func(yield func(Observation) bool) {
		if r == nil {
			return
		}
		r.mutex.Lock()
		snapshot := slices.Clone(r.observed)
		r.mutex.Unlock()

		for _, observation := range snapshot {
			if !yield(observation) {
				return
			}
		}
	}
}

// put stores an observation, replacing any earlier one for the same key.
//
// Replacing rather than appending is what WriteHeader needs: it can fire twice
// on a response the handler started and then faulted on, and the status the span
// reports has to be the one that went out. The stamp is replaced with it, and is
// taken here rather than in the five Observe methods — one clock read per write.
func (r *Record) put(observation Observation) {
	if r == nil {
		return
	}
	observation.Time = time.Now()

	r.mutex.Lock()
	defer r.mutex.Unlock()

	if index := r.indexOf(observation.Key); index >= 0 {
		r.observed[index] = observation
		return
	}
	r.observed = append(r.observed, observation)
}

// indexOf is a linear scan, which is the right shape for the twenty-odd facts a
// request records: an array indexed by numKeys would cost a per-request
// allocation the size of the whole table to hold mostly nothing. The caller
// holds the mutex.
func (r *Record) indexOf(k Key) int {
	return slices.IndexFunc(r.observed, func(o Observation) bool { return o.Key == k })
}

// requireKind panics when a key is observed as the wrong type.
//
// A panic is safe on a request path here because the mistake is not
// data-dependent: ObserveString(ResultCatalogCount, …) is wrong for every
// request, so it fails on the first test that walks the path and never first in
// production. Dropping the fact instead is found by whoever queries for the
// attribute that is missing.
//
// It runs BEFORE any nil-receiver check: nil-tolerance is for a missing record,
// not for a wrong call.
func requireKind(k Key, kind Kind) Definition {
	def := Of(k)
	if def.Kind != kind {
		panic(fmt.Sprintf("fact: %s is %v and was observed as %v", def.Name, def.Kind, kind))
	}
	return def
}

// clampRunes cuts a string to at most limit runes, reporting whether it cut.
// Runes rather than bytes: a byte cut can split a UTF-8 sequence and produce a
// replacement character in an attribute value nobody can search for. It happens
// here, where the value enters the record — one entrance, four exits.
func clampRunes(value string, limit int) (string, bool) {
	if limit <= 0 || len(value) <= limit {
		// len is a byte count and so a cheap lower bound on the rune count; a
		// string shorter in bytes than the limit cannot exceed it in runes.
		return value, false
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

// clampEntries cuts a list to at most limit entries, reporting whether it cut.
func clampEntries(values []string, limit int) ([]string, bool) {
	if limit <= 0 || len(values) <= limit {
		return values, false
	}
	return slices.Clip(values[:limit]), true
}
