package datastore

import (
	"context"

	"github.com/hiendaovinh/toolkit/pkg/arr"
	"github.com/jackc/pgx/v5"
	ice_x "github.com/phinc275/ice-x/internal/ice-x"
	"github.com/phinc275/ice-x/internal/ice-x/bob"
	realBob "github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/dm"
	"github.com/stephenafamo/bob/dialect/psql/sm"
	"github.com/stephenafamo/bob/dialect/psql/um"
	"github.com/stephenafamo/scan"
)

type XAccountDatastorePgx struct {
	pool        PGXPool
	bobExecutor BobExecutor
}

var _ ice_x.XAccountDatastore = (*XAccountDatastorePgx)(nil)

func NewXAccountDatastorePgx(pool PGXPool) (*XAccountDatastorePgx, error) {
	return &XAccountDatastorePgx{pool, &BobExecutorPgx{pool}}, nil
}

func (ds *XAccountDatastorePgx) FindAll(ctx context.Context) ([]*ice_x.XAccount, error) {
	items, err := bob.XAccounts.Query(ctx, ds.bobExecutor, sm.OrderBy(bob.XAccountColumns.Username)).All()
	if err != nil {
		return nil, err
	}
	return arr.ArrMap(items, FromXAccountBob), nil
}

func (ds *XAccountDatastorePgx) FindOneByUsername(ctx context.Context, username string) (*ice_x.XAccount, error) {
	item, err := bob.XAccounts.Query(ctx, ds.bobExecutor, sm.Where(bob.XAccountColumns.Username.EQ(psql.Arg(username)))).One()
	if err != nil {
		return nil, err
	}
	return FromXAccountBob(item), nil
}

func (ds *XAccountDatastorePgx) Create(ctx context.Context, account *ice_x.XAccountSetter) (*ice_x.XAccount, error) {
	item, err := bob.XAccounts.Insert(ctx, ds.bobExecutor, (*bob.XAccountSetter)(account))
	if err != nil {
		return nil, err
	}
	return FromXAccountBob(item), nil
}

func (ds *XAccountDatastorePgx) Update(ctx context.Context, account *ice_x.XAccountSetter) (*ice_x.XAccount, error) {
	if !account.Username.IsSet() {
		return nil, pgx.ErrNoRows
	}

	ks, vs := PrepareSetterMap(ctx, account)
	builder := psql.Update(
		um.Table(bob.XAccounts.Name(ctx)),
		um.Where(bob.XAccountColumns.Username.EQ(psql.Arg(account.Username.MustGet()))),
		um.Returning("*"),
	)
	for i, x := range ks {
		builder.Apply(
			um.Set(x).ToArg(vs[i]),
		)
	}

	item, err := realBob.One(ctx, ds.bobExecutor, builder, scan.StructMapper[*bob.XAccount]())
	if err != nil {
		return nil, err
	}

	return FromXAccountBob(item), nil
}

func (ds *XAccountDatastorePgx) Delete(ctx context.Context, username string) error {
	_, err := psql.Delete(
		dm.From(bob.TableNames.XAccounts),
		dm.Where(bob.XAccountColumns.Username.EQ(psql.Arg(username))),
	).Exec(ctx, ds.bobExecutor)
	return err
}
