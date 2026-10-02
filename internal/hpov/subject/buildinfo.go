package subject

import (
	"debug/buildinfo"
)

// buildData is the provenance subset HPOV records.
type buildData struct {
	GoVersion string
	GOOS      string
	GOARCH    string
	Revision  string
	Modified  bool
}

// readBuildInfo reads the subject binary's embedded build metadata
// with stdlib debug/buildinfo: no toolchain required.
func readBuildInfo(path string) (buildData, error) {
	var out buildData
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return out, err
	}
	out.GoVersion = bi.GoVersion
	for _, s := range bi.Settings {
		switch s.Key {
		case "GOOS":
			out.GOOS = s.Value
		case "GOARCH":
			out.GOARCH = s.Value
		case "vcs.revision":
			out.Revision = s.Value
		case "vcs.modified":
			out.Modified = s.Value == "true"
		}
	}
	return out, nil
}
