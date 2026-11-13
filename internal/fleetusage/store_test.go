package fleetusage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func recordAt(ledger *Ledger, key string, at time.Time, total int64) {
	ledger.Record(namedContext(key), coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: at, Detail: coreusage.Detail{InputTokens: total, TotalTokens: total}})
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	ledger := NewLedger(dir, fixedClock)
	yesterday := fixedNow.Add(-24 * time.Hour)
	recordAt(ledger, "alpha", yesterday, 10)
	recordAt(ledger, "alpha", fixedNow, 5)
	ledger.Record(context.Background(), coreusage.Record{Provider: "claude", Model: "opus", RequestedAt: fixedNow, Failed: true})
	ledger.Flush()

	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v (%v), want 0700", info.Mode().Perm(), err)
	}
	names := listNames(t, dir)
	if strings.Join(names, ",") != "2026-11-12.json,2026-11-13.json" {
		t.Fatalf("day files = %v", names)
	}
	for _, name := range names {
		info, errStat := os.Stat(filepath.Join(dir, name))
		if errStat != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v (%v), want 0600", name, info.Mode().Perm(), errStat)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-11-13.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file dayFile
	if json.Unmarshal(raw, &file) != nil || file.Day != "2026-11-13" || len(file.Buckets) != 2 {
		t.Fatalf("unexpected day file: %s", raw)
	}

	reloaded := NewLedger(dir, fixedClock)
	reloaded.Load()
	from, to := fixedNow.Add(-48*time.Hour), fixedNow.Add(time.Hour)
	before, _ := json.Marshal(ledger.Usage(from, to, ""))
	after, _ := json.Marshal(reloaded.Usage(from, to, ""))
	if string(before) != string(after) {
		t.Fatalf("round trip changed usage:\n%s\n%s", before, after)
	}
	reloaded.mu.Lock()
	dirty := len(reloaded.dirty)
	reloaded.mu.Unlock()
	if dirty != 0 {
		t.Fatal("loaded days must not be dirty")
	}
}

func TestFlushRewritesOnlyChangedDays(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir, fixedClock)
	recordAt(ledger, "alpha", fixedNow.Add(-24*time.Hour), 1)
	recordAt(ledger, "alpha", fixedNow, 1)
	ledger.Flush()
	stat := func(name string) os.FileInfo {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	oldDay, today := stat("2026-11-12.json"), stat("2026-11-13.json")
	ledger.Flush()
	if !os.SameFile(today, stat("2026-11-13.json")) {
		t.Fatal("a clean ledger must not rewrite files")
	}
	recordAt(ledger, "alpha", fixedNow, 1)
	ledger.Flush()
	if !os.SameFile(oldDay, stat("2026-11-12.json")) {
		t.Fatal("an unchanged day was rewritten")
	}
	if os.SameFile(today, stat("2026-11-13.json")) {
		t.Fatal("the changed day was not rewritten")
	}
}

func TestAtomicWriteLeavesNoPartialFiles(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir, fixedClock)
	recordAt(ledger, "alpha", fixedNow, 1)
	blocker := filepath.Join(dir, "2026-11-13.json")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledger.Flush()
	if names := listNames(t, dir); len(names) != 1 || names[0] != "2026-11-13.json" {
		t.Fatalf("failed write left files behind: %v", names)
	}
	ledger.mu.Lock()
	_, dirty := ledger.dirty[dayStart(fixedNow).Unix()]
	ledger.mu.Unlock()
	if !dirty {
		t.Fatal("a failed write must keep the day dirty")
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	ledger.Flush()
	if names := listNames(t, dir); len(names) != 1 {
		t.Fatalf("retry left files behind: %v", names)
	}
	reloaded := NewLedger(dir, fixedClock)
	reloaded.Load()
	if usage := reloaded.Usage(fixedNow.Add(-time.Hour), fixedNow.Add(time.Hour), ""); len(usage) != 1 || usage[0].TotalTokens != 1 {
		t.Fatalf("retried write lost data: %+v", usage)
	}
}

func TestLoadQuarantinesCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-11-13.json", "{not json")
	write("2026-11-12.json", `{"day":"2026-11-11","buckets":[]}`)
	write("2026-11-11.json", `{"day":"2026-11-11","buckets":[{"hour":"2026-11-12T01:00:00Z","key":"a","provider":"p","model":"m","requests":1}]}`)
	write("2026-11-10.json", `{"day":"2026-11-10","buckets":[{"hour":"2026-11-10T01:00:00Z","key":"a","provider":"p","model":"m","requests":3,"total_tokens":9}]}`)
	write(".2026-11-09.json.tmp-123", "partial")
	write("notes.txt", "kept")

	ledger := NewLedger(dir, fixedClock)
	ledger.Load()

	suffix := ".corrupt-" + strconv.FormatInt(fixedNow.Unix(), 10)
	want := map[string]bool{
		"2026-11-13.json" + suffix: true,
		"2026-11-12.json" + suffix: true,
		"2026-11-11.json" + suffix: true,
		"2026-11-10.json":          true,
		"notes.txt":                true,
	}
	names := listNames(t, dir)
	if len(names) != len(want) {
		t.Fatalf("files after load = %v", names)
	}
	for _, name := range names {
		if !want[name] {
			t.Fatalf("unexpected file %s in %v", name, names)
		}
	}
	usage := ledger.Usage(fixedNow.Add(-10*24*time.Hour), fixedNow.Add(time.Hour), "")
	if len(usage) != 1 || usage[0].Requests != 3 || usage[0].TotalTokens != 9 {
		t.Fatalf("valid day must load, corrupt days start empty: %+v", usage)
	}
	recordAt(ledger, "alpha", fixedNow, 4)
	ledger.Flush()
	reloaded := NewLedger(dir, fixedClock)
	reloaded.Load()
	if got := reloaded.Usage(fixedNow.Add(-time.Hour), fixedNow.Add(time.Hour), "alpha"); len(got) != 1 || got[0].TotalTokens != 4 {
		t.Fatalf("a quarantined day must accept new data: %+v", got)
	}
}

