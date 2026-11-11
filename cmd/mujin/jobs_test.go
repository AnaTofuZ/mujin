package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// origin から clone した 2 つのリポジトリの間で、ジョブのブランチを bundle で渡す。
func TestFetchBundle(t *testing.T) {
	dir := t.TempDir()
	git := func(d string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", d, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin, src, dst := filepath.Join(dir, "origin"), filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	git(dir, "init", "-q", "-b", "main", origin)
	git(origin, "commit", "-q", "--allow-empty", "-m", "base")
	git(dir, "clone", "-q", origin, src)
	git(dir, "clone", "-q", origin, dst)
	git(src, "switch", "-q", "-c", "mujin/J1")
	git(src, "commit", "-q", "--allow-empty", "-m", "work")

	if err := pipe(side("", bundleScript(src, "mujin/J1")), side("", fetchScript(dst, "mujin/J1"))); err != nil {
		t.Fatal(err)
	}
	if got, want := git(dst, "rev-parse", "mujin/J1"), git(src, "rev-parse", "mujin/J1"); got != want {
		t.Fatalf("dst の mujin/J1 = %s, want %s", got, want)
	}

	// 渡し先が分岐点のコミットを持っていなければ失敗する
	git(origin, "commit", "-q", "--allow-empty", "-m", "newer")
	git(src, "fetch", "-q")
	git(src, "rebase", "-q", "origin/main")
	if err := pipe(side("", bundleScript(src, "mujin/J1")), side("", fetchScript(dst, "mujin/J1"))); err == nil {
		t.Fatal("前提のコミットがないのに成功した")
	}
}

// ホーム以下のリポジトリを拾い、隠しディレクトリと node_modules の中は拾わない。
func TestPlacesScript(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"src/app", ".cache/x", "src/app/node_modules/dep"} {
		if out, err := exec.Command("git", "init", "-q", filepath.Join(home, d)).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	cmd := side("", placesScript)
	cmd.Env = append(cmd.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "- ~/src/app (origin なし)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
