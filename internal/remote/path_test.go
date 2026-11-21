package remote

import (
	"safekeep/internal/db"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPathRoundTrip(t *testing.T) {
	cases := []struct{ source, encoded string }{
		{"/var/lib/my_sql", "!var!lib!my_sql"},
		{"/a/!b", "!a!~!b"}, {"/a!/b", "!a~!!b"},
		{"/a/~!__", "!a!~~~!__"},
		{`C:\Users\backup_data`, `C~;~^Users~^backup_data`},
		{`\\server\share`, `~^~^server~^share`},
		{"/目录/含 空格", "!目录!含 空格"},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			encoded := EncodePath(tc.source)
			if encoded != tc.encoded {
				t.Fatalf("encoding: got %q want %q", encoded, tc.encoded)
			}
			decoded, err := DecodePath(encoded)
			if err != nil || decoded != tc.source {
				t.Fatalf("round trip: %q %v", decoded, err)
			}
		})
	}
	for _, bad := range []string{"~", "~x"} {
		if _, err := DecodePath(bad); err == nil {
			t.Errorf("accepted invalid escape %q", bad)
		}
	}
}

func FuzzPathRoundTrip(f *testing.F) {
	for _, source := range []string{"/var/lib/mysql", "/a/!b", "/a!/b", `C:\a~!b`} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if !utf8.ValidString(source) {
			return
		}
		decoded, err := DecodePath(EncodePath(source))
		if err != nil || decoded != source {
			t.Fatalf("round trip failed for %q: %q %v", source, decoded, err)
		}
	})
}

func TestArchiveCommandQuotesInput(t *testing.T) {
	dir := db.RemoteDirectory{Path: "/tmp/a'; touch /tmp/injected; echo '", Excludes: []string{"$(touch /tmp/injected)", "--checkpoint-action=exec=touch /tmp/injected"}}
	cmd := archiveCommand("posix", dir)
	if !strings.Contains(cmd, shellQuote(dir.Path)) || !strings.Contains(cmd, shellQuote("--exclude="+dir.Excludes[0])) {
		t.Fatalf("input is not quoted: %s", cmd)
	}
	if !strings.HasPrefix(archiveCommand("windows", dir), "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatal("Windows command must use encoded script")
	}
}
