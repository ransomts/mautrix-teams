package teamsdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	// Registers the "sqlite3-fk-wal" driver (a mattn/go-sqlite3 wrapper) that
	// the bridge itself uses at runtime, so no extra go.mod dependency is needed.
	_ "go.mau.fi/util/dbutil/litestream"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const testBridgeID networkid.BridgeID = "teams-test"

// newRawTestDB opens a fresh temp-file sqlite database. The file lives in
// t.TempDir(), so it is removed automatically when the test finishes.
func newRawTestDB(t *testing.T) *dbutil.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "teams.db")
	raw, err := dbutil.NewWithDialect("file:"+path+"?_txlock=immediate", "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return raw
}

// newTestDBWithBridge wraps raw in a teamsdb.Database for bridgeID and runs
// the schema upgrades.
func newTestDBWithBridge(t *testing.T, raw *dbutil.Database, bridgeID networkid.BridgeID) *Database {
	t.Helper()
	db := New(bridgeID, raw, zerolog.Nop())
	if err := db.Upgrade(context.Background()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	return db
}

// newTestDB opens a temp-file sqlite database, runs Upgrade, and closes it
// on cleanup.
func newTestDB(t *testing.T) *Database {
	t.Helper()
	return newTestDBWithBridge(t, newRawTestDB(t), testBridgeID)
}

// assertProfileMissing checks the result of looking up a profile that does
// not exist: (nil, nil), matching ThreadStateQuery.Get and
// ConsumptionHorizonQuery.Get, which is what callers in pkg/connector rely on.
func assertProfileMissing(t *testing.T, what string, got *Profile, err error) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %+v, want nil", what, got)
	}
	if err != nil {
		t.Errorf("%s err = %v, want nil", what, err)
	}
}

func TestNew(t *testing.T) {
	db := newTestDB(t)
	if db.Database == nil {
		t.Fatal("embedded dbutil.Database is nil")
	}
	if db.Database.VersionTable != "teams_version" {
		t.Errorf("version table = %q, want teams_version", db.Database.VersionTable)
	}
	for name, id := range map[string]networkid.BridgeID{
		"ThreadState":        db.ThreadState.BridgeID,
		"Profile":            db.Profile.BridgeID,
		"ConsumptionHorizon": db.ConsumptionHorizon.BridgeID,
	} {
		if id != testBridgeID {
			t.Errorf("%s.BridgeID = %q, want %q", name, id, testBridgeID)
		}
	}
}

func TestUpgradeIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// Insert a row so we can verify a second Upgrade does not wipe data.
	if err := db.Profile.Upsert(ctx, "user-1", "Alice", time.UnixMilli(1000)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	// A brand-new wrapper over the same connection must also be a no-op.
	again := New(testBridgeID, db.Database, zerolog.Nop())
	if err := again.Upgrade(ctx); err != nil {
		t.Fatalf("third upgrade via new wrapper: %v", err)
	}

	var version int
	if err := db.QueryRow(ctx, "SELECT version FROM teams_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	p, err := db.Profile.GetByTeamsUserID(ctx, "user-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p == nil || p.DisplayName != "Alice" {
		t.Errorf("profile after re-upgrade = %+v, want Alice", p)
	}

	for _, table := range []string{"teams_thread_state", "teams_profile", "teams_consumption_horizon_state"} {
		var n int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=$1", table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after upgrade", table)
		}
	}
}

func TestThreadStateMissingDB(t *testing.T) {
	ctx := context.Background()
	var q *ThreadStateQuery
	if err := q.Upsert(ctx, &ThreadState{}); err != errMissingDB {
		t.Errorf("nil query Upsert err = %v, want errMissingDB", err)
	}
	q = &ThreadStateQuery{BridgeID: testBridgeID}
	if _, err := q.Get(ctx, "login", "t"); err != errMissingDB {
		t.Errorf("Get without DB err = %v, want errMissingDB", err)
	}
	if _, err := q.ListForLogin(ctx, "login"); err != errMissingDB {
		t.Errorf("ListForLogin without DB err = %v, want errMissingDB", err)
	}
	if err := q.UpdateCursor(ctx, "login", "t", "1", 1); err != errMissingDB {
		t.Errorf("UpdateCursor without DB err = %v, want errMissingDB", err)
	}
}

