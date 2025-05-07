package fsm

import "github.com/centrifugal/centrifuge-go/internal/mutex"

type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
	StateClosed
)

type StateMachine struct {
	// state is the current state of the state machine.
	state State
	// stateMu is used to protect the state of the state machine.
	stateMu mutex.Mutex
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		state: StateDisconnected,
	}
}

func (sm *StateMachine) State() State {
	sm.stateMu.Lock()
	defer sm.stateMu.Unlock()
	return sm.state
}

func (sm *StateMachine) SetState(state State) {
	sm.stateMu.Lock()
	defer sm.stateMu.Unlock()
	sm.state = state
}

func (sm *StateMachine) IsDisconnected() bool {
	return sm.State() == StateDisconnected
}

func (sm *StateMachine) IsConnecting() bool {
	return sm.State() == StateConnecting
}

func (sm *StateMachine) IsConnected() bool {
	return sm.State() == StateConnected
}

func (sm *StateMachine) IsClosed() bool {
	return sm.State() == StateClosed
}
