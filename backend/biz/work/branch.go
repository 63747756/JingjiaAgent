package work

import "strings"

// Match Git's branch-name rules without invoking a shell or assuming main/master.
func validTaskBranch(branch string) bool {
	if len(branch) > 255 || strings.HasPrefix(branch, "-") || branch == "@" || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.HasSuffix(branch, ".") {
		return false
	}
	for _, c := range branch {
		if c <= 32 || c == 127 || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
