package agentresource

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"
)

// ValidateSkillPackage applies the same zip-slip/bomb/file-count boundaries as
// dispatch and additionally requires a SKILL.md entry. Used before work admission.
func ValidateSkillPackage(data []byte) error {
	_, err := InspectSkillPackage(data)
	return err
}

type PackageUsage struct {
	Files int
	Bytes int64
}

// Add applies the Guest's aggregate limits, including both skills and plugins.
func (u *PackageUsage) Add(next PackageUsage) error {
	if next.Files < 0 || next.Bytes < 0 || next.Files > DefaultUnzipLimits.MaxFiles-u.Files || next.Bytes > DefaultUnzipLimits.MaxTotalSize-u.Bytes {
		return fmt.Errorf("本轮技能和插件解压后合计不能超过 %d 个文件、%d MiB", DefaultUnzipLimits.MaxFiles, DefaultUnzipLimits.MaxTotalSize>>20)
	}
	u.Files += next.Files
	u.Bytes += next.Bytes
	return nil
}

func InspectSkillPackage(data []byte) (PackageUsage, error) {
	return inspectPackage(data, true)
}

func InspectResourcePackage(data []byte) (PackageUsage, error) {
	return inspectPackage(data, false)
}

func inspectPackage(data []byte, requireSkill bool) (PackageUsage, error) {
	var usage PackageUsage
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return usage, err
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return usage, fmt.Errorf("unsupported resource archive entry %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		rel, err := sanitizeZipPath(f.Name)
		if err != nil {
			return usage, err
		}
		if strings.ContainsAny(rel, ":\x00") || seen[strings.ToLower(rel)] {
			return usage, fmt.Errorf("ambiguous resource archive path %q", f.Name)
		}
		seen[strings.ToLower(rel)] = true
	}
	files, err := unzipToMemory(data, DefaultUnzipLimits)
	if err != nil {
		return usage, err
	}
	count := 0
	for _, file := range files {
		usage.Files++
		usage.Bytes += int64(len(file.Content))
		if strings.EqualFold(path.Base(file.RelPath), "SKILL.md") && len(file.Content) > 0 {
			count++
		}
	}
	if requireSkill && count != 1 {
		return usage, fmt.Errorf("skill package must contain exactly one nonempty SKILL.md")
	}
	return usage, nil
}
