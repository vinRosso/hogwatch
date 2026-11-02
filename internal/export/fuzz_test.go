package export

import "testing"

func FuzzCSV_NoFormulaCell(f *testing.F) {
	for _, s := range []string{"=1+1", "\t=x", "-2", "@SUM(A1)", "a\nb", `"`, "\xff", "", "+", "\r", "'=x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if msg := oracle(in, guarded); msg != "" {
			t.Fatalf("%q: %s", in, msg)
		}
	})
}
