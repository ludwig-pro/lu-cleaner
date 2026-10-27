package core

import (
	"errors"
	"reflect"
	"testing"
)

type fakeChildPolicy struct {
	state                                    childPolicyState
	calls                                    []string
	readN, failRead                          int
	failSet, failClear, wrongSet, wrongClear bool
}

func (p *fakeChildPolicy) read() (childPolicyState, error) {
	p.readN++
	p.calls = append(p.calls, "read")
	if p.readN == p.failRead {
		return childPolicyState{}, errors.New("read failed")
	}
	return p.state, nil
}
func (p *fakeChildPolicy) setProcessIO(v int) error {
	p.calls = append(p.calls, "setIO")
	if p.failSet {
		return errors.New("set failed")
	}
	if !p.wrongSet {
		p.state.processIO = v
	}
	return nil
}
func (p *fakeChildPolicy) clearBackground() error {
	p.calls = append(p.calls, "clearBG")
	if p.failClear {
		return errors.New("clear failed")
	}
	if !p.wrongClear {
		p.state.background = 0
	}
	return nil
}

func TestWorktreeChildPolicyPreservesIdentifiableRestrictions(t *testing.T) {
	for _, ioPolicy := range []int{diskDefault, diskUtility, diskThrottle} {
		p := &fakeChildPolicy{state: childPolicyState{1, ioPolicy, diskUtility}}
		changed, err := prepareWorktreeChild(p)
		wantIO := ioPolicy
		if wantIO == diskDefault {
			wantIO = diskUtility
		}
		if !changed || err != nil || p.state != (childPolicyState{0, wantIO, diskUtility}) {
			t.Fatalf("initial=%d changed=%v state=%+v err=%v", ioPolicy, changed, p.state, err)
		}
		want := []string{"read", "clearBG", "read"}
		if ioPolicy == diskDefault {
			want = []string{"read", "setIO", "read", "clearBG", "read"}
		}
		if !reflect.DeepEqual(p.calls, want) {
			t.Fatalf("calls=%v want=%v", p.calls, want)
		}
	}
}
func TestUnknownChildPoliciesRemainInherited(t *testing.T) {
	states := []childPolicyState{{0, 0, 4}, {2, 0, 4}, {-1, 0, 4}}
	for _, ioPolicy := range []int{-1, 1, 2, 5, 99} {
		states = append(states, childPolicyState{1, ioPolicy, 4})
	}
	for _, bgPolicy := range []int{-1, 0, 1, 2, 3, 5, 99} {
		states = append(states, childPolicyState{1, 0, bgPolicy})
	}
	for _, state := range states {
		p := &fakeChildPolicy{state: state}
		changed, err := prepareWorktreeChild(p)
		if changed || err != nil || len(p.calls) != 1 || p.state != state {
			t.Fatalf("state=%+v calls=%v changed=%v err=%v", state, p.calls, changed, err)
		}
	}
	p := &fakeChildPolicy{state: childPolicyState{1, 0, 4}, failRead: 1}
	if changed, err := prepareWorktreeChild(p); changed || err != nil || len(p.calls) != 1 {
		t.Fatal("initial read failure changed policy")
	}
}
func TestUncertainPolicyMutationRefusesBeforeExec(t *testing.T) {
	for _, p := range []*fakeChildPolicy{
		{failSet: true}, {wrongSet: true}, {failRead: 2}, {failClear: true}, {wrongClear: true}, {failRead: 3},
	} {
		p.state = childPolicyState{1, 0, 4}
		changed, err := prepareWorktreeChild(p)
		if changed || err == nil {
			t.Fatalf("uncertainty accepted: %+v", p)
		}
	}
}
