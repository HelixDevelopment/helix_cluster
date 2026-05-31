package resources

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// cgroupMaxSentinel is the literal token cgroup v2 uses in cpu.max / memory.max
// to indicate "no limit".
const cgroupMaxSentinel = "max"

// CPUMax holds the parsed contents of a cgroup v2 cpu.max file.
//
// cpu.max has the form "$QUOTA $PERIOD" where $QUOTA may be the literal "max"
// meaning the group is uncapped. Both values are in microseconds.
type CPUMax struct {
	// Unlimited is true when the quota is the "max" sentinel (no CPU cap).
	Unlimited bool
	// QuotaUsec is the CPU time, in microseconds, the group may consume per
	// PeriodUsec. Meaningless when Unlimited is true.
	QuotaUsec int64
	// PeriodUsec is the accounting period in microseconds.
	PeriodUsec int64
}

// Cores returns the effective number of CPU cores the cgroup is allowed to use,
// derived as quota/period. An unlimited group (or one with a zero/absent
// period) returns 0, meaning "no derived cap".
func (c CPUMax) Cores() float64 {
	if c.Unlimited || c.PeriodUsec <= 0 || c.QuotaUsec <= 0 {
		return 0
	}
	return float64(c.QuotaUsec) / float64(c.PeriodUsec)
}

// CPUStat holds parsed fields from a cgroup v2 cpu.stat file. Unknown keys are
// ignored; absent known keys remain zero.
type CPUStat struct {
	UsageUsec     int64
	UserUsec      int64
	SystemUsec    int64
	NrPeriods     int64
	NrThrottled   int64
	ThrottledUsec int64
}

// MemoryMax holds the parsed contents of a cgroup v2 memory.max file. The file
// is a single value in bytes or the literal "max" for no limit.
type MemoryMax struct {
	// Unlimited is true when memory.max is the "max" sentinel.
	Unlimited bool
	// Bytes is the memory limit in bytes; 0 when Unlimited.
	Bytes int64
}

// IODeviceStat holds parsed per-device counters from a cgroup v2 io.stat line.
type IODeviceStat struct {
	RBytes int64
	WBytes int64
	RIOs   int64
	WIOs   int64
	DBytes int64
	DIOs   int64
}

// CgroupV2Stats is an aggregate snapshot of the readable cgroup v2 controllers.
type CgroupV2Stats struct {
	CPUMax        CPUMax
	CPUStat       CPUStat
	MemoryCurrent int64
	MemoryMax     MemoryMax
	IO            map[string]IODeviceStat
}

// CgroupV2Reader reads cgroup v2 controller files through an fs.FS abstraction.
//
// Using fs.FS (rather than reading /sys/fs/cgroup directly) makes the reader
// fully testable on any OS: tests supply an embedded fixture FS, while
// production code supplies os.DirFS("/sys/fs/cgroup") (or a sub-FS for a
// specific group). This satisfies the CLAUDE-1 mandate that parsing be verified
// against real bytes rather than mocked.
type CgroupV2Reader struct {
	fsys fs.FS
	// root is the directory within fsys that holds the controller files
	// (cpu.max, memory.max, ...). Use "." for the FS root.
	root string
}

// NewCgroupV2Reader constructs a reader over the given filesystem and root
// directory. root may be "." or "" to mean the FS root.
func NewCgroupV2Reader(fsys fs.FS, root string) *CgroupV2Reader {
	if root == "" {
		root = "."
	}
	return &CgroupV2Reader{fsys: fsys, root: root}
}

// pathFor joins the reader root with a controller filename in a way that is
// valid for io/fs (always slash-separated, no leading slash).
func (r *CgroupV2Reader) pathFor(name string) string {
	if r.root == "." || r.root == "" {
		return name
	}
	return path.Join(r.root, name)
}

