package isureal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

// この driver は PostgreSQL の代用品ではない。実際の handler が lock と epoch
// INSERT SELECT を別 statement で発行し、READ COMMITTED の同一 tx に履歴を含むことを検査する。
type membershipSQLStep struct {
	sql      string
	check    func([]driver.NamedValue)
	args     []any
	columns  []string
	values   [][]driver.Value
	err      error
	affected *int64
}
type membershipTestDB struct {
	t                     *testing.T
	steps                 []membershipSQLStep
	committed, rolledBack bool
}
type membershipConnector struct{ state *membershipTestDB }

func (c membershipConnector) Connect(context.Context) (driver.Conn, error) {
	return &membershipConn{c.state}, nil
}
func (c membershipConnector) Driver() driver.Driver { return membershipDriver{c.state} }

type membershipDriver struct{ state *membershipTestDB }

func (d membershipDriver) Open(string) (driver.Conn, error) { return &membershipConn{d.state}, nil }

type membershipConn struct{ state *membershipTestDB }

func (c *membershipConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c *membershipConn) Close() error { return nil }
func (c *membershipConn) Begin() (driver.Tx, error) {
	return nil, errors.New("isolation must be explicit")
}
func (c *membershipConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		c.state.t.Fatalf("isolation = %v; READ COMMITTED required", opts.Isolation)
	}
	return c, nil
}
func (c *membershipConn) Commit() error   { c.state.committed = true; return nil }
func (c *membershipConn) Rollback() error { c.state.rolledBack = true; return nil }
func (c *membershipConn) next(query string, args []driver.NamedValue) membershipSQLStep {
	c.state.t.Helper()
	if len(c.state.steps) == 0 {
		c.state.t.Fatalf("unexpected SQL: %s", query)
	}
	step := c.state.steps[0]
	c.state.steps = c.state.steps[1:]
	if !strings.Contains(strings.Join(strings.Fields(query), " "), step.sql) {
		c.state.t.Fatalf("SQL = %s, want %s", query, step.sql)
	}
	if step.args != nil {
		if len(args) != len(step.args) {
			c.state.t.Fatalf("args = %v, want %v", args, step.args)
		}
		for i, want := range step.args {
			if want != nil && fmt.Sprint(args[i].Value) != fmt.Sprint(want) {
				c.state.t.Fatalf("arg[%d] = %v, want %v", i, args[i].Value, want)
			}
		}
	}
	if step.check != nil {
		step.check(args)
	}
	return step
}
func (c *membershipConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	step := c.next(q, args)
	if step.affected != nil {
		return driver.RowsAffected(*step.affected), step.err
	}
	return driver.RowsAffected(1), step.err
}
func (c *membershipConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	step := c.next(q, args)
	if step.err != nil {
		return nil, step.err
	}
	return &membershipRows{columns: step.columns, values: step.values}, nil
}

type membershipRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *membershipRows) Columns() []string { return r.columns }
func (r *membershipRows) Close() error      { return nil }
func (r *membershipRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}
func newMembershipTestDB(t *testing.T, steps []membershipSQLStep) (*sqlx.DB, *membershipTestDB) {
	t.Helper()
	state := &membershipTestDB{t: t, steps: steps}
	db := sqlx.NewDb(sql.OpenDB(membershipConnector{state}), "membership-test")
	t.Cleanup(func() {
		db.Close()
		if len(state.steps) != 0 {
			t.Errorf("%d SQL statements not executed", len(state.steps))
		}
	})
	return db, state
}
func membershipLock(region string) membershipSQLStep {
	return membershipSQLStep{sql: "SELECT pg_advisory_xact_lock_shared", args: []any{region}}
}
func membershipEpoch(region string, epoch int64) membershipSQLStep {
	return membershipSQLStep{sql: "SELECT current_epoch FROM region_state WHERE region = $1", args: []any{region}, columns: []string{"current_epoch"}, values: [][]driver.Value{{epoch}}}
}
func membershipWrite(user, region string, present bool) membershipSQLStep {
	var id any = user
	if user == "" {
		id = nil
	}
	return membershipSQLStep{sql: "INSERT INTO membership (user_id, region, epoch, present) SELECT $1, region, current_epoch, $3 FROM region_state WHERE region = $2 ON CONFLICT (user_id, region, epoch) DO UPDATE SET present = EXCLUDED.present", args: []any{id, region, present}}
}
func membershipContext(method, body string) *echo.Context {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c := echo.New().NewContext(req, httptest.NewRecorder())
	c.Set("session", &Session{UserID: "user", Region: "europe"})
	return c
}

