package oapi

import (
	"reflect"
	"testing"
)

// Compile-time assertions: each assignment fails to build when the generator
// stops producing the nullable shape the contract declares.
var (
	_ *string = ScanAdmin{}.Error
	_ *string = Me{}.Email
	_ *string = Me{}.Notice
	_ bool    = AuthConfig{}.Local
	_ bool    = AuthConfig{}.Oidc
	_ *string = ChildrenPage{}.NextCursor
	_ *string = Listing{}.NextCursor
	_         = [...]ListingRequestMode{Children, Subtree}
	_ string  = Node{}.RelPath
	_ *int64  = Child{}.BytesLogical
	_         = [...]TreemapNodeKind{TreemapNodeKindDir, TreemapNodeKindFiles, TreemapNodeKindRest}
	_ bool    = Listing{}.Truncated
	_ *string = Listing{}.Error
)

// ScanSummary is the user view: scan errors can hold host paths.
func TestScanSummary_HasNoError(t *testing.T) {
	if _, ok := reflect.TypeOf(ScanSummary{}).FieldByName("Error"); ok {
		t.Fatal("ScanSummary has an Error field")
	}
	if _, ok := reflect.TypeOf(ScanAdmin{}).FieldByName("Error"); !ok {
		t.Fatal("ScanAdmin lost its Error field")
	}
}

// M8 shapes.
var (
	_ bool    = AdminUser{}.HasPassword
	_ *string = UserPatch{}.Password
	_ string  = UserInput{}.Password
	_ bool    = UserPath{}.Redundant
	_ *int64  = ConfigIssue{}.Id
	_ *int64  = ScanJob{}.ScanId
	_ *int64  = ScanAdmin{}.JobId
	_ int64   = RankingRow{}.SharedHardlinkBytes
	_ *int64  = AvailableMount{}.RootId
	_ *int64  = MountStat{}.BytesAlloc
)

// The admin never types a host path: a root is created from an available mount.
func TestRootInput_HasNoHostPath(t *testing.T) {
	if _, ok := reflect.TypeOf(RootInput{}).FieldByName("HostPath"); ok {
		t.Fatal("RootInput has a HostPath field")
	}
	if _, ok := reflect.TypeOf(RootPatch{}).FieldByName("ContainerPath"); ok {
		t.Fatal("RootPatch has a ContainerPath field")
	}
}
