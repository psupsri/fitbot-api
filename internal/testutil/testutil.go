// Package testutil ช่วยสร้างฐานข้อมูลสำหรับ integration test
// ใช้ FITBOT_TEST_DATABASE_URL (ค่าเริ่มต้น: ฐานข้อมูล fitbot_test บนเครื่อง) แต่ละ package ใช้ schema ของตัวเอง
package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"fitbot/internal/db"
)

func DB(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FITBOT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres:///fitbot_test?host=/tmp"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil || admin.Ping(ctx) != nil {
		t.Skipf("ไม่มีฐานข้อมูลทดสอบ (%s): %v", url, err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := pgxpool.New(ctx, url+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
