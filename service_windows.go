//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const windowsServiceName = "RESPProxy"

func runService(args []string) (bool, error) {
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "install":
			if len(args) != 1 {
				return true, errors.New("usage: RESPProxy.exe install")
			}
			return true, installWindowsService()
		case "uninstall":
			if len(args) != 1 {
				return true, errors.New("usage: RESPProxy.exe uninstall")
			}
			return true, uninstallWindowsService()
		case "start":
			if len(args) != 1 {
				return true, errors.New("usage: RESPProxy.exe start")
			}
			return true, controlWindowsService(svc.Cmd(0), true)
		case "stop":
			if len(args) != 1 {
				return true, errors.New("usage: RESPProxy.exe stop")
			}
			return true, controlWindowsService(svc.Stop, false)
		case "status":
			if len(args) != 1 {
				return true, errors.New("usage: RESPProxy.exe status")
			}
			return true, showWindowsServiceStatus()
		default:
			return true, errors.New("usage: RESPProxy.exe [install|uninstall|start|stop|status]")
		}
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		return true, fmt.Errorf("detect Windows service context: %w", err)
	}
	if !isService {
		return false, nil
	}
	if err := svc.Run(windowsServiceName, &proxyWindowsService{}); err != nil {
		return true, fmt.Errorf("run Windows service: %w", err)
	}
	return true, nil
}

type proxyWindowsService struct{}

func (*proxyWindowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status := svc.Status{State: svc.StartPending, WaitHint: 10_000}
	statuses <- status
	eventLog, err := eventlog.Open(windowsServiceName)
	if err != nil {
		statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return false, 1
	}
	defer eventLog.Close()
	previousOutput := log.Writer()
	log.SetOutput(&windowsEventLogWriter{eventLog: eventLog})
	defer log.SetOutput(previousOutput)
	log.Printf("%s service starting", windowsServiceName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startupProgress := time.NewTicker(5 * time.Second)
	defer startupProgress.Stop()
	ready := make(chan error, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- runProxy(ctx, ready, false)
	}()

	for {
		select {
		case err := <-ready:
			if err != nil {
				<-finished
				log.Printf("%s service failed during startup: %v", windowsServiceName, err)
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			status = svc.Status{State: svc.Running, Accepts: accepted}
			statuses <- status
			log.Printf("%s service running", windowsServiceName)
		case <-startupProgress.C:
			if status.State == svc.StartPending {
				status.CheckPoint++
				status.WaitHint = 10_000
				statuses <- status
			}
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- status
			case svc.Stop, svc.Shutdown:
				status = svc.Status{State: svc.StopPending, WaitHint: 30_000}
				statuses <- status
				cancel()
				err := waitForProxyShutdown(finished, requests, statuses)
				exitCode := uint32(0)
				if err != nil {
					exitCode = 1
					log.Printf("%s service stopped with an error: %v", windowsServiceName, err)
				} else {
					log.Printf("%s service stopped", windowsServiceName)
				}
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: exitCode}
				return false, exitCode
			}
		case err := <-finished:
			exitCode := uint32(0)
			if err != nil {
				exitCode = 1
				log.Printf("%s service exited with an error: %v", windowsServiceName, err)
			}
			statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: exitCode}
			return false, exitCode
		}
	}
}

func waitForProxyShutdown(finished <-chan error, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	status := svc.Status{State: svc.StopPending, WaitHint: 30_000, CheckPoint: 1}
	for {
		select {
		case err := <-finished:
			return err
		case request := <-requests:
			if request.Cmd == svc.Interrogate {
				statuses <- status
			}
		case <-ticker.C:
			status.CheckPoint++
			statuses <- status
		}
	}
}

type windowsEventLogWriter struct {
	eventLog *eventlog.Log
}

