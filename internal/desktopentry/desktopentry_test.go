package desktopentry

import "testing"

func TestRewriteSetsMainKeysAndEveryExec(t *testing.T) {
	src := "[Desktop Entry]\nName=S3Hub\nExec=S3Hub %u\nIcon=S3Hub\nIcon[vi]=S3Hub\n\n[Desktop Action new]\nName=New\nExec=S3Hub --new\nIcon=keep\n"
	got := string(Rewrite([]byte(src), Quote("/home/a b/S3Hub.AppImage"), map[string]string{"Icon": "com.x.s3hub", "StartupWMClass": "com.x.s3hub"}))
	want := "[Desktop Entry]\nName=S3Hub\nExec=\"/home/a b/S3Hub.AppImage\" %u\nIcon=com.x.s3hub\nIcon[vi]=S3Hub\nStartupWMClass=com.x.s3hub\n\n[Desktop Action new]\nName=New\nExec=\"/home/a b/S3Hub.AppImage\" --new\nIcon=keep\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if again := string(Rewrite([]byte(got), Quote("/home/a b/S3Hub.AppImage"), map[string]string{"Icon": "com.x.s3hub", "StartupWMClass": "com.x.s3hub"})); again != got {
		t.Fatalf("a second rewrite changed the file:\n%s", again)
	}
}

func TestQuoteEscapesForTheSpec(t *testing.T) {
	if got := Quote("/opt/app"); got != "/opt/app" {
		t.Fatalf("plain path quoted: %s", got)
	}
	if got := Quote("/x/100%"); got != "/x/100%%" {
		t.Fatalf("a %% left to read as a field code: %s", got)
	}
	if got, want := Quote(`/x/$a"b`), `"/x/\\$a\\"b"`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestSpacesAroundTheEqualsSign(t *testing.T) {
	src := []byte("[Desktop Entry]\nExec = S3Hub %u\nIcon = S3Hub\n")
	if got := Get(src, "Exec"); got != "S3Hub %u" {
		t.Fatalf("Get = %q", got)
	}
	want := "[Desktop Entry]\nExec=/opt/s3hub %u\nIcon=com.x.s3hub\n"
	if got := string(Rewrite(src, "/opt/s3hub", map[string]string{"Icon": "com.x.s3hub"})); got != want {
		t.Fatalf("got:\n%s", got)
	}
}
