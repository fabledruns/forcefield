//go:build !windows && !linux

package envinfo

import "time"

func osVersion() string              { return "" }
func cpuModel() string               { return "" }
func physicalCores() *int            { return nil }
func memoryTotal() *int64            { return nil }
func powerSource() string            { return "unknown" }
func powerPlan() string              { return "unknown" }
func fsKind(string) string           { return "unknown" }
func isWSL() bool                    { return false }
func isVM() bool                     { return false }
func idleCPU(time.Duration) *float64 { return nil }
