package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupMemoryLimit(t *testing.T) {
	t.Parallel()
	const (
		v2Mount = "30 1 0:26 %s %s rw,nosuid shared:4 - cgroup2 cgroup2 rw,nsdelegate\n"
		v1Mount = "31 1 0:27 %s /sys/fs/cgroup/memory rw,nosuid shared:5 - cgroup cgroup rw,memory\n"
		v1Cpu   = "32 1 0:28 / /sys/fs/cgroup/cpu rw,nosuid shared:6 - cgroup cgroup rw,cpu,cpuacct\n"
	)
	v2 := func(mountRoot string) string { return fmt.Sprintf(v2Mount, mountRoot, "/sys/fs/cgroup") }
	v1 := func(mountRoot string) string { return fmt.Sprintf(v1Mount, mountRoot) }
	for _, test := range []struct {
		name      string
		files     map[string]string
		dirs      []string
		wantLimit uint64
		wantKnown bool
	}{
		{
			name:      "no cgroup information",
			wantKnown: true,
		},
		{
			name: "v2 host groups below the hierarchy root, unlimited",
			files: map[string]string{
				"proc/self/cgroup":                    "0::/user.slice\n",
				"proc/self/mountinfo":                 v2("/"),
				"sys/fs/cgroup/user.slice/memory.max": "max\n",
			},
			wantKnown: true,
		},
		{
			name: "v2 nested limits take the smallest ancestor",
			files: map[string]string{
				"proc/self/cgroup":                     "0::/outer/inner\n",
				"proc/self/mountinfo":                  v2("/"),
				"sys/fs/cgroup/outer/memory.max":       "1073741824\n",
				"sys/fs/cgroup/outer/inner/memory.max": "max\n",
			},
			wantLimit: 1 << 30,
			wantKnown: true,
		},
		{
			name: "v2 container at its namespace root with a limit",
			files: map[string]string{
				"proc/self/cgroup":         "0::/\n",
				"proc/self/mountinfo":      v2("/"),
				"sys/fs/cgroup/memory.max": "33554432\n",
			},
			wantLimit: 32 << 20,
			wantKnown: true,
		},
		{
			name: "v2 container at its namespace root without a limit may have a hidden one",
			files: map[string]string{
				"proc/self/cgroup":         "0::/\n",
				"proc/self/mountinfo":      v2("/"),
				"sys/fs/cgroup/memory.max": "max\n",
			},
			wantKnown: false,
		},
		{
			name: "v2 subtree mount translates the group path",
			files: map[string]string{
				"proc/self/cgroup":               "0::/parent/child\n",
				"proc/self/mountinfo":            v2("/parent"),
				"sys/fs/cgroup/child/memory.max": "33554432\n",
				"sys/fs/cgroup/memory.max":       "max\n",
			},
			wantLimit: 32 << 20,
			wantKnown: true,
		},
		{
			name: "v2 mount paths with escapes",
			files: map[string]string{
				"proc/self/cgroup":      "0::/a b/c\n",
				"proc/self/mountinfo":   fmt.Sprintf(v2Mount, `/a\040b`, `/cg\040space`),
				"cg space/c/memory.max": "16777216\n",
			},
			wantLimit: 16 << 20,
			wantKnown: true,
		},
		{
			name: "v2 unreadable limit file",
			files: map[string]string{
				"proc/self/cgroup":    "0::/user.slice\n",
				"proc/self/mountinfo": v2("/"),
			},
			dirs:      []string{"sys/fs/cgroup/user.slice/memory.max"},
			wantKnown: false,
		},
		{
			name: "v1 intermediate ancestor limit applies",
			files: map[string]string{
				"proc/self/cgroup":    "12:cpu,cpuacct:/parent/child\n5:memory:/parent/child\n",
				"proc/self/mountinfo": v1("/") + v1Cpu,
				"sys/fs/cgroup/memory/parent/child/memory.limit_in_bytes": "1073741824\n",
				"sys/fs/cgroup/memory/parent/memory.limit_in_bytes":       "33554432\n",
				"sys/fs/cgroup/memory/memory.limit_in_bytes":              "9223372036854771712\n",
			},
			wantLimit: 32 << 20,
			wantKnown: true,
		},
		{
			name: "v1 container mount of its own group",
			files: map[string]string{
				"proc/self/cgroup":                           "5:memory:/docker/abc\n",
				"proc/self/mountinfo":                        v1("/docker/abc"),
				"sys/fs/cgroup/memory/memory.limit_in_bytes": "268435456\n",
			},
			wantLimit: 256 << 20,
			wantKnown: true,
		},
		{
			name: "v1 container mount of its own unlimited group may have a hidden limit",
			files: map[string]string{
				"proc/self/cgroup":                           "5:memory:/docker/abc\n",
				"proc/self/mountinfo":                        v1("/docker/abc"),
				"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
			},
			wantKnown: false,
		},
		{
			name: "membership without a visible cgroup mount",
			files: map[string]string{
				"proc/self/cgroup":    "0::/user.slice\n",
				"proc/self/mountinfo": "25 1 8:1 / / rw - ext4 /dev/sda1 rw\n",
			},
			wantKnown: false,
		},
		{
			name: "v1 host root unlimited",
			files: map[string]string{
				"proc/self/cgroup":                           "5:memory:/\n",
				"proc/self/mountinfo":                        v1("/"),
				"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
			},
			wantKnown: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for name, content := range test.files {
				file := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// A directory in place of a limit file makes it unreadable, even for root.
			for _, dir := range test.dirs {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			limit, known := cgroupMemoryLimitAt(root)
			if limit != test.wantLimit || known != test.wantKnown {
				t.Fatalf("cgroupMemoryLimitAt = (%d, %v), want (%d, %v)", limit, known, test.wantLimit, test.wantKnown)
			}
		})
	}
}
