package datastore

import (
	"context"

	"github.com/hiendaovinh/toolkit/pkg/mapper"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stephenafamo/scan"
)

type PGXPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

func PrepareSetterMap[T any](ctx context.Context, updates T) ([]string, []any) {
	ks := []string{}
	vs := []any{}
	m := mapper.GetMappings(updates)

	for k, v := range m {
		ks = append(ks, k)
		vs = append(vs, v)
	}

	return ks, vs
}

type Iterator[T any, K any, U func(T) K] struct {
	cursor scan.ICursor[T]
	mapper U
}

func (iterator *Iterator[T, K, U]) Next() bool {
	return iterator.cursor.Next()
}

func (iterator *Iterator[T, K, U]) Get() (K, error) {
	var k K
	v, err := iterator.cursor.Get()
	if err != nil {
		return k, err
	}
	k = iterator.mapper(v)
	return k, nil
}

func (iterator *Iterator[T, K, U]) Err() error {
	return iterator.cursor.Err()
}

func (iterator *Iterator[T, K, U]) Close() error {
	return iterator.cursor.Close()
}
