package datastore

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jackc/pgx/v5"
	ice_x "github.com/phinc275/ice-x/internal/ice-x"
	"github.com/phinc275/ice-x/internal/ice-x/bob"
	"github.com/stephenafamo/scan"
)

type BobExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (scan.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type BobExecutorPgx struct {
	pool PGXPool
}

type rows struct {
	pgx.Rows
}

func (r rows) Close() error {
	r.Rows.Close()
	return nil
}

func (r rows) Columns() ([]string, error) {
	fields := r.FieldDescriptions()
	cols := make([]string, len(fields))

	for i, field := range fields {
		cols[i] = field.Name
	}

	return cols, nil
}

func (v *BobExecutorPgx) QueryContext(ctx context.Context, query string, args ...any) (scan.Rows, error) {
	r, err := v.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	return rows{r}, nil
}

func (v *BobExecutorPgx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tag, err := v.pool.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	return driver.RowsAffected(tag.RowsAffected()), err
}

func FromXAccountBob(v *bob.XAccount) *ice_x.XAccount {
	return &ice_x.XAccount{
		Username:                  v.Username,
		Password:                  v.Password.GetOrZero(),
		Email:                     v.Email.GetOrZero(),
		EmailPassword:             v.EmailPassword.GetOrZero(),
		AutoEmailConfirmationCode: v.AutoEmailConfirmationCode,
		Cookies:                   v.Cookies.GetOrZero(),
		CreatedAt:                 v.CreatedAt,
		UpdatedAt:                 v.UpdatedAt,
	}
}