func TestThreadStateUpsertValidation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	if err := db.ThreadState.Upsert(ctx, nil); err == nil {
		t.Error("Upsert(nil) should fail")
	}
	tests := []struct {
		name  string
		state *ThreadState
	}{
		{"empty thread", &ThreadState{UserLoginID: login, ThreadID: "  ", Conversation: "c"}},
		{"empty conversation", &ThreadState{UserLoginID: login, ThreadID: "t", Conversation: "\t"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.ThreadState.Upsert(ctx, tc.state); err == nil {
				t.Error("expected validation error")
			}
		})
	}
	if err := db.ThreadState.UpdateCursor(ctx, login, " ", "1", 1); err == nil {
		t.Error("UpdateCursor with blank thread id should fail")
	}
	got, err := db.ThreadState.Get(ctx, login, "   ")
	if err != nil || got != nil {
		t.Errorf("Get(blank) = %+v, %v; want nil, nil", got, err)
	}
}

func TestThreadStateUpsertGet(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	got, err := db.ThreadState.Get(ctx, login, "thread-1")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("get missing = %+v, want nil", got)
	}

	// ThreadID / Conversation are trimmed before storage.
	in := &ThreadState{
		UserLoginID:    login,
		ThreadID:       "  thread-1 ",
		Conversation:   " 19:abc@thread.v2 ",
		IsOneToOne:     true,
		Name:           "Chat with Bob",
		LastSequenceID: "10",
		LastMessageTS:  1234,
	}
	if err := db.ThreadState.Upsert(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if in.ThreadID != "thread-1" || in.Conversation != "19:abc@thread.v2" {
		t.Errorf("Upsert did not trim input in place: %+v", in)
	}

	got, err = db.ThreadState.Get(ctx, login, "thread-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := &ThreadState{
		BridgeID:       testBridgeID,
		UserLoginID:    login,
		ThreadID:       "thread-1",
		Conversation:   "19:abc@thread.v2",
		IsOneToOne:     true,
		Name:           "Chat with Bob",
		LastSequenceID: "10",
		LastMessageTS:  1234,
	}
	if *got != *want {
		t.Errorf("get = %+v, want %+v", *got, *want)
	}

	// Get also trims the lookup key.
	got, err = db.ThreadState.Get(ctx, login, " thread-1\n")
	if err != nil || got == nil {
		t.Fatalf("get with padded id = %+v, %v", got, err)
	}
}

// Upsert on an existing row updates conversation/is_one_to_one/name but
// deliberately leaves the cursor columns alone; that is what the SQL's
// ON CONFLICT clause does and callers rely on it.
func TestThreadStateUpsertPreservesCursor(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	first := &ThreadState{
		UserLoginID: login, ThreadID: "t", Conversation: "c1",
		IsOneToOne: false, Name: "Old", LastSequenceID: "50", LastMessageTS: 5000,
	}
	if err := db.ThreadState.Upsert(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := &ThreadState{
		UserLoginID: login, ThreadID: "t", Conversation: "c2",
		IsOneToOne: true, Name: "New", LastSequenceID: "1", LastMessageTS: 1,
	}
	if err := db.ThreadState.Upsert(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := db.ThreadState.Get(ctx, login, "t")
	if err != nil || got == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got.Conversation != "c2" || !got.IsOneToOne || got.Name != "New" {
		t.Errorf("metadata not updated: %+v", got)
	}
	if got.LastSequenceID != "50" || got.LastMessageTS != 5000 {
		t.Errorf("cursor changed by Upsert: seq=%q ts=%d, want 50/5000", got.LastSequenceID, got.LastMessageTS)
	}

	var count int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM teams_thread_state").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1", count)
	}
}

