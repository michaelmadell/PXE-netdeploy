package hostcheck

// DataDir: on Windows the key files get a restricted DACL when written
// (ADR 0021); no check here.
func DataDir(string) []string { return nil }
