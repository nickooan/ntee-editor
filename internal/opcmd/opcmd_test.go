package opcmd

import (
	"slices"
	"testing"
)

func TestParseAndRender(t *testing.T) {
	system := map[string]string{SystemFilePath: "my project/main.tf", SystemDirPath: "my project"}
	cases := []struct {
		name     string
		source   string
		args     []string
		want     string
		wantMax  int
		wantVars []string
	}{
		{"plain", "make build", nil, "make build", 0, nil},
		{"two args", "exec x.tf -i {$1} -b {$2}", []string{"master", "master"}, "exec x.tf -i master -b master", 2, nil},
		{"repeated arg", "echo {$1} {$1}", []string{"a"}, "echo a a", 1, nil},
		{"out of order", "cp {$2} {$1}", []string{"dst", "src"}, "cp src dst", 2, nil},
		{"spaces quoted", "echo {$1}", []string{"hello world"}, "echo 'hello world'", 1, nil},
		{"fpath quoted", "go test {$fpath}", nil, "go test 'my project/main.tf'", 0, []string{"fpath"}},
		{"fpath twice", "{$fpath}:{$fpath}", nil, "'my project/main.tf':'my project/main.tf'", 0, []string{"fpath"}},
		{"dpath and fpath", "cd {$dpath} && wc {$fpath}", nil, "cd 'my project' && wc 'my project/main.tf'", 0, []string{"dpath", "fpath"}},
		{"literal braces", "awk '{print $1}' {$1}", []string{"f.txt"}, "awk '{print $1}' f.txt", 1, nil},
		{"adjacent", "{$1}{$2}", []string{"a", "b"}, "ab", 2, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			template, err := Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if template.MaxArg != tc.wantMax || !slices.Equal(template.System, tc.wantVars) {
				t.Fatalf("MaxArg=%d System=%v, want %d %v", template.MaxArg, template.System, tc.wantMax, tc.wantVars)
			}
			got, err := template.Render(tc.args, system)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, source := range []string{"echo {$", "echo {$1", "echo {$}", "echo {$0}", "echo {$1x}", "echo {$home}", "echo {$-1}"} {
		if _, err := Parse(source); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", source)
		}
	}
}

func TestRenderArgCountMismatch(t *testing.T) {
	template, err := Parse("echo {$1} {$2}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.Render([]string{"a"}, nil); err == nil || err.Error() != "needs 2 args, got 1" {
		t.Fatalf("too few args: err=%v", err)
	}
	if _, err := template.Render([]string{"a", "b", "c"}, nil); err == nil {
		t.Fatal("too many args should error")
	}
}

func TestRenderMissingSystemValue(t *testing.T) {
	template, err := Parse("cat {$fpath}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.Render(nil, map[string]string{}); err == nil {
		t.Fatal("missing system value should error")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"master":           "master",
		"a/b-c_d.e:f=g,h@": "a/b-c_d.e:f=g,h@",
		"":                 "''",
		"two words":        "'two words'",
		"it's":             `'it'\''s'`,
		"$HOME":            "'$HOME'",
		"a;rm -rf /":       "'a;rm -rf /'",
		"x|y":              "'x|y'",
		"*.go":             "'*.go'",
	}
	for value, want := range cases {
		if got := ShellQuote(value); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"master master", []string{"master", "master"}},
		{"  a   b  ", []string{"a", "b"}},
		{`"hello world" x`, []string{"hello world", "x"}},
		{`'it"s' b`, []string{`it"s`, "b"}},
		{`"say \"hi\""`, []string{`say "hi"`}},
		{`a\ b`, []string{"a b"}},
		{`pre"mid"post`, []string{"premidpost"}},
		{`'' x`, []string{"", "x"}},
	}
	for _, tc := range cases {
		got, err := SplitArgs(tc.line)
		if err != nil {
			t.Fatalf("SplitArgs(%q): %v", tc.line, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
	for _, line := range []string{`"open`, `'open`} {
		if _, err := SplitArgs(line); err == nil {
			t.Errorf("SplitArgs(%q) should error", line)
		}
	}
}
