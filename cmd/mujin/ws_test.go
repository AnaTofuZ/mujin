package main

import (
	"os"
	"path/filepath"
	"testing"
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
