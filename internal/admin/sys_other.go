//go:build !windows && !linux
// +build !windows,!linux

package admin

// getProcessCPUUsage is not available on this platform.
func getProcessCPUUsage() float64 { return 0 }

// getSystemCPUUsage is not available on this platform.
func getSystemCPUUsage() float64 { return 0 }

// getSystemMemoryGB is not available on this platform.
func getSystemMemoryGB() (float64, float64) { return 0, 0 }

// getDiskFreeGB is not available on this platform.
func getDiskFreeGB(string) (float64, float64) { return 0, 0 }

// getAllDiskInfo is not available on this platform.
func getAllDiskInfo() []diskInfo { return nil }

// getNetworkIO is not available on this platform.
func getNetworkIO() (uint64, uint64) { return 0, 0 }
