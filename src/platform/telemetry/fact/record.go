package fact

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"
)

// Observation is one recorded fact, already bounded and already typed. A
// projection reads Kind and takes the matching field; it never switches on Key,
// which is what stops a projection from acquiring its own opinion about a value.
type Observation struct {
	Key  Key
	Kind Kind

	Text  string
	Int   int64
	Float float64
	Bool  bool
	List  []string

	// Truncated is true when the Definition's MaxEntries or MaxRunes bit. The
	// projection turns it into the Definition's TruncationFlag; a value silently
	// cut short reads as the value the caller sent.
	Truncated bool
}

// Record is one request's observed facts.
//
// Every method tolerates a nil receiver, and that is load-bearing rather than
// convenient. The probes chain (router.go:158-163) mounts RequestID + Recover
// only and allocates no record — deliberately, because a probe every few
// seconds carrying eid: API would be most of the stream and none of the signal
// — so a panic in /healthz reaches logNack with no record in context and must
// answer 500 rather than panic a second time inside the recovery that was
// answering the first. The acceptance and dbtest suites call controllers with
// no middleware at all and rely on the same property.
//
// Unlike middlewares.correlation, which documents that it needs no
// synchronisation, this holds a mutex. The difference is real: correlation is
// unexported, written from exactly one middleware and read after everything
// below it has returned, whereas Record is exported and written from both
// controllers, the response writer and any middleware below Trace. A handler
// that fans out across goroutines is a thing this type cannot prevent, and an
// uncontended mutex costs less than the race it removes.
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
// the bug: RequestLogger would then allocate on some deployments and adopt on
// others, so the record's lifetime would vary by environment variable.
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

// The context forms. These are what a controller calls; the method forms exist
// for the two holders that already have the pointer — Trace, which allocated
// it, and responseRecorder, which is handed it because WriteHeader records a
// status through a record it holds rather than a context it does not.
//
// The AST walk in 23c matches the selector regardless of which form is used,
// because both spell the key as a fact.Key and neither can spell it as a string.

// ObserveString records a KindString fact on the context's record, if there is
// one. Each of the five is a one-line forward to the method of the same name,
// where the behaviour and its reasons are documented.
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
// different answers. beckn.schemaContext absent means no schema predicate at
// all — every capability matches, which is the seeking-anything bucket and
// likely a large one — and empty means a seeker who sent an empty array.
// Collapsing them loses the larger of the two.
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
// nil receiver, which is what lets a projection run over a probe's absent
// record without asking first.
//
// It yields from a snapshot so a projection cannot deadlock against an Observe
// on another goroutine, and so a slow consumer does not hold the lock.
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
// Replacing rather than appending is what WriteHeader needs: it can fire more
// than once on a response the handler started writing and then faulted on, and
// the status the span reports has to be the one that went out.
func (r *Record) put(observation Observation) {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if index := r.indexOf(observation.Key); index >= 0 {
		r.observed[index] = observation
		return
	}
	r.observed = append(r.observed, observation)
}

// indexOf is a linear scan, and that is the right shape here: a request records
// on the order of twenty facts, so a scan beats an array indexed by every one of
// numKeys, which would cost a per-request allocation the size of the whole table
// to hold mostly nothing. The caller holds the mutex.
func (r *Record) indexOf(k Key) int {
	return slices.IndexFunc(r.observed, func(o Observation) bool { return o.Key == k })
}

// requireKind panics when a key is observed as the wrong type.
//
// Loudly, per telemetry-seam.md, and the reason a panic is safe on a request
// path is that the mistake is not data-dependent: ObserveString(ResultCatalogCount, …)
// is wrong for every request, so it fails on the first test that walks the path
// and never first in production. The alternative — dropping the fact — is found
// by whoever queries for the attribute that is missing, which is the failure
// mode this table exists to end.
//
// It runs BEFORE any nil-receiver check, so a mismatch written on the probes
// chain still fails rather than being swallowed by nil-tolerance. Nil-tolerance
// is for a missing record, not for a wrong call.
func requireKind(k Key, kind Kind) Definition {
	def := Of(k)
	if def.Kind != kind {
		panic(fmt.Sprintf("fact: %s is %v and was observed as %v", def.Name, def.Kind, kind))
	}
	return def
}
