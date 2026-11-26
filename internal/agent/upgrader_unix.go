//go:build !windows

package agent

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudflare/tableflip"
)

type processUpgrader struct {
	upg *tableflip.Upgrader
}

func newProcessUpgrader(pidFile string) (*processUpgrader, error) {
	upg, err := tableflip.New(tableflip.Options{
		UpgradeTimeout: 30 * time.Second,
		PIDFile:        pidFile,
	})
	if err != nil {
		return nil, err
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			_ = upg.Upgrade()
		}
	}()
	return &processUpgrader{upg: upg}, nil
}

func (p *processUpgrader) Ready() error          { return p.upg.Ready() }
func (p *processUpgrader) HasParent() bool       { return p.upg.HasParent() }
func (p *processUpgrader) Upgrade() error        { return p.upg.Upgrade() }
func (p *processUpgrader) Exit() <-chan struct{} { return p.upg.Exit() }
func (p *processUpgrader) Stop()                 { p.upg.Stop() }
