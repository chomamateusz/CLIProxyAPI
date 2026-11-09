package watcher

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestWatcherRemovedConfigRetainsConfigurationWithoutError(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if err := writeIssue6631Config(configPath, authDir, 1); err != nil {
		t.Fatal(err)
	}
	previousHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	hook := logtest.NewLocal(log.StandardLogger())
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previousHooks) })
	w, err := NewWatcher(configPath, authDir, func(*config.Config) { t.Error("removed config triggered a reload") })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(); err != nil {
			t.Error(err)
		}
	})
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	w.SetConfig(cfg)
	before := w.config
	if err := os.Rename(configPath, configPath+".saved"); err != nil {
		t.Fatal(err)
	}
	w.ReloadConfigIfChanged()
	if w.config != before {
		t.Fatal("removed config replaced the active configuration")
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level <= log.ErrorLevel {
			t.Fatal("removed config produced an error log")
		}
	}
}
