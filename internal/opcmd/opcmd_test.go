package opcmd

import (
	"fmt"
	"slices"
	"strings"
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
			got, err := template.Render(tc.args, nil, system)
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
	if _, err := template.Render([]string{"a"}, nil, nil); err == nil || err.Error() != "needs 2 args, got 1" {
		t.Fatalf("too few args: err=%v", err)
	}
	if _, err := template.Render([]string{"a", "b", "c"}, nil, nil); err == nil || err.Error() != "needs 2 args, got 3 — put extra flags after --" {
		t.Fatalf("too many args: err=%v", err)
	}
}

func TestRenderMissingSystemValue(t *testing.T) {
	template, err := Parse("cat {$fpath}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.Render(nil, nil, map[string]string{}); err == nil {
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
		got, extra, err := SplitArgs(tc.line)
		if err != nil {
			t.Fatalf("SplitArgs(%q): %v", tc.line, err)
		}
		if !slices.Equal(got, tc.want) || extra != nil {
			t.Errorf("SplitArgs(%q) = %q %q, want %q", tc.line, got, extra, tc.want)
		}
	}
	for _, line := range []string{`"open`, `'open`, `a -- "open`} {
		if _, _, err := SplitArgs(line); err == nil {
			t.Errorf("SplitArgs(%q) should error", line)
		}
	}
}

