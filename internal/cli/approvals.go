package cli

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/tui"
)

// approvalSweep is how often the queue looks for requests that have expired,
// while any waits. Tests shorten it.
var approvalSweep = time.Second

// queued is a request in the queue, and when it stops waiting.
type queued struct {
	approval control.Approval
	expires  time.Time
}

// approvals is the core's reviewer: it keeps what agents ask until it is
// answered or expires, and streams it to the clients.
//
// A request is added to the queue before its "approval" is published, and
// taken off before its "ended" is, both under mu: a state read after an
// envelope's number has every request that envelope adds, and none it ends,
// and the envelopes of one request are published in order.
type approvals struct {
	// publish is the hub's Publish. Tests wrap it to see what is published.
	publish func(control.Envelope) uint64
	now     func() time.Time
	// onAdd, when set, is told of each request once it is queued and
	// published, with mu not held. It is set before the queue reviews
	// anything.
	onAdd func(control.Approval)

	mu      sync.Mutex
	pending []queued // in the order they came
	// sweeping is set while the sweeper runs; closed once close has begun,
	// after which no sweeper starts.
	sweeping, closed bool
	stop             chan struct{}
	sweeper          sync.WaitGroup
}

var _ agentapi.Reviewer = (*approvals)(nil)

// newApprovals is an empty queue that streams to hub, with now for the time.
func newApprovals(hub *control.Hub, now func() time.Time) *approvals {
	return &approvals{publish: hub.Publish, now: now, stop: make(chan struct{})}
}

// Review queues a proposal to add a project.
func (q *approvals) Review(p agentapi.Proposal) {
	q.add(control.Approval{ID: p.ID, Proposal: &p}, p.Expires)
}

// ReviewPassthrough queues a request for a passthrough.
func (q *approvals) ReviewPassthrough(r agentapi.PassRequest) {
	q.add(control.Approval{ID: r.ID, Pass: &r}, r.Expires)
}

// ReviewDocument queues a request to show a document.
func (q *approvals) ReviewDocument(r agentapi.DocRequest) {
	q.add(control.Approval{ID: r.ID, Doc: &r}, r.Expires)
}

// Opened tells the clients of a passthrough that opened without asking.
// Nothing waits on it, so nothing is queued.
func (q *approvals) Opened(p agentapi.Passthrough) {
	q.publish(control.Envelope{Kind: control.KindOpened, Opened: &p})
}

// Shown tells the clients of a document shown without asking.
func (q *approvals) Shown(origin, requester, doc string) {
	q.publish(control.Envelope{Kind: control.KindShown, Shown: &control.Shown{Origin: origin, Requester: requester, Doc: doc}})
}

// Ended takes off the queue a request the agent API has stopped waiting
// on, however it ended, and publishes its "ended"; nothing when it is not
// pending.
func (q *approvals) Ended(id string) { q.take(id) }

// add queues a, which waits until expires, publishes it, and has the sweeper
// look for it expiring.
func (q *approvals) add(a control.Approval, expires time.Time) {
	q.mu.Lock()
	q.sweepLocked()
	q.pending = append(q.pending, queued{approval: a, expires: expires})
	q.publish(control.Envelope{Kind: control.KindApproval, Approval: &a})
	if !q.sweeping && !q.closed {
		q.sweeping = true
		q.sweeper.Add(1)
		go q.sweep()
	}
	q.mu.Unlock()
	if q.onAdd != nil {
		q.onAdd(a)
	}
}

// Pending is what waits for an answer, oldest first. Requests that have
// expired are dropped, and their "ended" published, first. A nil queue has
// nothing pending.
func (q *approvals) Pending() []control.Approval {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweepLocked()
	out := make([]control.Approval, len(q.pending))
	for i, p := range q.pending {
		out[i] = p.approval
	}
	return out
}

// take removes id and publishes "ended"; false when it was not pending.
func (q *approvals) take(id string) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.IndexFunc(q.pending, func(p queued) bool { return p.approval.ID == id })
	if i < 0 {
		return false
	}
	q.endLocked(i)
	return true
}

// endLocked removes the request at i and publishes its "ended".
func (q *approvals) endLocked(i int) {
	id := q.pending[i].approval.ID
	q.pending = slices.Delete(q.pending, i, i+1)
	q.publish(control.Envelope{Kind: control.KindEnded, ID: id})
}

// sweepLocked ends the requests that have expired.
func (q *approvals) sweepLocked() {
	now := q.now()
	for i := 0; i < len(q.pending); {
		if now.After(q.pending[i].expires) {
			q.endLocked(i)
			continue
		}
		i++
	}
}

// sweep ends the requests that expire, looking every approvalSweep, until
// none is pending or the queue is closed.
func (q *approvals) sweep() {
	defer q.sweeper.Done()
	tick := time.NewTicker(approvalSweep)
	defer tick.Stop()
	for {
		select {
		case <-q.stop:
			return
		case <-tick.C:
		}
		q.mu.Lock()
		q.sweepLocked()
		if len(q.pending) == 0 {
			q.sweeping = false
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
	}
}

// close stops the sweeper and waits for it. The queue still takes requests,
// but no longer sweeps them on its own.
func (q *approvals) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.stop)
	q.mu.Unlock()
	q.sweeper.Wait()
}

// approvalSummary says what a request asks, in a sentence without its full
// stop: "Claude Code on mini asks to open port 5173". What the agent sent
// is made safe to print.
func approvalSummary(a control.Approval) string {
	var s string
	switch {
	case a.Pass != nil:
		s = fmt.Sprintf("%s %s asks to open port %d", tui.RequesterName(a.Pass.Requester), onOrigin(a.Pass.Origin), a.Pass.Remote)
	case a.Doc != nil:
		s = fmt.Sprintf("%s %s asks to show %s", tui.RequesterName(a.Doc.Requester), onOrigin(a.Doc.Origin), a.Doc.Doc)
	case a.Proposal != nil:
		s = fmt.Sprintf("A coding agent %s proposes adding %s", onOrigin(a.Proposal.Origin), a.Proposal.Path)
	}
	return printable(s)
}

// onOrigin names the machine a request came from in a sentence, as the
// interface does: "on mini", or "on this machine".
func onOrigin(origin string) string {
	if origin == config.LocalServer {
		return "on this machine"
	}
	return "on " + origin
}

// approvalLine is what up and the core's log say of a request as it comes:
// what it asks, and where to answer it. pageURL is the address of
// Nearhost's page, or "" when it is not served. A proposal is only
// declined on the page: accepting one takes the interface.
func approvalLine(a control.Approval, pageURL string) string {
	if pageURL == "" {
		return approvalSummary(a) + ". Open nearhost to answer."
	}
	link := pageURL + "/approve/" + a.ID
	if a.Proposal != nil {
		return approvalSummary(a) + ". Review it with nearhost, or decline it at " + link
	}
	return approvalSummary(a) + ". Answer at " + link
}
