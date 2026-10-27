package core

import "fmt"

// These public Darwin disk-policy values are intentionally a small allowlist.
// DEFAULT does not expose every underlying intention; we only preserve known
// restrictive constraints, applying an explicit utility ceiling when needed.
const (
	diskDefault  = 0
	diskThrottle = 3
	diskUtility  = 4
)

type childPolicyState struct{ background, processIO, backgroundIO int }
type childPolicy interface {
	read() (childPolicyState, error)
	setProcessIO(int) error
	clearBackground() error
}

// prepareWorktreeChild only runs inside the helper process. Unknown initial
// state leaves inheritance untouched. Once a setter is attempted, uncertainty
// is a pre-exec failure: no invented rollback, no Git, and no parent retry.
func prepareWorktreeChild(p childPolicy) (bool, error) {
	before, err := p.read()
	if err != nil || before.background != 1 || before.backgroundIO != diskUtility ||
		(before.processIO != diskDefault && before.processIO != diskUtility && before.processIO != diskThrottle) {
		return false, nil
	}
	expected := before
	if before.processIO == diskDefault {
		if err := p.setProcessIO(diskUtility); err != nil {
			return false, fmt.Errorf("set utility disk policy: %w", err)
		}
		expected.processIO = diskUtility
		state, err := p.read()
		if err != nil || state != expected {
			return false, fmt.Errorf("utility disk policy could not be verified")
		}
	}
	if err := p.clearBackground(); err != nil {
		return false, fmt.Errorf("clear owned background request: %w", err)
	}
	expected.background = 0
	state, err := p.read()
	if err != nil || state != expected {
		return false, fmt.Errorf("child background and disk policies could not be verified")
	}
	return true, nil
}
