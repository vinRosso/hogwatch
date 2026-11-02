//go:build linux

package service

import (
	"reflect"
	"testing"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
)

func TestAvailableMounts(t *testing.T) {
	in := []fsx.Mount{
		{MountPoint: "/host/x y", FSType: "ext4"},
		{MountPoint: "/data", FSType: "ext4"},
		{MountPoint: "/host/proc", FSType: "proc"},
		{MountPoint: "/host/storage", FSType: "zfs", ReadOnly: true},
		{MountPoint: "/host", FSType: "ext4", ReadOnly: true},
		{MountPoint: "/hostile", FSType: "ext4"},
	}
	want := []db.AvailableMount{
		{ContainerPath: "/host", HostPath: "/", FSType: "ext4", ReadOnly: true},
		{ContainerPath: "/host/storage", HostPath: "/storage", FSType: "zfs", ReadOnly: true},
		{ContainerPath: "/host/x y", HostPath: "/x y", FSType: "ext4"},
	}
	for _, prefix := range []string{"/host", "/host/"} {
		if got := AvailableMounts(in, prefix); !reflect.DeepEqual(got, want) {
			t.Errorf("prefix %q: got %+v\nwant %+v", prefix, got, want)
		}
	}
	if got := AvailableMounts(nil, "/host"); got == nil || len(got) != 0 {
		t.Errorf("no mounts: got %#v, want empty non-nil", got)
	}
}