// UpdateCursor is an unconditional overwrite: it does not guard against
// moving backwards. Assert the actual behaviour so a future change is
// deliberate.
func TestThreadStateUpdateCursor(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	// Updating a row that does not exist is a silent no-op.
	if err := db.ThreadState.UpdateCursor(ctx, login, "nope", "1", 1); err != nil {
		t.Fatalf("update missing: %v", err)
	}
	if got, _ := db.ThreadState.Get(ctx, login, "nope"); got != nil {
		t.Errorf("UpdateCursor must not create rows, got %+v", got)
	}

	if err := db.ThreadState.Upsert(ctx, &ThreadState{
		UserLoginID: login, ThreadID: "t", Conversation: "c", Name: "n",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	steps := []struct {
		seq string
		ts  int64
	}{
		{"10", 1000},
		{"20", 2000},
		{"5", 500}, // backwards: still applied
		{"", 0},    // reset: still applied
	}
	for _, s := range steps {
		if err := db.ThreadState.UpdateCursor(ctx, login, " t ", s.seq, s.ts); err != nil {
			t.Fatalf("update cursor %q/%d: %v", s.seq, s.ts, err)
		}
		got, err := db.ThreadState.Get(ctx, login, "t")
		if err != nil || got == nil {
			t.Fatalf("get = %+v, %v", got, err)
		}
		if got.LastSequenceID != s.seq || got.LastMessageTS != s.ts {
			t.Errorf("after UpdateCursor(%q,%d): seq=%q ts=%d", s.seq, s.ts, got.LastSequenceID, got.LastMessageTS)
		}
		if got.Conversation != "c" || got.Name != "n" {
			t.Errorf("UpdateCursor touched metadata: %+v", got)
		}
	}
}

func TestThreadStateListForLogin(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	list, err := db.ThreadState.ListForLogin(ctx, login)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list empty = %d rows", len(list))
	}

	want := map[string]ThreadState{}
	for i, id := range []string{"t1", "t2", "t3"} {
		st := ThreadState{
			UserLoginID: login, ThreadID: id, Conversation: "conv-" + id,
			IsOneToOne: i%2 == 0, Name: "name-" + id,
			LastSequenceID: id + "-seq", LastMessageTS: int64(i) * 100,
		}
		if err := db.ThreadState.Upsert(ctx, &st); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		st.BridgeID = testBridgeID
		want[id] = st
	}

	list, err = db.ThreadState.ListForLogin(ctx, login)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != len(want) {
		t.Fatalf("list = %d rows, want %d", len(list), len(want))
	}
	seen := map[string]bool{}
	for _, st := range list {
		w, ok := want[st.ThreadID]
		if !ok {
			t.Errorf("unexpected thread %q", st.ThreadID)
			continue
		}
		if seen[st.ThreadID] {
			t.Errorf("thread %q listed twice", st.ThreadID)
		}
		seen[st.ThreadID] = true
		if *st != w {
			t.Errorf("thread %q = %+v, want %+v", st.ThreadID, *st, w)
		}
	}
}

func TestProfileMissingDB(t *testing.T) {
	ctx := context.Background()
	var q *ProfileQuery
	if _, err := q.GetByTeamsUserID(ctx, "u"); err != errMissingDB {
		t.Errorf("nil query Get err = %v, want errMissingDB", err)
	}
	if err := q.Upsert(ctx, "u", "n", time.Now()); err != errMissingDB {
		t.Errorf("nil query Upsert err = %v, want errMissingDB", err)
	}
}

func TestProfileUpsertGet(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	got, err := db.Profile.GetByTeamsUserID(ctx, "8:orgid:alice")
	assertProfileMissing(t, "get missing", got, err)
	got, err = db.Profile.GetByTeamsUserID(ctx, "  ")
	if err != nil || got != nil {
		t.Errorf("get blank = %+v, %v; want nil, nil", got, err)
	}

	// Blank ids/names are silently ignored, not stored.
	if err := db.Profile.Upsert(ctx, "  ", "Alice", time.Now()); err != nil {
		t.Errorf("upsert blank id: %v", err)
	}
	if err := db.Profile.Upsert(ctx, "8:orgid:alice", "  ", time.Now()); err != nil {
		t.Errorf("upsert blank name: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM teams_profile").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("blank upserts stored %d rows", count)
	}

	// Timestamps are stored as UTC millis; sub-millisecond precision and the
	// original location are dropped.
	seen1 := time.Date(2026, 9, 15, 12, 30, 45, 123_456_789, time.FixedZone("EST", -5*3600))
	if err := db.Profile.Upsert(ctx, " 8:orgid:alice ", " Alice Smith ", seen1); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = db.Profile.GetByTeamsUserID(ctx, "8:orgid:alice")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get = nil after upsert")
	}
	if got.BridgeID != testBridgeID || got.TeamsUserID != "8:orgid:alice" || got.DisplayName != "Alice Smith" {
		t.Errorf("profile = %+v", got)
	}
	if wantTS := seen1.UTC().Truncate(time.Millisecond); !got.LastSeenTS.Equal(wantTS) {
		t.Errorf("LastSeenTS = %v, want %v", got.LastSeenTS, wantTS)
	}
	if got.LastSeenTS.Location() != time.UTC {
		t.Errorf("LastSeenTS location = %v, want UTC", got.LastSeenTS.Location())
	}

	// Overwrite display name and timestamp.
	seen2 := seen1.Add(time.Hour)
	if err := db.Profile.Upsert(ctx, "8:orgid:alice", "Alice Jones", seen2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err = db.Profile.GetByTeamsUserID(ctx, "8:orgid:alice")
	if err != nil || got == nil {
		t.Fatalf("get after overwrite = %+v, %v", got, err)
	}
	if got.DisplayName != "Alice Jones" {
		t.Errorf("DisplayName = %q, want Alice Jones", got.DisplayName)
	}
	if wantTS := seen2.UTC().Truncate(time.Millisecond); !got.LastSeenTS.Equal(wantTS) {
		t.Errorf("LastSeenTS = %v, want %v", got.LastSeenTS, wantTS)
	}
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM teams_profile").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("row count after overwrite = %d, want 1", count)
	}
}

