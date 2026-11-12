package exchange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// DefaultMaxBodyBytes limits the size of exchange bodies.
const DefaultMaxBodyBytes = 1 << 20

// EventHandler handles a parsed (and verified) exchange event.
type EventHandler interface {
	HandleEvent(ctx context.Context, ev *Event) error
}

// EventHandlerFunc adapts a function to an EventHandler.
type EventHandlerFunc func(ctx context.Context, ev *Event) error

func (f EventHandlerFunc) HandleEvent(ctx context.Context, ev *Event) error { return f(ctx, ev) }

// Handler is an http.Handler for the exchange url.
//
// It reads and verifies the call, parses it into an *Event, passes it to
// Events and writes the "TRUE|" / "FALSE|" response Pay.nl expects.
// When Events returns an error, Pay.nl will retry the call later.
type Handler struct {
	Verifier     *Verifier
	Events       EventHandler
	MaxBodyBytes int64

	// OnError is called for every failed exchange call (optional).
	OnError func(r *http.Request, ev *Event, err error)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var limit = h.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		h.fail(w, r, nil, http.StatusBadRequest, fmt.Errorf("paynl/exchange: read body: %w", err))
		return
	}

	var keyID string
	if h.Verifier != nil {
		if keyID, err = h.Verifier.Verify(r.Header, body); err != nil {
			h.fail(w, r, nil, http.StatusUnauthorized, err)
			return
		}
	}

	ev, err := ParseRequest(r, body)
	if err != nil {
		h.fail(w, r, nil, http.StatusBadRequest, err)
		return
	}
	ev.KeyID = keyID
	ev.Verified = keyID != ""

	if h.Events != nil {
		if err := h.Events.HandleEvent(r.Context(), ev); err != nil {
			h.fail(w, r, ev, http.StatusInternalServerError, err)
			return
		}
	}

	WriteTrue(w, "")
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, ev *Event, status int, err error) {
	if h.OnError != nil {
		h.OnError(r, ev, err)
	}
	WriteFalse(w, status, err.Error())
}

// WriteTrue writes a successful exchange response.
func WriteTrue(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "TRUE|"+msg)
}

// WriteFalse writes a failed exchange response; Pay.nl will retry the call.
func WriteFalse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "FALSE|"+msg)
}

// Router dispatches events to handlers by topic. All matching handlers are
// called in registration order; errors are joined.
type Router struct {
	mu       sync.RWMutex
	handlers map[Topic][]EventHandler
	any      []EventHandler
}

// NewRouter creates an empty router.
func NewRouter() *Router {
	return &Router{handlers: make(map[Topic][]EventHandler)}
}

// On registers a handler for a topic.
func (r *Router) On(topic Topic, h EventHandler) *Router {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[topic] = append(r.handlers[topic], h)
	return r
}

// OnFunc registers a handler function for a topic.
func (r *Router) OnFunc(topic Topic, f func(context.Context, *Event) error) *Router {
	return r.On(topic, EventHandlerFunc(f))
}

// OnAny registers a handler called for every event.
func (r *Router) OnAny(h EventHandler) *Router {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.any = append(r.any, h)
	return r
}

// HandleEvent implements EventHandler.
func (r *Router) HandleEvent(ctx context.Context, ev *Event) error {
	r.mu.RLock()
	var hs = make([]EventHandler, 0, len(r.any)+len(r.handlers[ev.Topic()]))
	hs = append(hs, r.any...)
	hs = append(hs, r.handlers[ev.Topic()]...)
	r.mu.RUnlock()

	var errs []error
	for _, h := range hs {
		if err := h.HandleEvent(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
