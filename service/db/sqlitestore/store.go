package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/store"
	"modernc.org/sqlite"
)

var (
	ErrClosed            = errors.New("SQLite runtime is closed")
	ErrBusy              = errors.New("SQLite runtime is busy")
	ErrIncompatibleValue = errors.New("SQLite key is a nested bucket")
	ErrInvalidKey        = errors.New("invalid SQLite bucket or key length")
)

const operationTimeout = 30 * time.Second

// Store owns its SQL handles. The caller must retain the configuration-directory
// instance lock until Close returns, and must not nest Store transactions.
type Store struct {
	writer, reader *sql.DB
	closeOnce      sync.Once
	closeErr       error
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.reader != nil {
			s.closeErr = s.reader.Close()
		}
		if s.writer != nil {
			s.closeErr = errors.Join(s.closeErr, s.writer.Close())
		}
	})
	return s.closeErr
}

func (s *Store) View(fn func(store.ReadTx) error) error {
	if s == nil || s.reader == nil {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sqlError("begin runtime read", err)
	}
	defer tx.Rollback()
	if err := fn(readTx{ctx: ctx, tx: tx}); err != nil {
		return err
	}
	return sqlError("finish runtime read", tx.Commit())
}

func (s *Store) Update(fn func(store.WriteTx) error) error {
	if s == nil || s.writer == nil {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return sqlError("begin runtime write", err)
	}
	defer tx.Rollback() // Also releases the writer after a callback panic.
	if err := fn(writeTx{readTx{ctx: ctx, tx: tx}}); err != nil {
		return err
	}
	if err := checkBounds(ctx, tx); err != nil {
		return err
	}
	return sqlError("commit runtime write", tx.Commit())
}

func (s *Store) Snapshot() (*backup.Archive, error) {
	var result *backup.Archive
	err := s.View(func(tx store.ReadTx) error {
		concrete := tx.(readTx)
		var err error
		result, err = snapshotTx(concrete.ctx, concrete.tx)
		return err
	})
	return result, err
}

func (s *Store) Check() error { _, err := s.Snapshot(); return err }

type readTx struct {
	ctx context.Context
	tx  *sql.Tx
}

func (t readTx) Get(bucket, key []byte) ([]byte, error) {
	var value []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT data FROM kv_entries WHERE parent_id = (SELECT id FROM kv_entries WHERE parent_id IS NULL AND name = ?) AND name = ? AND kind = 1`, bucket, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, sqlError("read runtime value", err)
	}
	return nonNil(value), nil
}

func (t readTx) ForEach(bucket []byte, fn func([]byte, []byte) error) error {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT name, kind, data FROM kv_entries WHERE parent_id = (SELECT id FROM kv_entries WHERE parent_id IS NULL AND name = ?) ORDER BY name`, bucket)
	if err != nil {
		return sqlError("iterate runtime bucket", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, data []byte
		var kind int
		if err := rows.Scan(&name, &kind, &data); err != nil {
			return sqlError("read runtime entry", err)
		}
		if kind == 0 {
			data = nil
		} else {
			data = nonNil(data)
		}
		if err := fn(name, data); err != nil {
			return err
		}
	}
	return sqlError("iterate runtime bucket", rows.Err())
}

func (t readTx) ForEachBucket(fn func([]byte) error) error {
	rows, err := t.tx.QueryContext(t.ctx, "SELECT name FROM kv_entries WHERE parent_id IS NULL ORDER BY name")
	if err != nil {
		return sqlError("iterate runtime roots", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name []byte
		if err := rows.Scan(&name); err != nil {
			return sqlError("read runtime root", err)
		}
		if err := fn(name); err != nil {
			return err
		}
	}
	return sqlError("iterate runtime roots", rows.Err())
}

func (t readTx) BucketLen(bucket []byte) (int, error) {
	var length int
	err := t.tx.QueryRowContext(t.ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT id FROM kv_entries WHERE parent_id = (SELECT id FROM kv_entries WHERE parent_id IS NULL AND name = ?)
 UNION ALL SELECT entries.id FROM kv_entries entries JOIN descendants ON entries.parent_id = descendants.id)
 SELECT count(*) FROM descendants`, bucket).Scan(&length)
	return length, sqlError("count runtime bucket", err)
}

type writeTx struct{ readTx }

func (t writeTx) Put(bucket, key, value []byte) error {
	if len(bucket) == 0 || len(bucket) > 32768 || len(key) == 0 || len(key) > 32768 {
		return ErrInvalidKey
	}
	if len(value) > backup.MaxDataBytes {
		return errors.New("SQLite value exceeds the backup size limit")
	}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO kv_entries(parent_id,name,kind,sequence) VALUES(NULL,?,0,?) ON CONFLICT DO NOTHING`, bucket, make([]byte, 8)); err != nil {
		return sqlError("create runtime bucket", err)
	}
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO kv_entries(parent_id,name,kind,data) VALUES((SELECT id FROM kv_entries WHERE parent_id IS NULL AND name = ?),?,1,?)
 ON CONFLICT(parent_id,name) DO UPDATE SET data=excluded.data WHERE kv_entries.kind=1`, bucket, key, nonNil(value))
	if err != nil {
		return sqlError("write runtime value", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return sqlError("verify runtime write", err)
	}
	if changed == 0 {
		return ErrIncompatibleValue
	}
	return nil
}

func (t writeTx) Delete(bucket, key []byte) error {
	var kind int
	err := t.tx.QueryRowContext(t.ctx, `SELECT kind FROM kv_entries WHERE parent_id=(SELECT id FROM kv_entries WHERE parent_id IS NULL AND name=?) AND name=?`, bucket, key).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return sqlError("locate runtime deletion", err)
	}
	if kind == 0 {
		return ErrIncompatibleValue
	}
	_, err = t.tx.ExecContext(t.ctx, `DELETE FROM kv_entries WHERE parent_id=(SELECT id FROM kv_entries WHERE parent_id IS NULL AND name=?) AND name=?`, bucket, key)
	return sqlError("delete runtime value", err)
}

func (t writeTx) DeleteBucket(bucket []byte) error {
	_, err := t.tx.ExecContext(t.ctx, "DELETE FROM kv_entries WHERE parent_id IS NULL AND name=?", bucket)
	return sqlError("delete runtime bucket", err)
}

func nonNil(data []byte) []byte {
	if data == nil {
		return []byte{}
	}
	return data
}

func sqlError(operation string, err error) error {
	if err == nil {
		return nil
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return fmt.Errorf("%s: %w", operation, cause)
		}
	}
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) || err.Error() == "sql: database is closed" {
		return fmt.Errorf("%s: %w", operation, ErrClosed)
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.Code()&255 == 5 || sqliteErr.Code()&255 == 6) {
		return fmt.Errorf("%s: %w", operation, ErrBusy)
	}
	// Driver diagnostics may contain untrusted schema SQL or credential values.
	return fmt.Errorf("%s failed", operation)
}

var _ store.Store = (*Store)(nil)
var _ store.ReadTx = readTx{}
var _ store.WriteTx = writeTx{}
