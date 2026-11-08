// Package wire holds the agent API's JSON documents as the agent sees them
// (api/openapi.yaml, ADR 0025). Hand-written so the agent doesn't link the
// server's generated code; a server test checks they match the spec.
package wire

import "time"

// Identity is AgentIdentity.
type Identity struct {
	MACs         []string `json:"macs"`
	SMBIOSUUID   *string  `json:"smbiosUuid,omitempty"`
	Serial       *string  `json:"serial,omitempty"`
	Manufacturer *string  `json:"manufacturer,omitempty"`
	Model        *string  `json:"model,omitempty"`
	AgentVersion string   `json:"agentVersion"`
}

// Token is AgentToken.
type Token struct {
	Token        string    `json:"token"`
	DeploymentID string    `json:"deploymentId"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// CheckInResult is AgentCheckInResult.
type CheckInResult struct {
	State    string `json:"state"` // armed or locked
	ClientID string `json:"clientId"`
	Armed    *struct {
		Token
		ProfileName      string `json:"profileName"`
		CountdownSeconds int    `json:"countdownSeconds"`
	} `json:"armed"`
	Prompt *struct {
		RequirePassword    bool `json:"requirePassword"`
		PasswordConfigured bool `json:"passwordConfigured"`
		TimeoutSeconds     int  `json:"timeoutSeconds"`
	} `json:"prompt"`
}

// Profile is an AgentUnlockResult menu entry.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UnlockResult is AgentUnlockResult.
type UnlockResult struct {
	SessionToken     string    `json:"sessionToken"`
	ExpiresAt        time.Time `json:"expiresAt"`
	DefaultProfileID *string   `json:"defaultProfileId"`
	Profiles         []Profile `json:"profiles"`
}

// File is a downloadable file of a plan.
type File struct {
	Path       string `json:"path,omitempty"`
	ContentURL string `json:"contentUrl"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

// Plan is AgentDeploymentPlan.
type Plan struct {
	DeploymentID        string  `json:"deploymentId"`
	InstallMethod       string  `json:"installMethod"`
	Image               File    `json:"image"`
	EditionIndex        int     `json:"editionIndex"`
	UnattendURL         *string `json:"unattendUrl"`
	UnattendDestination *string `json:"unattendDestination,omitempty"`
	IdentityFiles       []struct {
		Kind       string `json:"kind"`
		ContentURL string `json:"contentUrl"`
		SHA256     string `json:"sha256"`
	} `json:"identityFiles,omitempty"`
	Cleanup *struct {
		RemoveUnattend *bool `json:"removeUnattend,omitempty"`
	} `json:"cleanup,omitempty"`
	OSDrivers []File `json:"osDrivers,omitempty"`
	SMB       *struct {
		UNCPath   *string `json:"uncPath,omitempty"`
		Username  *string `json:"username"`
		Password  *string `json:"password"`
		SetupPath *string `json:"setupPath,omitempty"`
	} `json:"smb"`
	Disk *struct {
		Layout          *string `json:"layout,omitempty"`
		TargetDiskIndex *int    `json:"targetDiskIndex"`
	} `json:"disk,omitempty"`
	BootOrderAfterDeploy string `json:"bootOrderAfterDeploy"` // disk-first or network-first
	BootManager          string `json:"bootManager"`          // windows-pca-2011 or windows-uefi-ca-2023
}

// Deployment stages (DeploymentStage), in order.
const (
	StageCheckedIn        = "checked-in"
	StagePartitioning     = "partitioning"
	StageApplyingImage    = "applying-image"
	StageInjectingDrivers = "injecting-drivers"
	StageConfiguring      = "configuring"
	StageFinalizing       = "finalizing"
	StageRebooting        = "rebooting"
)

// Progress is AgentProgress.
type Progress struct {
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

// LogEntry is LogEntry.
type LogEntry struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"` // debug, info, warn, error
	Message string    `json:"message"`
	Source  string    `json:"source,omitempty"`
}
