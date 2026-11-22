package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"quack/internal/client"
	"quack/internal/wire"
)

// The calls that start_action detached from the tool call that started them. They end with ctx, which lives as long
// as the server's session.
type runs struct {
	ctx   context.Context
	mu    sync.Mutex
	count int
	// Oldest first.
	all []*run
}

// Everything below cancel is guarded by runs.mu.
type run struct {
	id     string
	action string
	device string
	app    string
	cancel context.CancelFunc
	// The index of notifications[0], past the ones dropped to keep at most maxRunNotifications.
	first         int
	notifications []notificationOutput
	end           *runEnd
	// Closed and replaced on every new notification and at the end, which wakes the reads that wait.
	changed chan struct{}
}

type runEnd struct {
	message string
	data    json.RawMessage
	err     *errorOutput
}

type readRunOutput struct {
	Notifications []notificationOutput `json:"notifications"`
	Next          int                  `json:"next"`
	// The notifications between after and the oldest one the run kept.
	Dropped int  `json:"dropped,omitempty"`
	Done    bool `json:"done"`
	// The run's end, once done. A failed or canceled run has an error instead.
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *errorOutput    `json:"error,omitempty"`
}

type runOutput struct {
	Run    string `json:"run"`
	Action string `json:"action"`
	Device string `json:"device"`
	App    string `json:"app"`
	Done   bool   `json:"done"`
	Next   int    `json:"next"`
}

func newRuns(ctx context.Context) *runs {
	return &runs{ctx: ctx}
}

func (r *runs) start(target client.Target, app App, call wire.Call) string {
	ctx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	r.count++
	started := &run{
		id:      "r" + strconv.Itoa(r.count),
		action:  call.Action,
		device:  string(target.Device),
		app:     target.Package,
		cancel:  cancel,
		changed: make(chan struct{}),
	}
	r.all = append(r.all, started)
	r.mu.Unlock()
	go func() {
		defer cancel()
		result, err := app.Call(ctx, call, func(n wire.Notification) { r.notified(started, notificationOutput(n)) })
		r.ended(started, result, err, ctx.Err() != nil)
	}()
	return started.id
}

// Returns at once when the run has notifications from after on or has ended, and otherwise once one of them happens
// or wait runs out.
func (r *runs) read(ctx context.Context, id string, after int, wait time.Duration) (readRunOutput, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	waited := wait <= 0
	for {
		r.mu.Lock()
		found := r.find(id)
		if found == nil {
			r.mu.Unlock()
			return readRunOutput{}, noRun(id)
		}
		if waited || found.end != nil || found.next() > after {
			out := found.read(after)
			r.mu.Unlock()
			return out, nil
		}
		changed := found.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			waited = true
		case <-ctx.Done():
			return readRunOutput{}, ctx.Err()
		}
	}
}

// A run that has ended stays as it was.
func (r *runs) cancel(id string) error {
	r.mu.Lock()
	found := r.find(id)
	r.mu.Unlock()
	if found == nil {
		return noRun(id)
	}
	found.cancel()
	return nil
}

func (r *runs) list() []runOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []runOutput{}
	for _, held := range r.all {
		out = append(out, runOutput{held.id, held.action, held.device, held.app, held.end != nil, held.next()})
	}
	return out
}

func (r *runs) notified(to *run, notification notificationOutput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	to.notifications = append(to.notifications, notification)
	if len(to.notifications) > maxRunNotifications {
		to.notifications = slices.Delete(to.notifications, 0, 1)
		to.first++
	}
	to.wake()
}

func (r *runs) ended(ended *run, result wire.MessageResult, err error, canceled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case canceled:
		ended.end = &runEnd{err: &errorOutput{"canceled", "the run was canceled"}}
	case err != nil:
		ended.end = &runEnd{err: stepError(err)}
	default:
		ended.end = &runEnd{message: result.Message, data: result.Data}
	}
	ended.wake()
	r.dropFinished()
}

// Drops the oldest finished runs past maxFinishedRuns, so that runs nobody reads do not pile up.
func (r *runs) dropFinished() {
	finished := 0
	for _, held := range r.all {
		if held.end != nil {
			finished++
		}
	}
	r.all = slices.DeleteFunc(r.all, func(held *run) bool {
		if held.end == nil || finished <= maxFinishedRuns {
			return false
		}
		finished--
		return true
	})
}

func (r *runs) find(id string) *run {
	i := slices.IndexFunc(r.all, func(held *run) bool { return held.id == id })
	if i < 0 {
		return nil
	}
	return r.all[i]
}

func (held *run) read(after int) readRunOutput {
	after = min(max(after, 0), held.next())
	out := readRunOutput{Notifications: []notificationOutput{}, Next: held.next(), Done: held.end != nil}
	if after < held.first {
		out.Dropped = held.first - after
		after = held.first
	}
	out.Notifications = append(out.Notifications, held.notifications[after-held.first:]...)
	if held.end != nil {
		out.Message, out.Data, out.Error = held.end.message, held.end.data, held.end.err
	}
	return out
}

func (held *run) next() int { return held.first + len(held.notifications) }

func (held *run) wake() {
	close(held.changed)
	held.changed = make(chan struct{})
}

func noRun(id string) error { return errors.New("no run " + id) }

// Finds what the app would refuse before a detached call starts, so that start_action fails instead of starting a
// run that ends at once. The messages are the app's.
func checkCall(actions []wire.Action, call wire.Call) error {
	i := slices.IndexFunc(actions, func(a wire.Action) bool { return a.ID == call.Action })
	if i < 0 {
		return wire.Error{Code: wire.ErrorCodeUnknownAction, Message: fmt.Sprintf("no action '%s'", call.Action)}
	}
	params := actions[i].Params
	for _, key := range slices.Sorted(maps.Keys(call.Args)) {
		if !slices.ContainsFunc(params, func(p wire.Param) bool { return p.Name == key }) {
			return wire.Error{Code: wire.ErrorCodeInvalidArgs, Message: fmt.Sprintf("unknown argument '%s'", key)}
		}
	}
	for _, p := range params {
		if _, required := p.Presence.(wire.Required); required && len(call.Args[p.Name]) == 0 {
			return wire.Error{Code: wire.ErrorCodeInvalidArgs, Message: fmt.Sprintf("missing argument '%s'", p.Name)}
		}
	}
	return nil
}

const (
	maxRunNotifications = 1000
	maxFinishedRuns     = 100
	maxWait             = time.Minute
)
