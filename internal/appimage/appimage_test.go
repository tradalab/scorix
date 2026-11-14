package appimage

import "testing"

func TestAnAppImageIsOursOnlyWhenTheExecutableRunsFromItsMount(t *testing.T) {
	for _, c := range []struct {
		name, exe, image, dir, want string
	}{
		{"our own mount", "/tmp/.mount_s3hubX/usr/bin/s3hub", "/home/u/S3Hub.AppImage", "/tmp/.mount_s3hubX", "/home/u/S3Hub.AppImage"},
		{"trailing slash on the mount", "/tmp/.mount_s3hubX/usr/bin/s3hub", "/home/u/S3Hub.AppImage", "/tmp/.mount_s3hubX/", "/home/u/S3Hub.AppImage"},
		{"inherited from a terminal inside another AppImage", "/usr/bin/s3hub", "/opt/OtherIDE.AppImage", "/tmp/.mount_OtherIDE", ""},
		{"a sibling mount sharing the prefix", "/tmp/.mount_s3hubXY/usr/bin/s3hub", "/home/u/S3Hub.AppImage", "/tmp/.mount_s3hubX", ""},
		{"no APPDIR", "/tmp/.mount_s3hubX/usr/bin/s3hub", "/home/u/S3Hub.AppImage", "", ""},
		{"no APPIMAGE", "/tmp/.mount_s3hubX/usr/bin/s3hub", "", "/tmp/.mount_s3hubX", ""},
	} {
		if got, _ := Of(c.exe, c.image, c.dir); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
