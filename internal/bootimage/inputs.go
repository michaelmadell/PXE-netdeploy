package bootimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
)

// Recipe is the build recipe version: bump it whenever a change to the
// build code changes what a build produces, so existing builds go stale.
const Recipe = 1

// Inputs is everything a build depends on (ADR 0022). Its JSON is hashed
// for inputHash and stored with the build to explain staleness.
type Inputs struct {
	Recipe         int           `json:"recipe"`
	BootWIM        string        `json:"bootWim"` // SHA-256 of the source boot.wim
	SourceIndex    int           `json:"sourceIndex"`
	Architecture   string        `json:"architecture"`
	Builder        string        `json:"builder"` // resolved: dism or wimlib
	Agent          string        `json:"agent"`   // SHA-256 of the agent binary
	ServerURLs     []string      `json:"serverUrls"`
	Bootmgr        string        `json:"bootmgr"`
	TFTPBlockSize  int           `json:"tftpBlockSize"`
	TFTPWindowSize int           `json:"tftpWindowSize"`
	Drivers        []DriverInput `json:"drivers"`
}

// DriverInput is one enabled INF: its package archive's hash and its path
// in the package (packages are immutable).
type DriverInput struct {
	Package string `json:"package"`
	Path    string `json:"path"`
}

// normalize sorts the drivers and replaces nil slices, so equal inputs
// marshal identically.
func (in *Inputs) normalize() {
	if in.Drivers == nil {
		in.Drivers = []DriverInput{}
	}
	if in.ServerURLs == nil {
		in.ServerURLs = []string{}
	}
	sort.Slice(in.Drivers, func(i, j int) bool {
		a, b := in.Drivers[i], in.Drivers[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Path < b.Path
	})
}

// JSON is the canonical document (struct field order, sorted drivers).
func (in Inputs) JSON() []byte {
	in.normalize()
	b, _ := json.Marshal(in)
	return b
}

// Hash is inputHash: the SHA-256 of JSON.
func (in Inputs) Hash() string {
	sum := sha256.Sum256(in.JSON())
	return hex.EncodeToString(sum[:])
}

// Stale reasons (BootImage.staleReasons).
const (
	ReasonRecipe       = "recipe"
	ReasonSourceImage  = "source-image"
	ReasonSourceIndex  = "source-index"
	ReasonArchitecture = "architecture"
	ReasonBuilder      = "builder"
	ReasonAgent        = "agent"
	ReasonServerURLs   = "server-urls"
	ReasonBootmgr      = "bootmgr"
	ReasonTFTP         = "tftp"
	ReasonDrivers      = "drivers"
)

// Diff lists what changed from built to current. Fields current leaves
// unknown ("" — e.g. the builder under `auto`, or an agent this build
// lacks) are not compared.
func Diff(built, current Inputs) []string {
	built.normalize()
	current.normalize()
	var out []string
	add := func(changed bool, reason string) {
		if changed {
			out = append(out, reason)
		}
	}
	add(built.Recipe != current.Recipe, ReasonRecipe)
	add(current.BootWIM != "" && built.BootWIM != current.BootWIM, ReasonSourceImage)
	add(built.SourceIndex != current.SourceIndex, ReasonSourceIndex)
	add(built.Architecture != current.Architecture, ReasonArchitecture)
	add(current.Builder != "" && built.Builder != current.Builder, ReasonBuilder)
	add(current.Agent != "" && built.Agent != current.Agent, ReasonAgent)
	add(!slices.Equal(built.ServerURLs, current.ServerURLs), ReasonServerURLs)
	add(built.Bootmgr != current.Bootmgr, ReasonBootmgr)
	add(built.TFTPBlockSize != current.TFTPBlockSize || built.TFTPWindowSize != current.TFTPWindowSize, ReasonTFTP)
	add(!slices.Equal(built.Drivers, current.Drivers), ReasonDrivers)
	return out
}