func (w *windowsEventLogWriter) Write(message []byte) (int, error) {
	text := strings.TrimSpace(string(message))
	var err error
	if strings.Contains(strings.ToLower(text), "error") || strings.Contains(strings.ToLower(text), "failed") {
		err = w.eventLog.Error(1, text)
	} else {
		err = w.eventLog.Info(1, text)
	}
	if err != nil {
		return 0, err
	}
	return len(message), nil
}

func installWindowsService() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager (run elevated): %w", err)
	}
	defer manager.Disconnect()

	service, err := manager.CreateService(windowsServiceName, executable, mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        windows.SERVICE_AUTO_START,
		ErrorControl:     windows.SERVICE_ERROR_NORMAL,
		ServiceStartName: `NT AUTHORITY\LocalService`,
		DisplayName:      "RESPProxy",
		Description:      "RESP2 TCP proxy for routing standalone Redis clients to Redis Cluster.",
	})
	if err != nil {
		return fmt.Errorf("install %s service: %w", windowsServiceName, err)
	}
	defer service.Close()
	if err := eventlog.InstallAsEventCreate(windowsServiceName, windows.EVENTLOG_ERROR_TYPE|windows.EVENTLOG_WARNING_TYPE|windows.EVENTLOG_INFORMATION_TYPE); err != nil {
		_ = service.Delete()
		return fmt.Errorf("register Windows Event Log source: %w", err)
	}
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := service.SetRecoveryActions(actions, 24*60*60); err != nil {
		_ = service.Delete()
		_ = eventlog.Remove(windowsServiceName)
		return fmt.Errorf("configure service recovery actions: %w", err)
	}
	fmt.Printf("Installed %s service. Configure its environment before starting it.\n", windowsServiceName)
	return nil
}

func uninstallWindowsService() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager (run elevated): %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsServiceName)
	if err != nil {
		return fmt.Errorf("open %s service: %w", windowsServiceName, err)
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query %s service: %w", windowsServiceName, err)
	}
	if status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop %s service before uninstalling: %w", windowsServiceName, err)
		}
		if err := waitForWindowsServiceState(service, svc.Stopped, 30*time.Second); err != nil {
			return err
		}
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("uninstall %s service: %w", windowsServiceName, err)
	}
	if err := eventlog.Remove(windowsServiceName); err != nil {
		return fmt.Errorf("remove Windows Event Log source: %w", err)
	}
	fmt.Printf("Uninstalled %s service.\n", windowsServiceName)
	return nil
}

func controlWindowsService(command svc.Cmd, start bool) error {
	if start {
		return withWindowsService(func(service *mgr.Service) error {
			if err := service.Start(); err != nil {
				return fmt.Errorf("start %s service: %w", windowsServiceName, err)
			}
			if err := waitForWindowsServiceState(service, svc.Running, 30*time.Second); err != nil {
				return err
			}
			fmt.Printf("%s service is running.\n", windowsServiceName)
			return nil
		})
	}
	return withWindowsService(func(service *mgr.Service) error {
		if _, err := service.Control(command); err != nil {
			return fmt.Errorf("stop %s service: %w", windowsServiceName, err)
		}
		if err := waitForWindowsServiceState(service, svc.Stopped, 30*time.Second); err != nil {
			return err
		}
		fmt.Printf("%s service stopped.\n", windowsServiceName)
		return nil
	})
}

func showWindowsServiceStatus() error {
	return withWindowsService(func(service *mgr.Service) error {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("query %s service: %w", windowsServiceName, err)
		}
		fmt.Printf("%s service state: %v\n", windowsServiceName, status.State)
		return nil
	})
}

func withWindowsService(action func(*mgr.Service) error) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsServiceName)
	if err != nil {
		return fmt.Errorf("open %s service: %w", windowsServiceName, err)
	}
	defer service.Close()
	return action(service)
}

func waitForWindowsServiceState(service *mgr.Service, wanted svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("query %s service state: %w", windowsServiceName, err)
		}
		if status.State == wanted {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s service to reach state %v", windowsServiceName, wanted)
}
