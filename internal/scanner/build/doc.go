// Package build produces one scan generation end to end: Run
// creates the file, walks the tree into it, builds the indexes, numbers the
// directories, re-attributes hardlinks and finishes it, removing the file on
// any failure. Verify is an independent checker of the invariants a finished
// generation must satisfy; it reads only the file.
package build
