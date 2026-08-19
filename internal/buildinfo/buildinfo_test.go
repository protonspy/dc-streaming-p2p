package buildinfo

import "testing"

func TestShortCommit(t *testing.T) {
	cases := []struct {
		name   string
		commit string
		want   string
	}{
		{name: "abbreviates a full hash", commit: "aea6f3896919436a69588388382a16b58a59e8e5", want: "aea6f38"},
		{name: "keeps a value already short", commit: "abc", want: "abc"},
		{name: "keeps the boundary length", commit: "abcdefg", want: "abcdefg"},
		{name: "keeps an empty value", commit: "", want: ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restore := Commit
			t.Cleanup(func() { Commit = restore })

			Commit = c.commit
			if got := ShortCommit(); got != c.want {
				t.Errorf("ShortCommit() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		name    string
		version string
		commit  string
		date    string
		want    string
	}{
		{
			name:    "a stamped build names commit and date",
			version: "1.2.0",
			commit:  "aea6f3896919436a69588388382a16b58a59e8e5",
			date:    "2026-08-18",
			want:    "central 1.2.0 (aea6f38) built 2026-08-18",
		},
		{
			name:    "an unstamped build carries neither",
			version: "dev",
			commit:  "unknown",
			date:    "unknown",
			want:    "central dev",
		},
		{
			name:    "empty stamps are treated as absent",
			version: "dev",
			commit:  "",
			date:    "",
			want:    "central dev",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreVersion, restoreCommit, restoreDate := Version, Commit, Date
			t.Cleanup(func() { Version, Commit, Date = restoreVersion, restoreCommit, restoreDate })

			Version, Commit, Date = c.version, c.commit, c.date
			if got := String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}
