//go:build windows

package main

import (
	"context"
	"log"

	"golang.org/x/sys/windows/svc"
)

// runAsService runs the agent under the Windows service manager when started as a service and
// reports whether it did.
func runAsService(a *agent) bool {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false
	}
	if err := svc.Run("MyCaffePrintAgent", &service{agent: a}); err != nil {
		log.Fatalf("[PRINT AGENT] service: %v", err)
	}
	return true
}

type service struct{ agent *agent }

func (s *service) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.agent.run(ctx); close(done) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			status <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			cancel()
			<-done
			return false, 0
		}
	}
	cancel()
	return false, 0
}
