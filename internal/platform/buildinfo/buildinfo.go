// Package buildinfo holds compile-time build metadata and version payload types.
package buildinfo

import "runtime/debug"

// Compile-time metadata stamped via Go -ldflags -X.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Details holds the resolved build metadata for the running binary.
type Details struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

// Response is the JSON payload returned by /version and /api/version.
type Response struct {
	Version        string           `json:"version"`
	Commit         string           `json:"commit"`
	BuildTime      string           `json:"build_time"`
	SchemaVersions map[string]int64 `json:"schema_versions"`
}

// Info returns the build metadata, falling back to Go's embedded VCS settings
// when Commit or BuildTime were not stamped at link time.
func Info() Details {
	return resolveInfo(Version, Commit, BuildTime, debug.ReadBuildInfo)
}

func resolveInfo(version, commit, buildTime string, readBuildInfo func() (*debug.BuildInfo, bool)) Details {
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	if buildTime == "" {
		buildTime = "unknown"
	}
	if (commit == "unknown" || buildTime == "unknown") && readBuildInfo != nil {
		if bi, ok := readBuildInfo(); ok && bi != nil {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if commit == "unknown" && s.Value != "" {
						commit = s.Value
					}
				case "vcs.time":
					if buildTime == "unknown" && s.Value != "" {
						buildTime = s.Value
					}
				}
			}
		}
	}
	return Details{
		Version:   version,
		Commit:    commit,
		BuildTime: buildTime,
	}
}
