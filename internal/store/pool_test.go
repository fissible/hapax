package store_test

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/store"
)

// The distractor pool is other people's writing. The privacy invariant already
// forbids its prose; this artifact goes further and keeps NO path
// representation at all — not a path, not a commitment to one — because a
// filename can carry a name, a correspondent or a publication, and the author's
// corpus is the only thing Hapax is the system of record for.
//
// What the pool needs to do is say it CHANGED. Sorted admitted-content hashes do
// that, are location-independent, and can be revalidated against the files
// themselves. What they cannot do is say which member a segment came from,
// which a population does not need.
func TestAPoolIsMembershipWithoutProvenance(t *testing.T) {
	s := newStore(t)
	pool := poolFixture(hashA, hashB)
	if err := s.PutDistractorPool(ctx(), pool); err != nil {
		t.Fatalf("PutDistractorPool: %v", err)
	}

	read, err := s.LoadDistractorPool(ctx(), pool.ID)
	if err != nil {
		t.Fatalf("LoadDistractorPool: %v", err)
	}
	if read.ID != pool.ID || read.PolicyDigest != pool.PolicyDigest {
		t.Errorf("identity did not survive: %+v", read)
	}
	if read.Members != len(pool.ContentHashes) {
		t.Errorf("members = %d, want %d", read.Members, len(pool.ContentHashes))
	}
	if len(read.ContentHashes) != len(pool.ContentHashes) {
		t.Fatalf("%d hashes came back, %d went in", len(read.ContentHashes), len(pool.ContentHashes))
	}
	// Compared as a SET, sorted: the order a pool reads back in is its own and
	// is asserted separately below. Requiring the caller's order here as well
	// would be two contracts for one method.
	want := append([]string(nil), pool.ContentHashes...)
	sort.Strings(want)
	if !reflect.DeepEqual(read.ContentHashes, want) {
		t.Errorf("membership = %v, want %v", read.ContentHashes, want)
	}
}

// The schema itself is the guarantee, not the writer's discipline: a column that
// could hold a path is one a later slice fills in. Driven from the database so
// it cannot go stale against the code.
func TestThePoolSchemaHasNowhereToPutAPath(t *testing.T) {
	s := newStore(t)
	raw := openRaw(t, s)
	for _, table := range []string{"distractor_pool", "distractor_pool_member"} {
		rows, err := raw.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatalf("columns of %s: %v", table, err)
		}
		found := 0
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan: %v", err)
			}
			found++
			// Every column is an identity, a digest, a count or a timestamp.
			// Anything named for a location or a name is the thing this refuses.
			for _, forbidden := range []string{"path", "name", "file", "title", "author", "source", "url"} {
				if strings.Contains(name, forbidden) {
					t.Errorf("%s.%s could hold a location or a person", table, name)
				}
			}
		}
		rows.Close()
		if found == 0 {
			t.Fatalf("%s has no columns; this check is vacuous", table)
		}
	}
}

// A pool is content-addressed, so writing the same one twice is a replay and
// writing a different one at the same identity is a conflict — the same rule
// every other artifact here follows.
func TestRewritingAPoolIsAReplayOrAConflict(t *testing.T) {
	s := newStore(t)
	pool := poolFixture(hashA, hashB)
	if err := s.PutDistractorPool(ctx(), pool); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.PutDistractorPool(ctx(), pool); err != nil {
		t.Errorf("an identical rewrite was refused: %v", err)
	}

	different := pool
	different.ContentHashes = []string{hashA}
	if err := s.PutDistractorPool(ctx(), different); !errors.Is(err, store.ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

// Membership is a multiset in nothing but name: duplicate admitted content is
// refused, because the content hash is the per-document clustering key the
// bootstrap groups by, and two members sharing one would be one cluster
// pretending to be two.
func TestAPoolRefusesTheSameMemberTwice(t *testing.T) {
	s := newStore(t)
	pool := poolFixture(hashA, hashA)
	if err := s.PutDistractorPool(ctx(), pool); err == nil {
		t.Error("accepted a pool holding the same content twice")
	}
}

// And the order is the pool's, not the writer's: identity is over SORTED
// hashes, so a pool assembled in a different order is the same pool.
func TestAPoolReadsBackInTheOrderItsIdentityIsOver(t *testing.T) {
	s := newStore(t)
	ordered, reversed := poolFixture(hashA, hashB), poolFixture(hashB, hashA)
	if ordered.ID != reversed.ID {
		t.Fatalf("the fixture gives two identities to one pool: %q and %q", ordered.ID, reversed.ID)
	}
	if err := s.PutDistractorPool(ctx(), reversed); err != nil {
		t.Fatalf("PutDistractorPool: %v", err)
	}
	read, err := s.LoadDistractorPool(ctx(), reversed.ID)
	if err != nil {
		t.Fatalf("LoadDistractorPool: %v", err)
	}
	for i := 1; i < len(read.ContentHashes); i++ {
		if read.ContentHashes[i-1] >= read.ContentHashes[i] {
			t.Errorf("hashes came back unsorted: %v", read.ContentHashes)
			break
		}
	}
}

func TestAPoolThatIsNotThereIsNotFound(t *testing.T) {
	if _, err := newStore(t).LoadDistractorPool(ctx(), hashA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// poolFixture derives the identity the way the pool declares it, so a fixture
// cannot hand the store an ID unrelated to what it contains.
func poolFixture(hashes ...string) store.DistractorPool {
	return store.DistractorPool{
		ID:            store.DistractorPoolID(poolPolicy, hashes),
		PolicyDigest:  poolPolicy,
		ContentHashes: hashes,
	}
}

var poolPolicy = fakeID("policy", "distractors")
