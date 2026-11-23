package autoinstall

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/michaelmadell/pxe-netdeploy/internal/unattend"
)

var yamlLineRE = regexp.MustCompile(`line (\d+)`)

// Check checks rendered user-data (ADR 0035): cloud-config with an
// autoinstall version 1 that creates the account, or names identity as
// interactive.
func Check(out []byte, d Data) []unattend.Finding {
	var fs []unattend.Finding
	add := func(sev, code string, line int, format string, a ...any) {
		fs = append(fs, unattend.Finding{Severity: sev, Code: code, Line: line, Message: fmt.Sprintf(format, a...)})
	}
	first, _, _ := bytes.Cut(out, []byte("\n"))
	if string(bytes.TrimRight(first, "\r \t")) != "#cloud-config" {
		add(unattend.SevError, "autoinstall_header", 1, "The first line must be #cloud-config, or cloud-init ignores the file.")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		line := 0
		if m := yamlLineRE.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		add(unattend.SevError, "autoinstall_yaml", line, "The output isn't valid YAML: %v.", err)
		return fs
	}
	ai, ok := doc["autoinstall"].(map[string]any)
	if !ok {
		add(unattend.SevError, "autoinstall_missing", 0, "There's no autoinstall: mapping at the top level.")
		return fs
	}
	if v, ok := ai["version"].(int); !ok || v != 1 {
		add(unattend.SevError, "autoinstall_version", 0, "autoinstall.version must be 1.")
	}
	interactive := map[string]bool{}
	if list, ok := ai["interactive-sections"].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				interactive[s] = true
			}
		}
	}
	id, hasID := ai["identity"].(map[string]any)
	_, hasUserData := ai["user-data"]
	if !hasID && !hasUserData && !interactive["identity"] && !interactive["*"] {
		add(unattend.SevError, "autoinstall_identity", 0, "There's no identity (or user-data) section, so the installer would stop to ask for an account.")
	}
	if hasID && d.Hostname != "" {
		if h, _ := id["hostname"].(string); h != d.Hostname {
			add(unattend.SevWarning, "hostname_dropped", 0, "identity.hostname isn't .Hostname (%q), so the profile's hostname pattern is ignored.", d.Hostname)
		}
	}
	if d.Report.Webhook != "" {
		rep, _ := ai["reporting"].(map[string]any)
		found := false
		for _, v := range rep {
			if m, ok := v.(map[string]any); ok && m["endpoint"] == d.Report.Webhook {
				found = true
			}
		}
		if !found {
			add(unattend.SevWarning, "report_missing", 0, "No reporting webhook to .Report.Webhook: netdeploy won't see the install's progress.")
		}
		if !bytes.Contains(out, []byte(d.Report.Finished)) {
			add(unattend.SevWarning, "report_finish_missing", 0, "Nothing calls .Report.Finished: the deployment won't be marked succeeded and ends at the inactivity timeout.")
		}
	}
	if hasID && d.Account.Username != "" {
		if u, _ := id["username"].(string); u != d.Account.Username {
			add(unattend.SevWarning, "account_dropped", 0, "identity.username isn't .Account.Username (%q).", d.Account.Username)
		}
	}
	return fs
}
