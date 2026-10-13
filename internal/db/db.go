// Package db เชื่อมต่อ PostgreSQL และจัดการ migration (ไฟล์ SQL ฝังอยู่ใน binary) (F1)
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("เชื่อมต่อฐานข้อมูล: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("เชื่อมต่อฐานข้อมูลไม่ได้ (PostgreSQL รันอยู่หรือไม่? `brew services start postgresql@18`): %w", err)
	}
	return pool, nil
}

type migration struct {
	version  int
	name     string
	up, down string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*migration{}
	for _, e := range entries {
		name := e.Name()
		prefix, rest, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("ชื่อไฟล์ migration ไม่ถูกต้อง: %s", name)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		m := byVersion[v]
		if m == nil {
			base, _, _ := strings.Cut(rest, ".")
			m = &migration{version: v, name: base}
			byVersion[v] = m
		}
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			m.up = string(b)
		case strings.HasSuffix(name, ".down.sql"):
			m.down = string(b)
		}
	}
	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration %04d ต้องมีทั้ง up และ down", m.version)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func applied(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, error) {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	vs, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, v := range vs {
		out[v] = true
	}
	return out, nil
}

// Up ใช้ทุก migration ที่ยังไม่ได้ใช้ คืนชื่อที่ใช้ไป
func Up(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	done, err := applied(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range ms {
		if done[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.up); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version)
			return err
		})
		if err != nil {
			return out, fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
		out = append(out, fmt.Sprintf("%04d_%s", m.version, m.name))
	}
	return out, nil
}

// Down ย้อน migration ล่าสุดตามจำนวนขั้น
func Down(ctx context.Context, pool *pgxpool.Pool, steps int) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	done, err := applied(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(ms) - 1; i >= 0 && len(out) < steps; i-- {
		m := ms[i]
		if !done[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.down); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.version)
			return err
		})
		if err != nil {
			return out, fmt.Errorf("ย้อน migration %04d_%s: %w", m.version, m.name, err)
		}
		out = append(out, fmt.Sprintf("%04d_%s", m.version, m.name))
	}
	return out, nil
}
