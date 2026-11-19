package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AnaTofuZ/mujin/internal/coder"
)

// 同じ setup は 1 回だけ、失敗した setup と書き換えた setup は次も流れることを、ローカルの bash で確かめる。
func TestSetupScriptOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	count := filepath.Join(home, "count")
	run := func(s string) error { return side("", setupScript(s)).Run() }
	read := func() string { b, _ := os.ReadFile(count); return string(b) }

	if run("exit 3") == nil {
		t.Fatal("setup の失敗を見逃した")
	}
	a := "echo a >>" + count
	for range 2 {
		if err := run(a); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(); got != "a\n" {
		t.Fatalf("同じ setup が %q と流れた", got)
	}
	if err := run("echo b >>" + count); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "a\nb\n" {
		t.Fatalf("書き換えた setup が流れない: %q", got)
	}
}

// 実際の herdr machine list の出力から、SSH ターゲットが一致する行の ID だけを拾う。ラベルが同名でも別ホストなら拾わない。
func TestHerdrIDs(t *testing.T) {
	list := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tdev-1.coder\tdev-1.coder\tdefault\tenabled\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\tdev-2\tdev-2.coder\tdefault\tenabled\n" +
		"cccccccccccccccccccccccccccccccc\tdev-1\tother-host\tdefault\tenabled\n"
	if got := herdrIDs(list, "dev-1.coder"); !slices.Equal(got, []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}) {
		t.Fatalf("got %v", got)
	}
	if got := herdrIDs(list, "dev-9.coder"); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestRecreateArgs(t *testing.T) {
	var w coder.Workspace
	w.Name, w.TemplateName = "ws1", "workspace"
	got := strings.Join(recreateArgs(w, []coder.Parameter{{Name: "size", Value: "4"}, {Name: "gcp_project", Value: ""}}), " ")
	if want := "create ws1 --template workspace --parameter size=4 --parameter gcp_project= --yes"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// 分かれて届いた URL は最後まで届いてから 1 回だけ開き、出力はそのまま流す。
func TestURLOpener(t *testing.T) {
	var out strings.Builder
	var opened []string
	o := &urlOpener{w: &out, open: func(u string) { opened = append(opened, u) }}
	in := []string{"If the browser didn't open, visit: https://claude.ai/oauth/auth", "orize?code=true&state=x\x1b[0m\r\n", "Paste code: https://other.example/ \n"}
	for _, p := range in {
		if _, err := o.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"https://claude.ai/oauth/authorize?code=true&state=x"}; !slices.Equal(opened, want) {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	if out.String() != strings.Join(in, "") {
		t.Fatalf("出力が変わった: %q", out.String())
	}
}
