package cube

import "fmt"

// Filter selects cube cells by bitset per dimension (bit i = bucket i). A zero
// bitset means "all" for that dimension.
type Filter struct{ Categories, Ages, Sizes uint16 }

func pass(set uint16, i int) bool { return set == 0 || set&(1<<uint(i)) != 0 }

// Apply sums bytes and files over the cells of b matching f. A sum that would
// overflow int64 is reported as ErrCorrupt.
func (f Filter) Apply(b Blob) (bytes, files int64, err error) {
	a, err := Decode(b)
	if err != nil {
		return 0, 0, err
	}
	for cat := 0; cat < NumCategories; cat++ {
		if !pass(f.Categories, cat) {
			continue
		}
		for age := 0; age < NumAges; age++ {
			if !pass(f.Ages, age) {
				continue
			}
			for sz := 0; sz < NumSizes; sz++ {
				if !pass(f.Sizes, sz) {
					continue
				}
				c := a[(cat*NumAges+age)*NumSizes+sz]
				nb, nf := bytes+c.Bytes, files+c.Files
				if nb < bytes || nf < files {
					return 0, 0, fmt.Errorf("%w: sum overflow", ErrCorrupt)
				}
				bytes, files = nb, nf
			}
		}
	}
	return bytes, files, nil
}
