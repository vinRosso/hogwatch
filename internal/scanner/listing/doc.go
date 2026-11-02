// Package listing runs one on-demand listing job. It resolves the
// target node in its generation file, opens the directory through
// fsx.OpenBeneath so no symlink is followed and no mount is crossed, checks
// that it is still the directory the scan saw, and writes listing_results.
// The implementation is Linux-only.
package listing