// readTrimmed reads an entire controller file and returns its trimmed contents.
func (r *CgroupV2Reader) readTrimmed(name string) (string, error) {
	b, err := fs.ReadFile(r.fsys, r.pathFor(name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// parseInt64 parses a base-10 signed integer, rejecting malformed input and
// overflow. It is the single choke point for numeric hardening: callers never
// silently coerce bad data to zero.
func parseInt64(s string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse int %q: %w", s, err)
	}
	return v, nil
}

// parseNonNegative parses a base-10 integer and additionally rejects negative
// values, which are never valid for byte counts or usage counters.
func parseNonNegative(s string) (int64, error) {
	v, err := parseInt64(s)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("value %d is negative", v)
	}
	return v, nil
}

// ReadCPUMax parses cpu.max. A missing file is an error; a malformed quota or
// period is an error (never silently zero); the "max" quota sentinel yields
// Unlimited.
func (r *CgroupV2Reader) ReadCPUMax() (CPUMax, error) {
	s, err := r.readTrimmed("cpu.max")
	if err != nil {
		return CPUMax{}, fmt.Errorf("read cpu.max: %w", err)
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return CPUMax{}, errors.New("cpu.max: empty")
	}

	out := CPUMax{}
	if fields[0] == cgroupMaxSentinel {
		out.Unlimited = true
	} else {
		q, err := parseNonNegative(fields[0])
		if err != nil {
			return CPUMax{}, fmt.Errorf("cpu.max quota: %w", err)
		}
		out.QuotaUsec = q
	}

	// Period defaults to the kernel default (100000us) when omitted, but a
	// present period must parse cleanly.
	out.PeriodUsec = 100000
	if len(fields) >= 2 {
		p, err := parseNonNegative(fields[1])
		if err != nil {
			return CPUMax{}, fmt.Errorf("cpu.max period: %w", err)
		}
		if p == 0 {
			return CPUMax{}, errors.New("cpu.max: period is zero")
		}
		out.PeriodUsec = p
	}
	return out, nil
}

// ReadCPUStat parses cpu.stat. Known keys are mapped; unknown keys are ignored.
// A malformed value for a known key is an error.
func (r *CgroupV2Reader) ReadCPUStat() (CPUStat, error) {
	b, err := fs.ReadFile(r.fsys, r.pathFor("cpu.stat"))
	if err != nil {
		return CPUStat{}, fmt.Errorf("read cpu.stat: %w", err)
	}
	var st CPUStat
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return CPUStat{}, fmt.Errorf("cpu.stat: malformed line %q", line)
		}
		target := cpuStatField(&st, fields[0])
		if target == nil {
			continue // unknown key
		}
		v, err := parseNonNegative(fields[1])
		if err != nil {
			return CPUStat{}, fmt.Errorf("cpu.stat key %q: %w", fields[0], err)
		}
		*target = v
	}
	if err := sc.Err(); err != nil {
		return CPUStat{}, fmt.Errorf("scan cpu.stat: %w", err)
	}
	return st, nil
}

// cpuStatField returns a pointer to the CPUStat field for a known key, or nil.
func cpuStatField(st *CPUStat, key string) *int64 {
	switch key {
	case "usage_usec":
		return &st.UsageUsec
	case "user_usec":
		return &st.UserUsec
	case "system_usec":
		return &st.SystemUsec
	case "nr_periods":
		return &st.NrPeriods
	case "nr_throttled":
		return &st.NrThrottled
	case "throttled_usec":
		return &st.ThrottledUsec
	default:
		return nil
	}
}

// ReadMemoryCurrent parses memory.current (a single non-negative byte count).
func (r *CgroupV2Reader) ReadMemoryCurrent() (int64, error) {
	s, err := r.readTrimmed("memory.current")
	if err != nil {
		return 0, fmt.Errorf("read memory.current: %w", err)
	}
	v, err := parseNonNegative(s)
	if err != nil {
		return 0, fmt.Errorf("memory.current: %w", err)
	}
	return v, nil
}

// ReadMemoryMax parses memory.max. The "max" sentinel yields Unlimited with
// Bytes==0; any other value must parse as a non-negative int64 (overflow is an
// error).
func (r *CgroupV2Reader) ReadMemoryMax() (MemoryMax, error) {
	s, err := r.readTrimmed("memory.max")
	if err != nil {
		return MemoryMax{}, fmt.Errorf("read memory.max: %w", err)
	}
	if s == cgroupMaxSentinel {
		return MemoryMax{Unlimited: true}, nil
	}
	v, err := parseNonNegative(s)
	if err != nil {
		return MemoryMax{}, fmt.Errorf("memory.max: %w", err)
	}
	return MemoryMax{Bytes: v}, nil
}