func TestRegisterTracksMembershipAfterSuccessfulInsert(t *testing.T) {
	db, state := newMembershipTestDB(t, []membershipSQLStep{
		membershipLock("asia"),
		{sql: "INSERT INTO users", columns: []string{"id", "region"}, values: [][]driver.Value{{"new-user", "asia"}}},
		membershipWrite("new-user", "asia", true),
	})
	if err := registerHandler(&HandlerContext{DB: db})(membershipContext("POST", `{"username":"new-user","displayName":"New User","region":"asia"}`)); err != nil {
		t.Fatal(err)
	}
	if !state.committed || state.rolledBack {
		t.Fatalf("commit=%v rollback=%v", state.committed, state.rolledBack)
	}
}
func TestFailedRegistrationDoesNotWriteHistory(t *testing.T) {
	db, state := newMembershipTestDB(t, []membershipSQLStep{membershipLock("asia"), {sql: "INSERT INTO users", columns: []string{"id", "region"}}})
	if err := registerHandler(&HandlerContext{DB: db})(membershipContext("POST", `{"username":"taken","displayName":"Taken","region":"asia"}`)); err == nil {
		t.Fatal("expected username conflict")
	}
	if state.committed || !state.rolledBack {
		t.Fatalf("commit=%v rollback=%v", state.committed, state.rolledBack)
	}
}
func TestMoveUsesLockedRegionAndIndependentEpochs(t *testing.T) {
	db, state := newMembershipTestDB(t, []membershipSQLStep{
		{sql: "SELECT region FROM users", columns: []string{"region"}, values: [][]driver.Value{{"asia"}}},
		membershipLock("america"), membershipLock("asia"),
		{sql: "UPDATE users SET", columns: []string{"id", "region", "secret_key_hash"}, values: [][]driver.Value{{"user", "america", strings.Repeat("0", 64)}}},
		membershipWrite("user", "asia", false),
		membershipWrite("user", "america", true),
	})
	// Session は europe だが、実際の old region は row lock で取得した asia。
	if err := updateMeHandler(&HandlerContext{DB: db})(membershipContext("PATCH", `{"region":"america"}`)); err != nil {
		t.Fatal(err)
	}
	if !state.committed {
		t.Fatal("not committed")
	}
}
func TestDeleteKeepsHistoricalMembership(t *testing.T) {
	db, state := newMembershipTestDB(t, []membershipSQLStep{
		{sql: "SELECT id, region FROM users", columns: []string{"id", "region"}, values: [][]driver.Value{{"user", "asia"}}},
		membershipLock("asia"), {sql: "UPDATE users SET status='deleted'"},
		membershipWrite("user", "asia", false),
		{sql: "UPDATE login_sessions SET active=false"},
	})
	if err := deleteAccountHandler(&HandlerContext{DB: db})(membershipContext("DELETE", "")); err != nil {
		t.Fatal(err)
	}
	if !state.committed {
		t.Fatal("not committed")
	}
}
func TestMembershipWriteFailureRollsBackRegistration(t *testing.T) {
	failure := errors.New("history insert failed")
	write := membershipWrite("new-user", "asia", true)
	write.err = failure
	db, state := newMembershipTestDB(t, []membershipSQLStep{membershipLock("asia"), {sql: "INSERT INTO users", columns: []string{"id", "region"}, values: [][]driver.Value{{"new-user", "asia"}}}, write})
	if err := registerHandler(&HandlerContext{DB: db})(membershipContext("POST", `{"username":"new-user","displayName":"New User","region":"asia"}`)); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if state.committed || !state.rolledBack {
		t.Fatalf("commit=%v rollback=%v", state.committed, state.rolledBack)
	}
}
func TestMissingMembershipEpochFailsClosed(t *testing.T) {
	step := membershipWrite("user", "asia", true)
	zero := int64(0)
	step.affected = &zero
	db, state := newMembershipTestDB(t, []membershipSQLStep{membershipLock("asia"), step})
	tx, err := db.BeginTxx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	if err := lockPostingWindowRegionsShared(context.Background(), tx, "asia"); err != nil {
		t.Fatal(err)
	}
	if err := recordMembershipState(context.Background(), tx, "user", "asia", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v", err)
	}
	tx.Rollback()
	if state.committed {
		t.Fatal("unexpected commit")
	}
}
