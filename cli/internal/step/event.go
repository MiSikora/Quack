package step

import "quack/internal/wire"

// Event tells what a run did. Started and then Ended or Failed frame each step, with the Notified of a Call between
// them. Number counts a recipe's steps from 1, a nested recipe's included; it is 0 in a plain action's run. Logged takes
// the number of the step started last, 0 before the first.
type Event interface{ event() }

type Started struct {
	Number int
	Line   string
}

type Notified struct {
	Number       int
	Notification wire.Notification
}

type Ended struct {
	Number int
	Step   Step
	Result wire.MessageResult
}

type Failed struct {
	Number int
	Err    error
}

type Logged struct {
	Number int
	Text   string
}

func (Started) event()  {}
func (Notified) event() {}
func (Ended) event()    {}
func (Failed) event()   {}
func (Logged) event()   {}
