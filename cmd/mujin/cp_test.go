package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitEndpoint(t *testing.T) {
	for in, want := range map[string][2]string{
		"ws-a:~/dump.sql": {"ws-a", "~/dump.sql"},
		"./out/":          {"", "./out/"},
		"./a:b":           {"", "./a:b"},
		":x":              {"", ":x"},
	} {
		if ws, p := splitEndpoint(in); ws != want[0] || p != want[1] {
			t.Errorf("splitEndpoint(%q) = %q, %q", in, ws, p)
		}
	}
}

// ローカル同士で pipe と tar のスクリプトを通す。リモート側も同じスクリプトを bash -lc で実行する。
func TestPipeLocal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "it's here")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := pipe(side("", sendScript(src)), side("", recvScript(dst))); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "it's here")); err != nil || string(b) != "hi" {
		t.Fatalf("got %q, %v", b, err)
	}
	if err := pipe(side("", "exit 3"), side("", "cat >/dev/null")); err == nil {
		t.Fatal("送信側の失敗を見逃した")
	}
}
