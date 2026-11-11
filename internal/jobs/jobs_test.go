package jobs

import "testing"

func TestDecide(t *testing.T) {
	for _, c := range []struct {
		local, running bool
		agent          string
		remote         bool
		want           string
	}{
		{true, false, "", false, Done},
		{false, false, "working", false, Stopped},
		{false, true, "working", true, Running},
		{false, true, "blocked", false, Blocked},
		{false, true, "idle", true, Done},
		{false, true, "done", false, Check},
		{false, true, "", false, Check},
	} {
		if got := Decide(c.local, c.running, c.agent, c.remote); got != c.want {
			t.Errorf("Decide(%v,%v,%q,%v) = %s, want %s", c.local, c.running, c.agent, c.remote, got, c.want)
		}
	}
}

func TestCreateLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for range 2 {
		if err := Create(&Job{WS: "ws-a", Cwd: "~/x", Prompt: "直して\n---\n本文"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].ID != "J2" || all[1].Agent != "mujin-j2" || all[0].Prompt != "直して\n---\n本文" {
		t.Fatalf("got %+v", all)
	}
}