func TestDefaults(t *testing.T) {
	cases := []struct {
		name, source     string
		args             []string
		want             string
		wantMin, wantMax int
	}{
		{"bare default used", "git checkout {$1 or master}", nil, "git checkout master", 0, 1},
		{"bare default overridden", "git checkout {$1 or master}", []string{"dev"}, "git checkout dev", 0, 1},
		{"quoted default with spaces", "echo {$1 or \"other word\"}", nil, "echo 'other word'", 0, 1},
		{"quoted default with brace and quote", `echo {$1 or "a } \"b\""}`, nil, `echo 'a } "b"'`, 0, 1},
		{"empty default disappears", `git log {$1 or ""} --oneline`, nil, "git log  --oneline", 0, 1},
		{"typed empty arg stays", `git log {$1 or ""} --oneline`, []string{""}, "git log '' --oneline", 0, 1},
		{"required then optional", `cp {$1} {$2 or /tmp}`, []string{"a"}, "cp a /tmp", 1, 2},
		{"required then optional both given", `cp {$1} {$2 or /tmp}`, []string{"a", "b"}, "cp a b", 1, 2},
		{"defaulted before required, all typed", `x {$1 or a} {$2}`, []string{"p", "q"}, "x p q", 1, 2},
		{"defaulted before required, required only", `x {$1 or a} {$2}`, []string{"q"}, "x a q", 1, 2},
		{"same index required elsewhere", `x {$1} {$1 or z}`, []string{"v"}, "x v v", 1, 1},
		{"spaces inside braces", `x {$1   or   5  }`, nil, "x 5", 0, 1},
		{"default needs quoting", `x {$1 or "$HOME"}`, nil, "x '$HOME'", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			template, err := Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if template.MinArg != tc.wantMin || template.MaxArg != tc.wantMax {
				t.Fatalf("MinArg=%d MaxArg=%d, want %d %d", template.MinArg, template.MaxArg, tc.wantMin, tc.wantMax)
			}
			got, err := template.Render(tc.args, nil, nil)
			if err != nil || got != tc.want {
				t.Fatalf("Render = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDefaultErrors(t *testing.T) {
	for _, source := range []string{
		"x {$1 or}", `x {$1 or "x}`, "x {$1 master}", "x {$fpath or a}", "x {$0 or a}",
		"x {$1 or a b}", `x {$1 or a"b"}`, "x {$1 or", "x {$1 or }",
	} {
		if _, err := Parse(source); err == nil {
			t.Errorf("Parse(%q) should fail", source)
		}
	}
	if _, err := Parse("x {$fpath or a}"); err == nil || !strings.Contains(err.Error(), "only arguments take defaults") {
		t.Errorf("system default error = %v", err)
	}
	if _, err := Parse("x {$1 orange}"); err == nil {
		t.Error(`"orange" is not the or keyword`)
	}
}

func TestQuotedPlaceholderRejected(t *testing.T) {
	for _, source := range []string{
		`echo "{$1}"`, `echo '{$1}'`, `cat "dir/{$fpath}"`, `echo $'{$1}'`,
		`echo "a {$2 or x} b"`, `x "{$1 or "y"}"`, `echo "it's {$1}"`, `echo "\"{$1}"`,
	} {
		if _, err := Parse(source); err == nil || !strings.Contains(err.Error(), "inside") {
			t.Errorf("Parse(%q) = %v, want an inside-quotes error", source, err)
		}
	}
	for _, source := range []string{`echo \{$1}`, `echo ${$1}`, `echo "a"\{$1}`} {
		if _, err := Parse(source); err == nil || !strings.Contains(err.Error(), "directly follow") {
			t.Errorf("Parse(%q) = %v, want a directly-follows error", source, err)
		}
	}
	for _, source := range []string{
		`echo {$1}`, `echo "a" {$1}`, `echo \"{$1}`, `echo "\"" {$1}`, `echo 'it'\''s' {$1}`,
		`awk '{print $1}' {$fpath}`, `x {$2 or "other word"}`, `echo \\{$1}`, `echo {$1}{$2} "b"`,
		`echo $'a\'b' {$1}`, `echo $HOME/{$1}`,
	} {
		if _, err := Parse(source); err != nil {
			t.Errorf("Parse(%q) = %v, want ok", source, err)
		}
	}
	if _, err := Parse(`git commit -m "{$1}"`); err == nil || err.Error() != `{$1} is inside "…" quotes — remove them, values are quoted for you` {
		t.Errorf("double-quote error = %v", err)
	}
	if _, err := Parse(`echo '{$fpath}'`); err == nil || err.Error() != `{$fpath} is inside '…' quotes — remove them, values are quoted for you` {
		t.Errorf("single-quote error = %v", err)
	}
	if _, err := Parse(`echo \{$1}`); err == nil || err.Error() != `{$1} can't directly follow \ — put a space between them` {
		t.Errorf("backslash error = %v", err)
	}
}

func TestArgCountErrors(t *testing.T) {
	template, err := Parse("x {$1} {$2 or a} {$3 or b}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.Render(nil, nil, nil); err == nil || err.Error() != "needs 1 arg ($1), got 0" {
		t.Errorf("too few: %v", err)
	}
	if _, err := template.Render([]string{"1", "2", "3", "4"}, nil, nil); err == nil || err.Error() != "takes at most 3 args, got 4 — put extra flags after --" {
		t.Errorf("too many: %v", err)
	}
	if got, err := template.Render([]string{"1", "2"}, nil, nil); err != nil || got != "x 1 2 b" {
		t.Errorf("partial: %q %v", got, err)
	}
}

func TestArgSpecs(t *testing.T) {
	template, err := Parse(`run {$1} {$2 or master} {$3 or ""} {$2 or other}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArgSpec{{Index: 1}, {Index: 2, Default: "master", Optional: true}, {Index: 3, Optional: true}}
	if got := template.ArgSpecs(); !slices.Equal(got, want) {
		t.Fatalf("ArgSpecs = %+v, want %+v", got, want)
	}
	middle, _ := Parse(`x {$1 or a} {$2}`)
	if specs := middle.ArgSpecs(); !specs[0].Optional || specs[1].Optional {
		t.Fatalf("a defaulted arg stays optional even before a required one: %+v", specs)
	}
	gap, _ := Parse(`x {$1} {$3}`)
	if specs := gap.ArgSpecs(); !specs[1].Optional || gap.MinArg != 2 {
		t.Fatalf("an unreferenced index is optional: %+v MinArg=%d", specs, gap.MinArg)
	}
}

func TestSpans(t *testing.T) {
	source := `cat {$fpath} | tail -n {$1 or "1 0"} {$2 or} {$3}`
	got := Spans(source)
	want := []struct {
		text  string
		kind  SpanKind
		index int
	}{
		{"{$fpath}", SpanSystem, 0},
		{`{$1 or "1 0"}`, SpanArg, 1},
		{"{$2 or}", SpanInvalid, 0},
		{"{$3}", SpanArg, 3},
	}
	if len(got) != len(want) {
		t.Fatalf("Spans = %+v", got)
	}
	for index, span := range got {
		if text := source[span.Start:span.End]; text != want[index].text || span.Kind != want[index].kind || span.Index != want[index].index {
			t.Errorf("span %d = %q kind=%d index=%d, want %+v", index, text, span.Kind, span.Index, want[index])
		}
	}
	quoted := `echo "{$1}" {$2} \{$3}`
	if spans := Spans(quoted); len(spans) != 3 || spans[0].Kind != SpanInvalid || spans[1].Kind != SpanArg || spans[1].Index != 2 || spans[2].Kind != SpanInvalid {
		t.Fatalf("quoted or escaped placeholders should be invalid: %+v", spans)
	}
	if spans := Spans("echo {$1 or"); len(spans) != 1 || spans[0].Kind != SpanInvalid || spans[0].End != len("echo {$1 or") {
		t.Fatalf("unclosed placeholder should span to the end: %+v", spans)
	}
}

func bindingText(bindings []Binding) string {
	var parts []string
	for _, binding := range bindings {
		part := fmt.Sprintf("$%d=%s", binding.Index, binding.Value)
		if binding.FromDefault {
			part += "*"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

func TestBindRequiredOnlyAndPositional(t *testing.T) {
	template, err := Parse(`printf '[%s]' {$1} {$2 or master} {$3} {$4 or ""}`)
	if err != nil {
		t.Fatal(err)
	}
	if template.MinArg != 2 || template.MaxArg != 4 {
		t.Fatalf("MinArg=%d MaxArg=%d", template.MinArg, template.MaxArg)
	}
	cases := []struct {
		args         []string
		wantBindings string
		wantRender   string
		wantErr      string
	}{
		{[]string{"a", "b"}, "$1=a $2=master* $3=b $4=*", "printf '[%s]' a master b ", ""},
		{[]string{"a", "b", "c"}, "$1=a $2=b $3=c $4=*", "printf '[%s]' a b c ", ""},
		{[]string{"a", "b", "c", "d"}, "$1=a $2=b $3=c $4=d", "printf '[%s]' a b c d", ""},
		{[]string{"a"}, "", "", "needs 2 args ($1, $3), got 1"},
		{[]string{"a", "b", "c", "d", "e"}, "", "", "takes at most 4 args, got 5 — put extra flags after --"},
	}
	for _, tc := range cases {
		bindings, err := template.Bind(tc.args)
		rendered, renderErr := template.Render(tc.args, nil, nil)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr || renderErr == nil {
				t.Errorf("Bind(%q) err = %v, want %q", tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil || bindingText(bindings) != tc.wantBindings {
			t.Errorf("Bind(%q) = %q, %v; want %q", tc.args, bindingText(bindings), err, tc.wantBindings)
		}
		if renderErr != nil || rendered != tc.wantRender {
			t.Errorf("Render(%q) = %q, %v; want %q", tc.args, rendered, renderErr, tc.wantRender)
		}
	}
}

func TestBindUncoveredRequiredArg(t *testing.T) {
	template, err := Parse(`x {$1} {$2 or p} {$3 or q} {$4}`)
	if err != nil {
		t.Fatal(err)
	}
	if bindings, err := template.Bind([]string{"a", "b"}); err != nil || bindingText(bindings) != "$1=a $2=p* $3=q* $4=b" {
		t.Errorf("required-only: %q %v", bindingText(bindings), err)
	}
	if _, err := template.Bind([]string{"a", "b", "c"}); err == nil || err.Error() != "with 3 args, $1–$3 are filled in order; $4 has no default" {
		t.Errorf("uncovered: %v", err)
	}
	if bindings, err := template.Bind([]string{"a", "b", "c", "d"}); err != nil || bindingText(bindings) != "$1=a $2=b $3=c $4=d" {
		t.Errorf("all typed: %q %v", bindingText(bindings), err)
	}
}

func TestSplitArgsExtra(t *testing.T) {
	cases := []struct {
		line            string
		wantArgs, wantX []string
	}{
		{"a -- -P 30", []string{"a"}, []string{"-P", "30"}},
		{"-- -v", nil, []string{"-v"}},
		{"a b --", []string{"a", "b"}, nil},
		{`a -- -m "two words"`, []string{"a"}, []string{"-m", "two words"}},
		{"-- a -- b", nil, []string{"a", "--", "b"}},
		{`'--' x`, []string{"--", "x"}, nil},
		{`"--" x`, []string{"--", "x"}, nil},
		{`\-- x`, []string{"--", "x"}, nil},
		{"a --x b", []string{"a", "--x", "b"}, nil},
	}
	for _, tc := range cases {
		args, extra, err := SplitArgs(tc.line)
		if err != nil || !slices.Equal(args, tc.wantArgs) || !slices.Equal(extra, tc.wantX) {
			t.Errorf("SplitArgs(%q) = %q %q %v, want %q %q", tc.line, args, extra, err, tc.wantArgs, tc.wantX)
		}
	}
}

func TestRenderExtra(t *testing.T) {
	cases := []struct {
		source string
		args   []string
		extra  []string
		want   string
	}{
		{"go test {$1}", []string{"./..."}, []string{"-P", "30"}, "go test ./... -P 30"},
		{"make test", nil, []string{"-v"}, "make test -v"},
		{"echo {$1}", []string{"a"}, []string{"two words", "a;b"}, "echo a 'two words' 'a;b'"},
		{"echo {$1}", []string{"a"}, nil, "echo a"},
		{"git log {$1 or \"\"}", nil, []string{"--oneline"}, "git log  --oneline"},
	}
	for _, tc := range cases {
		template, err := Parse(tc.source)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.source, err)
		}
		if got, err := template.Render(tc.args, tc.extra, nil); err != nil || got != tc.want {
			t.Errorf("Render(%q, %q, %q) = %q, %v; want %q", tc.source, tc.args, tc.extra, got, err, tc.want)
		}
	}
}

func TestFragments(t *testing.T) {
	cases := []struct {
		name, source     string
		args             []string
		want             string
		wantMin, wantMax int
	}{
		{"given", `cmd {#"-P $1"}`, []string{"30"}, "cmd -P 30", 0, 1},
		{"omitted", `cmd {#"-P $1"} x`, nil, "cmd  x", 0, 1},
		{"glued", `cmd {#"--parallel=$1"}`, []string{"4"}, "cmd --parallel=4", 0, 1},
		{"value quoted", `cmd {#"-m $1"}`, []string{"two words"}, "cmd -m 'two words'", 0, 1},
		{"suffix", `cmd {#"-o $1.out"}`, []string{"a b"}, "cmd -o 'a b'.out", 0, 1},
		{"spaces around closing", `cmd {#"-P $1"  }`, []string{"3"}, "cmd -P 3", 0, 1},
		{"after required", `cmd {$1} {#"-P $2"}`, []string{"a"}, "cmd a ", 1, 2},
		{"after required given", `cmd {$1} {#"-P $2"}`, []string{"a", "b"}, "cmd a -P b", 1, 2},
		{"required elsewhere", `cmd {$1} {#"-P $1"}`, []string{"a"}, "cmd a -P a", 1, 1},
		{"typed empty", `cmd {#"-P $1"}`, []string{""}, "cmd -P ''", 0, 1},
		{"shell length stays literal", `echo ${#HOME} {$1}`, []string{"a"}, "echo ${#HOME} a", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			template, err := Parse(tc.source)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if template.MinArg != tc.wantMin || template.MaxArg != tc.wantMax {
				t.Fatalf("MinArg=%d MaxArg=%d, want %d %d", template.MinArg, template.MaxArg, tc.wantMin, tc.wantMax)
			}
			if got, err := template.Render(tc.args, nil, nil); err != nil || got != tc.want {
				t.Fatalf("Render = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestFragmentErrors(t *testing.T) {
	cases := []struct{ source, wantErr string }{
		{`x {#"-P"}`, "needs an argument like $1"},
		{`x {#"$1 $2"}`, "exactly one argument"},
		{`x {#"-P $HOME"}`, "shell variables aren't allowed"},
		{`x {#"-P $"}`, "shell variables aren't allowed"},
		{`x {#"-P $0"}`, "args start at $1"},
		{`x {#"-P {$1}"}`, "not {$2}"},
		{`x {#"-P $1;rm"}`, `";" isn't allowed`},
		{`x {#"-P '$1'"}`, `"'" isn't allowed`},
		{`x {#"-P \$1"}`, `"\\" isn't allowed`},
		{`x {#"-P $1" or ""}`, "already empty"},
		{`x {#"-P $1" y}`, "unexpected"},
		{`x {#"-P $1`, "unclosed"},
		{`x {#"-P $1"`, "unclosed"},
		{`x "{#"-P $1"}"`, "inside"},
		{`x '{#"-P $1"}'`, "inside"},
		{`x ${#"-P $1"}`, "directly follow"},
	}
	for _, tc := range cases {
		if _, err := Parse(tc.source); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Parse(%q) = %v, want error containing %q", tc.source, err, tc.wantErr)
		}
	}
}

func TestFragmentSpansAndSpecs(t *testing.T) {
	source := `go test {$1} {#"-p $2"} {#"-run $3"`
	spans := Spans(source)
	if len(spans) != 3 || spans[1].Kind != SpanFragment || spans[1].Index != 2 || spans[2].Kind != SpanInvalid {
		t.Fatalf("Spans = %+v", spans)
	}
	if text := source[spans[1].Start:spans[1].End]; text != `{#"-p $2"}` {
		t.Fatalf("fragment span = %q", text)
	}
	if reference := source[spans[1].ReferenceStart:spans[1].ReferenceEnd]; reference != "$2" {
		t.Fatalf("reference = %q", reference)
	}

	template, err := Parse(`go test {$1} {#"-p $2"} {#"-x $2"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArgSpec{{Index: 1}, {Index: 2, Optional: true, Fragment: "-p $2"}}
	if got := template.ArgSpecs(); !slices.Equal(got, want) {
		t.Fatalf("ArgSpecs = %+v, want %+v", got, want)
	}
}
