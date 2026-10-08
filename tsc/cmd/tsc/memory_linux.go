package main

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

// cgroupMemoryLimit returns the smallest memory limit of the cgroups containing this process, or
// 0 if there is none. Containers usually limit memory this way, far below physical memory.
//
// known is false when a limit may apply that cannot be read: a limit file is unreadable, or the
// visible cgroups are a subtree (a container's cgroup namespace) whose hidden ancestors may set
// the limit. Batch tuning that raises memory use must then stay off.
func cgroupMemoryLimit() (limit uint64, known bool) {
	return cgroupMemoryLimitAt("/")
}

// cgroupMemoryLimitAt is cgroupMemoryLimit with /proc and the cgroup mounts under root.
//
// The limits of a cgroup's ancestors apply to it as well, so every ancestor visible below a
// cgroup mount is read (cgroup v2 memory.max, v1 memory.limit_in_bytes). A process's cgroup path
// in /proc/self/cgroup is relative to the hierarchy root, while a mount may expose only a subtree
// of it (containers mount their own group), so the path is translated by the mount's root from
// /proc/self/mountinfo.
func cgroupMemoryLimitAt(root string) (limit uint64, known bool) {
	membership, err := os.ReadFile(path.Join(root, "proc/self/cgroup"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, true
	} else if err != nil {
		return 0, false
	}
	// Lines read "hierarchy-ID:controller-list:cgroup-path"; v2 uses ID 0 and no controllers.
	var v2Group, v1Group string
	for line := range strings.Lines(string(membership)) {
		fields := strings.SplitN(strings.TrimSuffix(line, "\n"), ":", 3)
		switch {
		case len(fields) != 3:
		case fields[0] == "0" && fields[1] == "":
			v2Group = fields[2]
		case strings.Contains(","+fields[1]+",", ",memory,"):
			v1Group = fields[2]
		}
	}
	if v2Group == "" && v1Group == "" {
		return 0, true
	}
	mounts, err := os.ReadFile(path.Join(root, "proc/self/mountinfo"))
	if err != nil {
		return 0, false
	}
	// Without a visible mount of the hierarchies the process belongs to, their limits are unknown.
	known = true
	mounted := false
	// Lines read "id parent dev mount-root mount-point options [optional...] - fstype source super-options".
	for line := range strings.Lines(string(mounts)) {
		before, after, ok := strings.Cut(line, " - ")
		fields, tail := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 5 || len(tail) < 3 {
			continue
		}
		mountRoot, mountPoint := unescapeMountPath(fields[3]), path.Join(root, unescapeMountPath(fields[4]))
		var mountLimit uint64
		var mountKnown bool
		switch {
		case tail[0] == "cgroup2" && v2Group != "":
			mountLimit, mountKnown = ancestorMemoryLimit(mountPoint, mountRoot, v2Group, "memory.max")
		case tail[0] == "cgroup" && v1Group != "" && strings.Contains(","+tail[2]+",", ",memory,"):
			mountLimit, mountKnown = ancestorMemoryLimit(mountPoint, mountRoot, v1Group, "memory.limit_in_bytes")
		default:
			continue
		}
		mounted = true
		limit = minLimit(limit, mountLimit)
		known = known && mountKnown
	}
	// A finite visible limit is trusted even below hidden ancestors: container runtimes give a
	// container a limit no larger than its parents' (a Kubernetes pod's limit is the sum of its
	// containers'), so the visible one is the effective limit.
	return limit, mounted && (known || limit != 0)
}

// ancestorMemoryLimit returns the smallest limit in the file named limitFile of group and its
// ancestors that are visible in the cgroup mount at dir, whose root is the group mountRoot.
// known is false if a limit file is unreadable, or if no visible group sets a limit while the
// mount's top group is not the hierarchy root (it has a limit file, or the mount is a subtree),
// so that hidden ancestors may.
func ancestorMemoryLimit(dir string, mountRoot string, group string, limitFile string) (limit uint64, known bool) {
	relative := "/"
	if rest, ok := strings.CutPrefix(path.Clean(group), path.Clean(mountRoot)); ok && (rest == "" || rest[0] == '/' || mountRoot == "/") {
		relative = path.Clean("/" + rest)
	}
	for group := relative; ; group = path.Dir(group) {
		groupLimit, exists, ok := readMemoryLimit(path.Join(dir, group, limitFile))
		if !ok {
			return 0, false
		}
		limit = minLimit(limit, groupLimit)
		if group == "/" {
			// The v2 hierarchy root has no memory.max; the v1 root's limit is always unlimited.
			topIsRoot := path.Clean(mountRoot) == "/" && (limitFile != "memory.max" || !exists)
			return limit, limit != 0 || topIsRoot
		}
	}
}

// readMemoryLimit returns the byte limit in a cgroup limit file, or 0 if it sets none: "max"
// (v2), a value near the maximum int64 (v1), or a missing file. ok is false if the file cannot be
// read or parsed.
func readMemoryLimit(file string) (limit uint64, exists bool, ok bool) {
	text, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, true
	} else if err != nil {
		return 0, true, false
	}
	value := strings.TrimSpace(string(text))
	if value == "max" {
		return 0, true, true
	}
	limit, err = strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, true, false
	}
	if limit >= 1<<62 {
		return 0, true, true
	}
	return limit, true, true
}

// unescapeMountPath decodes the octal escapes (\040 for a space, \134 for a backslash, ...) that
// /proc/self/mountinfo uses in paths.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool {
	return c >= '0' && c <= '7'
}
