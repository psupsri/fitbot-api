package db_test

import (
	"context"
	"testing"

	"fitbot/internal/db"
	"fitbot/internal/testutil"
)

// AC F1: migration สร้างและย้อนกลับได้ ทุกตารางข้อมูลมี user_id
func TestMigrateUpDown(t *testing.T) {
	pool := testutil.DB(t, "t_db")
	ctx := context.Background()
	tables := func() map[string]bool {
		rows, _ := pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name <> 'schema_migrations'`)
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out[n] = true
		}
		return out
	}
	got := tables()
	if len(got) != 8 {
		t.Fatalf("ต้องมี 8 ตาราง ได้ %v", got)
	}
	for n := range got {
		if n == "users" || n == "job_runs" {
			continue
		}
		var has bool
		pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'user_id')`, n).Scan(&has)
		if !has {
			t.Errorf("ตาราง %s ไม่มี user_id", n)
		}
	}
	if _, err := db.Down(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	if n := len(tables()); n != 0 {
		t.Fatalf("หลัง down ยังเหลือ %d ตาราง", n)
	}
	if _, err := db.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if done, _ := db.Up(ctx, pool); len(done) != 0 {
		t.Fatal("up ซ้ำต้องไม่ทำอะไร")
	}
}
