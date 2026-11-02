package fsx

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseMountinfo(t *testing.T) {
	const in = `22 28 0:21 / /sys rw,nosuid shared:7 master:2 - sysfs sysfs rw
23 28 8:1 / /mnt/a\040b rw,relatime - ext4 /dev/sda1 rw
24 28 0:6 / /dev rw,nosuid - devtmpfs udev rw,size=1k
25 28 0:30 / /plain rw - tmpfs tmpfs rw
`
	got, err := ParseMountinfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		{Dev: unix.Mkdev(0, 21), FSType: "sysfs", MountPoint: "/sys"},
		{Dev: unix.Mkdev(8, 1), FSType: "ext4", MountPoint: "/mnt/a b"},
		{Dev: unix.Mkdev(0, 6), FSType: "devtmpfs", MountPoint: "/dev"},
		{Dev: unix.Mkdev(0, 30), FSType: "tmpfs", MountPoint: "/plain"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mounts, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseMountinfo_Malformed(t *testing.T) {
	for name, in := range map[string]string{
		"too few fields": "22 28 0:21 / /sys\n",
		"no separator":   "22 28 0:21 / /sys rw shared:1 sysfs sysfs rw\n",
		"bad devno":      "22 28 021 / /sys rw - sysfs sysfs rw\n",
	} {
		if _, err := ParseMountinfo(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReadMounts(t *testing.T) {
	ms, err := ReadMounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("no mounts")
	}
}

func TestParseMountinfo_ReadOnly(t *testing.T) {
	const in = `23 28 8:1 / /a ro,relatime shared:1 - ext4 /dev/sda1 rw
24 28 8:2 / /b rw,nosuid - ext4 /dev/sda2 ro
25 28 8:3 / /c rw,nosuid,ro2 - ext4 /dev/sda3 ro
`
	got, err := ParseMountinfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false}
	for i, w := range want {
		if got[i].ReadOnly != w {
			t.Errorf("mount %d ReadOnly = %v, want %v", i, got[i].ReadOnly, w)
		}
	}
}
