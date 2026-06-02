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
//
// run accepts a separate env slice so that the BMC password can be delivered
// to the child process via the IPMI_PASSWORD environment variable rather than
// via a "-P <pass>" argv element, which would be visible to every local user
// through the system process table (ps/top). The env slice is appended to the
// inherited environment inside execIPMIRunner; it is captured verbatim by the
// test recorder so closure tests can assert the password is present in env and
// absent from argv.
type ipmiRunner interface {
	// lookPath returns the resolved path to ipmitool or an error if absent.
	lookPath() (string, error)
	// run executes ipmitool with args and the extra environment entries in env,
	// then returns its combined output. env entries are "KEY=VALUE" strings.
	run(ctx context.Context, args []string, env []string) (string, error)
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

// redactedIPMIError builds an error message from the public args only, never
// from the env slice (which carries the BMC password via IPMI_PASSWORD). This
// ensures the password never appears in any error string returned to callers or
// written to logs.
//
// It is a package-level function so it can be unit-tested directly against
// an injected args/env pair — enabling a mutation guard that actually exercises
// the production code path (CLAUDE-1 anti-bluff: the old approach used a mock
// runner whose run() returned a bare error that was trivially password-free).
//
// MUTATION GUARD: if this function is changed to include env entries (e.g. by
// concatenating env into the error string), TestExecIPMIRunnerErrorRedaction
// fails immediately because the password embedded in the env slice would appear
// in the returned error message.
func redactedIPMIError(args []string, env []string, cause error) error {
	// env is intentionally NOT referenced here. The compiler will reject
	// accidental use of env in the format string at compile time because env is
	// an unexported local, but to make the safety guarantee explicit: the only
	// data in the error message is args and cause, both of which are
	// password-free.
	_ = env // acknowledge env parameter; intentionally excluded from error text
	return fmt.Errorf("ipmitool %s: %w", strings.Join(args, " "), cause)
}

// run executes ipmitool with the given public args. The extra env entries
// (e.g. "IPMI_PASSWORD=secret") are appended to the inherited process
// environment so the password is never on the argv and therefore never visible
// to other processes via the system process table.
//
// Error messages are built via redactedIPMIError using the public args only —
// the password is NOT included in any error string returned to callers
// (CLAUDE-1 anti-bluff: a leaked password in an error is a security defect,
// not a PASS).
func (execIPMIRunner) run(ctx context.Context, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "ipmitool", args...)
	// Inherit the full current environment and append the extra entries last so
	// they win over any same-named variable the parent may have set.
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), redactedIPMIError(args, env, err)
	}
	return string(out), nil
}

// IPMIConfig holds the lan-plus connection parameters for a target's BMC.
type IPMIConfig struct {
	// Host is the BMC IP/hostname (the -H argument).
	Host string
	// Username is the BMC user (-U).
	Username string
	// Password is the BMC password. It is delivered to ipmitool via the
	// IPMI_PASSWORD environment variable (with the "-E" flag) so it never
	// appears on the process's argv and is therefore not visible to other local
	// users via the system process table.
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

// powerArgs builds the public ipmitool argv for a "chassis power <verb>"
// operation — every flag except the password. The password is deliberately
// absent from argv and is instead delivered to the child process via the
// IPMI_PASSWORD environment variable using the "-E" flag (see powerEnv).
//
// Keeping this in one place makes the argv deterministically unit-testable
// (CLAUDE-1: prove we build the right command without leaking the secret,
// not just that exec runs). Mutation guard: removing "-E" from the returned
// slice or adding "-P <pass>" back would break TestIPMIArgvContainsNoPassword
// and TestIPMIArgvUsesEFlag.
func (a *IPMIAgent) powerArgs(verb string) []string {
	return []string{
		"-I", a.cfg.Interface,
		"-H", a.cfg.Host,
		"-U", a.cfg.Username,
		"-E", // read password from IPMI_PASSWORD env var; never from argv
		"chassis", "power", verb,
	}
}

// powerEnv returns the environment entries that carry the BMC password to the
// ipmitool child process. Using IPMI_PASSWORD + "-E" means the secret is
// process-private: it is visible only inside the child's environment block,
// not on the public argv that every local user can observe via "ps" or
// /proc/<pid>/cmdline. Mutation guard: removing this method (or returning an
// empty slice) would break TestIPMIEnvCarriesPassword.
func (a *IPMIAgent) powerEnv() []string {
	return []string{"IPMI_PASSWORD=" + a.cfg.Password}
}

// Fence powers the target off via IPMI and confirms the chassis reports "off".
// If ipmitool is absent it returns ErrIPMIToolAbsent (never a fake fence).
//
// SAFETY INVARIANT: target MUST match cfg.Host (the BMC address this agent was
// configured to operate). If they differ, this agent would send power-off
// commands to the BMC in cfg.Host while emitting a Confirmation claiming target
// was fenced — a wrong-node-fence / split-brain hazard that is the exact failure
// mode STONITH exists to prevent. We therefore return ErrTargetMismatch when
// target != cfg.Host so the mismatch is caught loudly at the call site rather
// than silently fencing the wrong machine.
//
// MUTATION GUARD: TestIPMIAgentTargetMustMatchHost asserts this guard fires;
// removing the check (or changing != to ==) makes that test fail.
func (a *IPMIAgent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if target != a.cfg.Host {
		return nil, fmt.Errorf("ipmi fence %q: %w (agent cfg.Host=%q)", target, ErrTargetMismatch, a.cfg.Host)
	}
	if _, err := a.runner.lookPath(); err != nil {
		return nil, err
	}
	if _, err := a.runner.run(ctx, a.powerArgs("off"), a.powerEnv()); err != nil {
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
//
// The same target/host mismatch guard as Fence applies here: we refuse to
// report status for a target that does not match the BMC this agent is wired to.
func (a *IPMIAgent) IsFenced(ctx context.Context, target string) (bool, error) {
	if target != "" && target != a.cfg.Host {
		return false, fmt.Errorf("ipmi status %q: %w (agent cfg.Host=%q)", target, ErrTargetMismatch, a.cfg.Host)
	}
	if _, err := a.runner.lookPath(); err != nil {
		return false, err
	}
	out, err := a.runner.run(ctx, a.powerArgs("status"), a.powerEnv())
	if err != nil {
		return false, fmt.Errorf("ipmi status %q: %w", target, err)
	}
	return strings.Contains(strings.ToLower(out), "power is off"), nil
}
