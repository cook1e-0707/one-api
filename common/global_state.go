package common

import (
	"os"
	"sync"
	"time"

	"github.com/songquanpeng/one-api/common/logger"
)

var (
	// Global high load mode state
	isHighLoadMode bool
	highLoadMutex  sync.RWMutex
	
	// Ticker for monitoring high load flag file
	highLoadTicker *time.Ticker
	highLoadStop   chan bool
)

const (
	HighLoadFlagPath = "/tmp/high_load_flag"
	CheckInterval    = 5 * time.Second
)

// IsHighLoadMode returns the current high load mode status
func IsHighLoadMode() bool {
	highLoadMutex.RLock()
	defer highLoadMutex.RUnlock()
	return isHighLoadMode
}

// setHighLoadMode sets the high load mode status and logs changes
func setHighLoadMode(newStatus bool) {
	highLoadMutex.Lock()
	defer highLoadMutex.Unlock()
	
	if isHighLoadMode != newStatus {
		isHighLoadMode = newStatus
		logger.SysLogf("High load mode status changed to: %v", isHighLoadMode)
	}
}

// checkHighLoadFlag checks if the high load flag file exists
func checkHighLoadFlag() bool {
	_, err := os.Stat(HighLoadFlagPath)
	return err == nil
}

// StartHighLoadMonitor starts the background goroutine to monitor high load mode
func StartHighLoadMonitor() {
	logger.SysLog("Starting high load mode monitor")
	
	highLoadTicker = time.NewTicker(CheckInterval)
	highLoadStop = make(chan bool)
	
	// Initial check
	initialStatus := checkHighLoadFlag()
	setHighLoadMode(initialStatus)
	
	go func() {
		for {
			select {
			case <-highLoadTicker.C:
				currentStatus := checkHighLoadFlag()
				setHighLoadMode(currentStatus)
			case <-highLoadStop:
				highLoadTicker.Stop()
				logger.SysLog("High load mode monitor stopped")
				return
			}
		}
	}()
}

// StopHighLoadMonitor stops the high load mode monitor
func StopHighLoadMonitor() {
	if highLoadStop != nil {
		close(highLoadStop)
	}
} 