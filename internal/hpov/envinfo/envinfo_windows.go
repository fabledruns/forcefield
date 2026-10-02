//go:build windows

package envinfo

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatus   = modKernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemPowerStatus = modKernel32.NewProc("GetSystemPowerStatus")
)

func osVersion() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return ""
	}
	return itoa(uint(v.MajorVersion)) + "." + itoa(uint(v.MinorVersion)) + "." + itoa(uint(v.BuildNumber))
}

func itoa(u uint) string {
	if u == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

func cpuModel() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	s, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func physicalCores() *int { return nil }

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memoryTotal() *int64 {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r1, _, _ := procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&st)))
	if r1 == 0 {
		return nil
	}
	v := int64(st.TotalPhys)
	return &v
}

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func powerSource() string {
	var st systemPowerStatus
	r1, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st)))
	if r1 == 0 {
		return "unknown"
	}
	switch st.ACLineStatus {
	case 1:
		return "ac"
	case 0:
		return "battery"
	default:
		return "unknown"
	}
}

// powerPlan shells to powercfg (best-effort); failures stay "unknown".
func powerPlan() string {
	p, err := exec.LookPath("powercfg")
	if err != nil {
		return "unknown"
	}
	_ = p
	out, err := exec.Command("powercfg", "/getactivescheme").Output()
	if err != nil {
		return "unknown"
	}
	s := strings.TrimSpace(string(out))
	// "Power Scheme GUID: xxx  (Balanced)" — keep the parenthesized name.
	if i := strings.LastIndex(s, "("); i >= 0 {
		if j := strings.Index(s[i:], ")"); j >= 0 {
			return strings.ToLower(strings.TrimSpace(s[i+1 : i+j]))
		}
	}
	if s != "" {
		return "unknown"
	}
	return "unknown"
}

func fsKind(workroot string) string {
	abs, err := filepath.Abs(workroot)
	if err != nil {
		return "unknown"
	}
	root := filepath.VolumeName(abs) + `\`
	root16, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "unknown"
	}
	var fsName [32]uint16
	// GetVolumeInformation(root, volName, ..., fsName, ...).
	r1, _, _ := procGetVolumeInfo.Call(
		uintptr(unsafe.Pointer(root16)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&fsName[0])),
		uintptr(len(fsName)),
	)
	_ = r1
	name := windows.UTF16ToString(fsName[:])
	if name == "" {
		return "unknown"
	}
	return strings.ToLower(name)
}

var procGetVolumeInfo = modKernel32.NewProc("GetVolumeInformationW")

func isWSL() bool { return false }

func isVM() bool { return false }

// idleCPU samples whole-system busy % via GetSystemTimes over d.
func idleCPU(d time.Duration) *float64 {
	sample := func() (idle, total uint64, ok bool) {
		var i, k, u windows.Filetime
		r1, _, _ := procGetSystemTimes.Call(
			uintptr(unsafe.Pointer(&i)),
			uintptr(unsafe.Pointer(&k)),
			uintptr(unsafe.Pointer(&u)),
		)
		if r1 == 0 {
			return 0, 0, false
		}
		fi := uint64(i.HighDateTime)<<32 | uint64(i.LowDateTime)
		fk := uint64(k.HighDateTime)<<32 | uint64(k.LowDateTime)
		fu := uint64(u.HighDateTime)<<32 | uint64(u.LowDateTime)
		return fi, fk + fu, true
	}
	i0, t0, ok := sample()
	if !ok {
		return nil
	}
	time.Sleep(d)
	i1, t1, ok := sample()
	if !ok || t1 <= t0 {
		return nil
	}
	v := (1 - float64(i1-i0)/float64(t1-t0)) * 100
	return &v
}
