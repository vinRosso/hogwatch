package repo

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/vinRosso/hogwatch/internal/cube"
)

// hw_cube_bytes(blob, cats, ages, sizes) and hw_cube_files(...) apply a cube
// filter inside SQLite, so a page of a huge directory sorts by filtered value
// without every child passing through Go. A NULL blob (a directory without a
// cube row) sums to 0; a corrupt blob is an SQL error.
func init() {
	for name, wantBytes := range map[string]bool{"hw_cube_bytes": true, "hw_cube_files": false} {
		err := sqlite.RegisterDeterministicScalarFunction(name, 4,
			func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				return cubeSum(args, wantBytes)
			})
		if err != nil {
			panic(fmt.Sprintf("repo: register %s: %v", name, err))
		}
	}
}

func cubeSum(args []driver.Value, wantBytes bool) (driver.Value, error) {
	if args[0] == nil {
		return int64(0), nil
	}
	blob, ok := args[0].([]byte)
	if !ok {
		return nil, fmt.Errorf("repo: cube blob is %T", args[0])
	}
	var f cube.Filter
	for i, dst := range []*uint16{&f.Categories, &f.Ages, &f.Sizes} {
		v, ok := args[i+1].(int64)
		if !ok || v < 0 || v > 0xffff {
			return nil, fmt.Errorf("repo: cube filter argument %d is %v", i+1, args[i+1])
		}
		*dst = uint16(v)
	}
	b, n, err := f.Apply(cube.Blob(blob))
	if err != nil {
		return nil, err
	}
	if wantBytes {
		return b, nil
	}
	return n, nil
}

// filterArgs are the three SQL arguments of one hw_cube_* call.
func filterArgs(f cube.Filter) []any {
	return []any{int64(f.Categories), int64(f.Ages), int64(f.Sizes)}
}
