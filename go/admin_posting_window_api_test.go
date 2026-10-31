package isureal

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// SQL の `$4 || substr(gen_random_uuid()::text, 16)` と同じ組み立てで、UUID v7 になることを確かめる。
func TestNotificationIDPrefixMakesUUIDv7(t *testing.T) {
	startsAt := time.Date(2026, 10, 31, 3, 47, 20, 730_719_000, time.UTC)
	prev := ""
	for i := range 1000 {
		at := startsAt.Add(time.Duration(i) * 7 * time.Millisecond)
		id := notificationIDPrefix(at) + uuid.NewString()[15:]
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("uuid.Parse(%q): %v", id, err)
		}
		if parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
			t.Fatalf("%q: version %d, variant %v", id, parsed.Version(), parsed.Variant())
		}
		if sec, nsec := parsed.Time().UnixTime(); time.Unix(sec, nsec).UnixMilli() != at.UnixMilli() {
			t.Fatalf("%q: time %v, want %v", id, time.Unix(sec, nsec).UTC(), at)
		}
		if len(id) != 36 || id <= prev {
			t.Fatalf("%q is not after %q", id, prev)
		}
		prev = id
	}
}