// ReadIOStat parses io.stat into a map keyed by "major:minor" device. A missing
// io.stat file is not fatal (some groups have no io controller): an empty map
// and nil error are returned. Malformed lines/keys are errors.
func (r *CgroupV2Reader) ReadIOStat() (map[string]IODeviceStat, error) {
	b, err := fs.ReadFile(r.fsys, r.pathFor("io.stat"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]IODeviceStat{}, nil
		}
		return nil, fmt.Errorf("read io.stat: %w", err)
	}

	out := make(map[string]IODeviceStat)
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("io.stat: malformed line %q", line)
		}
		dev := fields[0]
		if !strings.Contains(dev, ":") {
			return nil, fmt.Errorf("io.stat: bad device id %q", dev)
		}
		var ds IODeviceStat
		for _, kv := range fields[1:] {
			k, val, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("io.stat: malformed pair %q", kv)
			}
			target := ioStatField(&ds, k)
			if target == nil {
				continue // unknown counter
			}
			v, err := parseNonNegative(val)
			if err != nil {
				return nil, fmt.Errorf("io.stat %s.%s: %w", dev, k, err)
			}
			*target = v
		}
		out[dev] = ds
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan io.stat: %w", err)
	}
	return out, nil
}

// ioStatField returns a pointer to the IODeviceStat field for a known counter.
func ioStatField(ds *IODeviceStat, key string) *int64 {
	switch key {
	case "rbytes":
		return &ds.RBytes
	case "wbytes":
		return &ds.WBytes
	case "rios":
		return &ds.RIOs
	case "wios":
		return &ds.WIOs
	case "dbytes":
		return &ds.DBytes
	case "dios":
		return &ds.DIOs
	default:
		return nil
	}
}

// ReadStats reads every controller into a single aggregate snapshot. cpu.max,
// cpu.stat, memory.current and memory.max are required; io.stat is optional.
func (r *CgroupV2Reader) ReadStats() (CgroupV2Stats, error) {
	cpuMax, err := r.ReadCPUMax()
	if err != nil {
		return CgroupV2Stats{}, err
	}
	cpuStat, err := r.ReadCPUStat()
	if err != nil {
		return CgroupV2Stats{}, err
	}
	memCur, err := r.ReadMemoryCurrent()
	if err != nil {
		return CgroupV2Stats{}, err
	}
	memMax, err := r.ReadMemoryMax()
	if err != nil {
		return CgroupV2Stats{}, err
	}
	io, err := r.ReadIOStat()
	if err != nil {
		return CgroupV2Stats{}, err
	}
	return CgroupV2Stats{
		CPUMax:        cpuMax,
		CPUStat:       cpuStat,
		MemoryCurrent: memCur,
		MemoryMax:     memMax,
		IO:            io,
	}, nil
}

// Read implements the Reader interface, mapping cgroup v2 limits into the
// shared NodeResources shape so the reader can be registered with a
// NodeAggregator. CPU capacity is the derived core cap; memory total/used come
// from memory.max/memory.current (in KiB). An unlimited memory.max leaves
// TotalKB at 0 (no cap to report).
func (r *CgroupV2Reader) Read(nodeID string) (NodeResources, error) {
	s, err := r.ReadStats()
	if err != nil {
		return NodeResources{}, fmt.Errorf("cgroup v2 read: %w", err)
	}

	var totalKB int64
	if !s.MemoryMax.Unlimited {
		totalKB = s.MemoryMax.Bytes / 1024
	}
	usedKB := s.MemoryCurrent / 1024
	availableKB := totalKB - usedKB
	if availableKB < 0 {
		availableKB = 0
	}

	return NodeResources{
		NodeID: nodeID,
		CPU: CPUInfo{
			UsedCores: s.CPUMax.Cores(),
		},
		Memory: MemoryInfo{
			TotalKB:     totalKB,
			AvailableKB: availableKB,
			UsedKB:      usedKB,
		},
	}, nil
}

// Ensure CgroupV2Reader satisfies the Reader interface.
var _ Reader = (*CgroupV2Reader)(nil)
