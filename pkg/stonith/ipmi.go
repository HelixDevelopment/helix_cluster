package stonith

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ipmiRunner abstracts execution of the ipmitool binary so the exact argv can
// be unit-tested without an IPMI BMC present. The real implementation shells out
// via os/exec; tests substitute a recorder that captures argv and returns canned
// output. lookPath mirrors exec.LookPath so absence of the binary surfaces as a
// typed ErrIPMIToolAbsent rather than a fabricated success.
type ipmiRunner interface {
	// lookPath returns the resolved path to ipmitool or an error if absent.
	lookPath() (string, error)
	// run executes ipmitool with args and returns its combined output.
	run(ctx context.Context, args []string) (string, error)
}

// execIPMIRunner is the production ipmiRunner backed by os/exec.
type execIPMIRunner struct{}

func (execIPMIRunner) lookPath() (string, error) {
	p, err := exec.LookPath("ipmitool")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrIPMIToolAbsent, err)
	}
	return p, nil
}

func (execIPMIRunner) run(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "ipmitool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ipmitool %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// IPMIConfig holds the lan-plus connection parameters for a target's BMC.
type IPMIConfig struct {
	// Host is the BMC IP/hostname (the -H argument).
	Host string
	// Username is the BMC user (-U).
	Username string
	// Password is the BMC password (-P).
	Password string
	// Interface is the ipmitool interface (-I), defaulting to "lanplus".
	Interface string
}

// IPMIAgent fences a node by issuing an out-of-band chassis power off through
// the target's BMC via ipmitool. Out-of-band fencing works even when the host
// OS is wedged, which is why it is the canonical first STONITH level.
type IPMIAgent struct {
	name   string
	cfg    IPMIConfig
	runner ipmiRunner
}

// NewIPMIAgent builds an IPMIAgent for the given BMC config using the real
// ipmitool binary.
func NewIPMIAgent(name string, cfg IPMIConfig) *IPMIAgent {
	return newIPMIAgent(name, cfg, execIPMIRunner{})
}

// newIPMIAgent is the testable constructor that accepts an injected runner.
func newIPMIAgent(name string, cfg IPMIConfig, r ipmiRunner) *IPMIAgent {
	if cfg.Interface == "" {
		cfg.Interface = "lanplus"
	}
	return &IPMIAgent{name: name, cfg: cfg, runner: r}
}

// Name returns the agent's name.
func (a *IPMIAgent) Name() string { return a.name }

// powerArgs builds the exact ipmitool argv for a "chassis power <verb>"
// operation. Keeping this in one place makes the argv deterministically
// unit-testable (CLAUDE-1: prove we build the right command, not just that exec
// runs). The order is: -I <iface> -H <host> -U <user> -P <pass> chassis power
// <verb>.
func (a *IPMIAgent) powerArgs(verb string) []string {
	return []string{
		"-I", a.cfg.Interface,
		"-H", a.cfg.Host,
		"-U", a.cfg.Username,
		"-P", a.cfg.Password,
		"chassis", "power", verb,
	}
}

// Fence powers the target off via IPMI and confirms the chassis reports "off".
// If ipmitool is absent it returns ErrIPMIToolAbsent (never a fake fence).
func (a *IPMIAgent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if _, err := a.runner.lookPath(); err != nil {
		return nil, err
	}
	if _, err := a.runner.run(ctx, a.powerArgs("off")); err != nil {
		return nil, fmt.Errorf("ipmi power off %q: %w", target, err)
	}
	// Confirm sink-side: query chassis power status and require "off".
	fenced, err := a.IsFenced(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ipmi confirm %q: %w", target, err)
	}
	if !fenced {
		return nil, fmt.Errorf("ipmi %q still powered: %w", target, ErrNotConfirmed)
	}
	return &Confirmation{
		Target: target,
		Agent:  a.name,
		Method: "ipmi power off",
		At:     time.Now(),
	}, nil
}

// IsFenced queries "chassis power status" and reports true when the BMC says the
// chassis is off. ipmitool prints "Chassis Power is off" / "...is on".
func (a *IPMIAgent) IsFenced(ctx context.Context, target string) (bool, error) {
	if _, err := a.runner.lookPath(); err != nil {
		return false, err
	}
	out, err := a.runner.run(ctx, a.powerArgs("status"))
	if err != nil {
		return false, fmt.Errorf("ipmi status %q: %w", target, err)
	}
	return strings.Contains(strings.ToLower(out), "power is off"), nil
}
