package fleetusage

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	DirName       = "fleet-key-usage"
	flushInterval = time.Minute
)

var active atomic.Pointer[Ledger]

var runner struct {
	mu     sync.Mutex
	ledger *Ledger
	cancel context.CancelFunc
	done   chan struct{}
}

type usagePlugin struct{}

func init() {
	coreusage.RegisterPlugin(usagePlugin{})
}

func (usagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if ledger := active.Load(); ledger != nil {
		ledger.Record(ctx, record)
	}
}

func Active() *Ledger { return active.Load() }

// ResolveDir places the ledger beside the logs directory unless that lands inside
// the auth directory: the file token store walks the auth directory recursively
// and would load every day file there as a credential.
func ResolveDir(cfg *config.Config) string {
	dir := absolute(filepath.Join(filepath.Dir(logging.ResolveLogDirectory(cfg)), DirName))
	if cfg == nil {
		return dir
	}
	authDir, errAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errAuthDir != nil || strings.TrimSpace(authDir) == "" {
		return dir
	}
	authDir = absolute(authDir)
	if !within(dir, authDir) {
		return dir
	}
	return authDir + "-" + DirName
}

func absolute(path string) string {
	if resolved, errAbs := filepath.Abs(path); errAbs == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func within(path, root string) bool {
	rel, errRel := filepath.Rel(root, path)
	if errRel != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func Start(ctx context.Context, dir string) *Ledger {
	return startLedger(ctx, NewLedger(dir, nil), flushInterval)
}

func startLedger(ctx context.Context, ledger *Ledger, interval time.Duration) *Ledger {
	Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	if errDir := ensureDir(ledger.dir); errDir != nil {
		log.Warnf("fleet key usage: cannot prepare %s: %v", ledger.dir, errDir)
	}
	ledger.Load()
	ledger.Prune()
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	runner.mu.Lock()
	runner.ledger, runner.cancel, runner.done = ledger, cancel, done
	runner.mu.Unlock()
	active.Store(ledger)
	go ledger.run(loopCtx, interval, done)
	log.Infof("fleet key usage: recording to %s", ledger.dir)
	return ledger
}

func Stop() {
	runner.mu.Lock()
	ledger, cancel, done := runner.ledger, runner.cancel, runner.done
	runner.ledger, runner.cancel, runner.done = nil, nil, nil
	runner.mu.Unlock()
	if ledger == nil {
		return
	}
	cancel()
	<-done
	ledger.Flush()
}

func (l *Ledger) run(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastPrune := dayStart(l.now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Flush()
			if today := dayStart(l.now()); today.After(lastPrune) {
				l.Prune()
				lastPrune = today
			}
		}
	}
}