func TestRetention(t *testing.T) {
	dir := t.TempDir()
	cutoff := retentionCutoff(fixedNow)
	expired := cutoff.Add(-24 * time.Hour)
	for _, day := range []time.Time{expired, cutoff} {
		file := dayFile{Day: day.Format(dayLayout), Buckets: []fileBucket{{Hour: day, Key: "a", Provider: "p", Model: "m", Counters: Counters{Requests: 1}}}}
		raw, _ := json.Marshal(file)
		if err := os.WriteFile(filepath.Join(dir, dayFileName(day)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, dayFileName(expired)+".corrupt-1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := fixedNow
	ledger := NewLedger(dir, func() time.Time { return now })
	ledger.Load()
	if names := listNames(t, dir); len(names) != 1 || names[0] != dayFileName(cutoff) {
		t.Fatalf("expired files must be deleted at load: %v", names)
	}
	recordAt(ledger, "a", expired, 1)
	if got := ledger.Usage(expired, fixedNow, ""); len(got) != 1 || got[0].Requests != 1 {
		t.Fatalf("records older than the retention must be dropped: %+v", got)
	}

	now = fixedNow.Add(24 * time.Hour)
	ledger.Prune()
	if names := listNames(t, dir); len(names) != 0 {
		t.Fatalf("daily prune must delete expired files: %v", names)
	}
	if got := ledger.Usage(expired, now, ""); len(got) != 0 {
		t.Fatalf("daily prune must drop expired days from memory: %+v", got)
	}
}

func TestResolveDir(t *testing.T) {
	t.Run("writable path outside auth dir", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("WRITABLE_PATH", base)
		cfg := &config.Config{}
		cfg.AuthDir = filepath.Join(t.TempDir(), "auths")
		if got := ResolveDir(cfg); got != filepath.Join(base, DirName) {
			t.Fatalf("ResolveDir = %s", got)
		}
	})
	t.Run("logs inside auth dir", func(t *testing.T) {
		authDir := filepath.Join(t.TempDir(), ".cli-proxy-api")
		t.Setenv("WRITABLE_PATH", authDir)
		cfg := &config.Config{}
		cfg.AuthDir = authDir
		if got := ResolveDir(cfg); got != authDir+"-"+DirName {
			t.Fatalf("ResolveDir = %s, want a sibling of the auth dir", got)
		}
	})
	t.Run("default layout", func(t *testing.T) {
		t.Setenv("WRITABLE_PATH", "")
		t.Chdir(t.TempDir())
		authDir := filepath.Join(t.TempDir(), ".cli-proxy-api")
		cfg := &config.Config{}
		cfg.AuthDir = authDir
		if got := ResolveDir(cfg); got != authDir+"-"+DirName {
			t.Fatalf("ResolveDir = %s, want a sibling of the auth dir", got)
		}
	})
	if !within("/a/b/c", "/a/b") || !within("/a/b", "/a/b") || within("/a/bc", "/a/b") || within("/a", "/a/b") {
		t.Fatal("within is wrong")
	}
}

func TestStartFlushesPeriodicallyAndOnStop(t *testing.T) {
	dir := t.TempDir()
	ledger := startLedger(context.Background(), NewLedger(dir, nil), 10*time.Millisecond)
	t.Cleanup(Stop)
	if Active() != ledger {
		t.Fatal("started ledger must be active")
	}
	usagePlugin{}.HandleUsage(namedContext("alpha"), coreusage.Record{Provider: "codex", Model: "gpt-6", Detail: coreusage.Detail{TotalTokens: 3}})
	deadline := time.Now().Add(5 * time.Second)
	for len(listNames(t, dir)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("periodic flush did not write a day file")
		}
		time.Sleep(10 * time.Millisecond)
	}
	usagePlugin{}.HandleUsage(namedContext("alpha"), coreusage.Record{Provider: "codex", Model: "gpt-6", Detail: coreusage.Detail{TotalTokens: 4}})
	Stop()
	reloaded := NewLedger(dir, nil)
	reloaded.Load()
	now := time.Now()
	if got := reloaded.Usage(now.Add(-2*time.Hour), now.Add(2*time.Hour), "alpha"); len(got) != 1 || got[0].TotalTokens != 7 {
		t.Fatalf("stop must flush pending counts: %+v", got)
	}
}
