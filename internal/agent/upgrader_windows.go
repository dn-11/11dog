//go:build windows

package agent

import "errors"

type processUpgrader struct {
	exitCh chan struct{}
}

func newProcessUpgrader(_ string) (*processUpgrader, error) {
	return &processUpgrader{exitCh: make(chan struct{})}, nil
}

func (p *processUpgrader) Ready() error          { return nil }
func (p *processUpgrader) HasParent() bool       { return false }
func (p *processUpgrader) Upgrade() error        { return errors.New("tableflip self-upgrade is only supported on Unix/Linux") }
func (p *processUpgrader) Exit() <-chan struct{} { return p.exitCh }
func (p *processUpgrader) Stop()                 {}