func TestConsumptionHorizonMissingDB(t *testing.T) {
	ctx := context.Background()
	var q *ConsumptionHorizonQuery
	if _, err := q.Get(ctx, "l", "t", "u"); err != errMissingDB {
		t.Errorf("nil query Get err = %v, want errMissingDB", err)
	}
	if err := q.UpsertLastRead(ctx, "l", "t", "u", 1); err != errMissingDB {
		t.Errorf("nil query Upsert err = %v, want errMissingDB", err)
	}
}

func TestConsumptionHorizon(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const login networkid.UserLoginID = "login-a"

	got, err := db.ConsumptionHorizon.Get(ctx, login, "thread", "user")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("get missing = %+v, want nil", got)
	}
	for _, blank := range [][2]string{{"", "user"}, {"thread", " "}} {
		got, err := db.ConsumptionHorizon.Get(ctx, login, blank[0], blank[1])
		if err != nil || got != nil {
			t.Errorf("get blank %q/%q = %+v, %v; want nil, nil", blank[0], blank[1], got, err)
		}
		if err := db.ConsumptionHorizon.UpsertLastRead(ctx, login, blank[0], blank[1], 1); err == nil {
			t.Errorf("upsert blank %q/%q should fail", blank[0], blank[1])
		}
	}

	if err := db.ConsumptionHorizon.UpsertLastRead(ctx, login, " thread ", " user ", 1000); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = db.ConsumptionHorizon.Get(ctx, login, "thread", "user")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := &ConsumptionHorizonState{
		BridgeID: testBridgeID, UserLoginID: login,
		ThreadID: "thread", TeamsUserID: "user", LastReadTS: 1000,
	}
	if got == nil || *got != *want {
		t.Errorf("get = %+v, want %+v", got, want)
	}

	// A later upsert replaces the value, including moving backwards.
	for _, ts := range []int64{2000, 500} {
		if err := db.ConsumptionHorizon.UpsertLastRead(ctx, login, "thread", "user", ts); err != nil {
			t.Fatalf("upsert %d: %v", ts, err)
		}
		got, err = db.ConsumptionHorizon.Get(ctx, login, "thread", "user")
		if err != nil || got == nil {
			t.Fatalf("get after %d = %+v, %v", ts, got, err)
		}
		if got.LastReadTS != ts {
			t.Errorf("LastReadTS = %d, want %d", got.LastReadTS, ts)
		}
	}

	// Different teams users in the same thread are separate rows.
	if err := db.ConsumptionHorizon.UpsertLastRead(ctx, login, "thread", "other", 42); err != nil {
		t.Fatalf("upsert other: %v", err)
	}
	got, err = db.ConsumptionHorizon.Get(ctx, login, "thread", "user")
	if err != nil || got == nil || got.LastReadTS != 500 {
		t.Errorf("first user's horizon disturbed: %+v, %v", got, err)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM teams_consumption_horizon_state").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("row count = %d, want 2", count)
	}
}

