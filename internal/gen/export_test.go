package gen

import "database/sql"

// SetMaxErrorRows lowers the stored-error cap for one test and returns a
// function that restores it.
func SetMaxErrorRows(n int64) (restore func()) {
	old := maxErrorRows
	maxErrorRows = n
	return func() { maxErrorRows = old }
}

// WriterDB exposes the Writer's connection so tests can inspect it mid-write.
func WriterDB(w *Writer) *sql.DB { return w.conn }

// SetAfterStat installs a hook OpenReadOnly runs between its existence check
// and opening the file, and returns a function that removes it.
func SetAfterStat(f func()) (restore func()) {
	old := afterStat
	afterStat = f
	return func() { afterStat = old }
}
