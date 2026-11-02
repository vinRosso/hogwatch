package finalize

// SetFlushDirs lowers the delta flush threshold for one test and returns a
// function that restores it.
func SetFlushDirs(n int) (restore func()) {
	old := flushDirs
	flushDirs = n
	return func() { flushDirs = old }
}