// Rows are keyed by user login: two logins on the same bridge never see
// each other's thread state or consumption horizons.
func TestScopedPerUserLogin(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const a, b networkid.UserLoginID = "login-a", "login-b"

	for _, login := range []networkid.UserLoginID{a, b} {
		if err := db.ThreadState.Upsert(ctx, &ThreadState{
			UserLoginID: login, ThreadID: "shared", Conversation: "conv-" + string(login), Name: string(login),
		}); err != nil {
			t.Fatalf("upsert %s: %v", login, err)
		}
		if err := db.ThreadState.Upsert(ctx, &ThreadState{
			UserLoginID: login, ThreadID: "only-" + string(login), Conversation: "c", Name: "n",
		}); err != nil {
			t.Fatalf("upsert %s: %v", login, err)
		}
	}
	if err := db.ThreadState.UpdateCursor(ctx, a, "shared", "a-seq", 111); err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumptionHorizon.UpsertLastRead(ctx, a, "shared", "user", 999); err != nil {
		t.Fatal(err)
	}

	// Login A sees its own data.
	gotA, err := db.ThreadState.Get(ctx, a, "shared")
	if err != nil || gotA == nil {
		t.Fatalf("get a = %+v, %v", gotA, err)
	}
	if gotA.Conversation != "conv-login-a" || gotA.LastSequenceID != "a-seq" || gotA.LastMessageTS != 111 {
		t.Errorf("login a row = %+v", gotA)
	}

	// Login B has the same thread id but its own row, untouched by A's cursor.
	gotB, err := db.ThreadState.Get(ctx, b, "shared")
	if err != nil || gotB == nil {
		t.Fatalf("get b = %+v, %v", gotB, err)
	}
	if gotB.Conversation != "conv-login-b" || gotB.LastSequenceID != "" || gotB.LastMessageTS != 0 {
		t.Errorf("login b row leaked login a's data: %+v", gotB)
	}
	if got, _ := db.ThreadState.Get(ctx, b, "only-login-a"); got != nil {
		t.Errorf("login b can see login a's private thread: %+v", got)
	}
	if got, _ := db.ThreadState.Get(ctx, "login-c", "shared"); got != nil {
		t.Errorf("unknown login sees rows: %+v", got)
	}

	for _, tc := range []struct {
		login networkid.UserLoginID
		want  map[string]bool
	}{
		{a, map[string]bool{"shared": true, "only-login-a": true}},
		{b, map[string]bool{"shared": true, "only-login-b": true}},
		{"login-c", map[string]bool{}},
	} {
		list, err := db.ThreadState.ListForLogin(ctx, tc.login)
		if err != nil {
			t.Fatalf("list %s: %v", tc.login, err)
		}
		gotIDs := map[string]bool{}
		for _, st := range list {
			if st.UserLoginID != tc.login {
				t.Errorf("list %s returned row for %s", tc.login, st.UserLoginID)
			}
			gotIDs[st.ThreadID] = true
		}
		if len(gotIDs) != len(tc.want) {
			t.Errorf("list %s = %v, want %v", tc.login, gotIDs, tc.want)
			continue
		}
		for id := range tc.want {
			if !gotIDs[id] {
				t.Errorf("list %s missing %q", tc.login, id)
			}
		}
	}

	if got, err := db.ConsumptionHorizon.Get(ctx, b, "shared", "user"); err != nil || got != nil {
		t.Errorf("login b sees login a's horizon: %+v, %v", got, err)
	}
	if got, err := db.ConsumptionHorizon.Get(ctx, a, "shared", "user"); err != nil || got == nil || got.LastReadTS != 999 {
		t.Errorf("login a horizon = %+v, %v", got, err)
	}
}

