package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestWatcherConfigAtomicReplaceRetainsWatch(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			testWatcherConfigAtomicReplace(t, symlink)
		})
	}
}

func testWatcherConfigAtomicReplace(t *testing.T, symlink bool) {
	configPath, authDir := newIssue6631ConfigFile(t)
	content := func(strategy string, affinity bool) []byte {
		return []byte(fmt.Sprintf("auth-dir: %q\nrouting:\n  strategy: %s\n  session-affinity: %t\n", filepath.ToSlash(authDir), strategy, affinity))
	}
	if err := os.WriteFile(configPath, content("round-robin", false), 0o600); err != nil {
		t.Fatal(err)
	}
	writePath := configPath
	if symlink {
		configPath = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.Symlink(writePath, configPath); err != nil {
			t.Fatal(err)
		}
	}
	type routing struct {
		strategy string
		affinity bool
	}
	applied := make(chan routing, 16)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { applied <- routing{cfg.Routing.Strategy, cfg.Routing.SessionAffinity} })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if err := w.Stop(); err != nil {
			t.Error(err)
		}
	})
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	w.SetConfig(cfg)
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	await := func(want routing) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case got := <-applied:
				if got == want {
					return
				}
			case <-deadline.C:
				t.Fatalf("routing reload not observed: %+v", want)
			}
		}
	}
	await(routing{"round-robin", false})
	for _, next := range []routing{{"use-or-lose", true}, {"fill-first", false}} {
		replacement := writePath + ".replacement"
		if err := os.WriteFile(replacement, content(next.strategy, next.affinity), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, writePath); err != nil {
			t.Fatal(err)
		}
		await(next)
	}
	if err := os.WriteFile(writePath, content("use-or-lose", true), 0o600); err != nil {
		t.Fatal(err)
	}
	await(routing{"use-or-lose", true})
}