// Rows are also keyed by bridge id: two Database wrappers with different
// bridge ids over the same connection are fully isolated, including profiles
// (which have no login dimension).
func TestScopedPerBridgeID(t *testing.T) {
	ctx := context.Background()
	raw := newRawTestDB(t)
	db1 := newTestDBWithBridge(t, raw, "bridge-1")
	db2 := newTestDBWithBridge(t, raw, "bridge-2")
	const login networkid.UserLoginID = "login"

	if err := db1.Profile.Upsert(ctx, "user", "From Bridge 1", time.UnixMilli(1)); err != nil {
		t.Fatal(err)
	}
	if err := db1.ThreadState.Upsert(ctx, &ThreadState{
		UserLoginID: login, ThreadID: "t", Conversation: "c", Name: "bridge 1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db1.ConsumptionHorizon.UpsertLastRead(ctx, login, "t", "user", 7); err != nil {
		t.Fatal(err)
	}

	got, err := db2.Profile.GetByTeamsUserID(ctx, "user")
	assertProfileMissing(t, "bridge-2 lookup of bridge-1 profile", got, err)
	if got, err := db2.ThreadState.Get(ctx, login, "t"); err != nil || got != nil {
		t.Errorf("bridge-2 sees bridge-1 thread: %+v, %v", got, err)
	}
	if list, err := db2.ThreadState.ListForLogin(ctx, login); err != nil || len(list) != 0 {
		t.Errorf("bridge-2 list = %v, %v; want empty", list, err)
	}
	if got, err := db2.ConsumptionHorizon.Get(ctx, login, "t", "user"); err != nil || got != nil {
		t.Errorf("bridge-2 sees bridge-1 horizon: %+v, %v", got, err)
	}

	// Writing the same keys from bridge-2 must not clobber bridge-1.
	if err := db2.Profile.Upsert(ctx, "user", "From Bridge 2", time.UnixMilli(2)); err != nil {
		t.Fatal(err)
	}
	if err := db2.ThreadState.Upsert(ctx, &ThreadState{
		UserLoginID: login, ThreadID: "t", Conversation: "c", Name: "bridge 2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db2.ThreadState.UpdateCursor(ctx, login, "t", "b2", 22); err != nil {
		t.Fatal(err)
	}

	got1, err := db1.Profile.GetByTeamsUserID(ctx, "user")
	if err != nil || got1 == nil || got1.DisplayName != "From Bridge 1" || got1.BridgeID != "bridge-1" {
		t.Errorf("bridge-1 profile clobbered: %+v, %v", got1, err)
	}
	got2, err := db2.Profile.GetByTeamsUserID(ctx, "user")
	if err != nil || got2 == nil || got2.DisplayName != "From Bridge 2" || got2.BridgeID != "bridge-2" {
		t.Errorf("bridge-2 profile = %+v, %v", got2, err)
	}
	ts1, err := db1.ThreadState.Get(ctx, login, "t")
	if err != nil || ts1 == nil || ts1.Name != "bridge 1" || ts1.LastSequenceID != "" || ts1.LastMessageTS != 0 {
		t.Errorf("bridge-1 thread clobbered: %+v, %v", ts1, err)
	}
	ts2, err := db2.ThreadState.Get(ctx, login, "t")
	if err != nil || ts2 == nil || ts2.Name != "bridge 2" || ts2.LastSequenceID != "b2" || ts2.BridgeID != "bridge-2" {
		t.Errorf("bridge-2 thread = %+v, %v", ts2, err)
	}
}
