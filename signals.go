package paynl

import (
	"context"

	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
	"github.com/Nigel2392/go-django/contrib/paynl/internal/prov"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
	"github.com/Nigel2392/go-signals"
)

// SignalPayload carries information about a payment lifecycle event.
type SignalPayload struct {
	Provider      prov.BaseProvider
	TransactionID string
	OrderID       string
	State         paystate.PayState
	AmountCents   int64
	Order         *models.Order
	Payment       *models.Payment
	Event         *exchange.Event
	Raw           []byte
}

// Signals groups all signals emitted by the paynl app/provider.
type Signals struct {
	// Started is emitted when a new transaction is created on Pay.nl.
	Started signals.Signal[*SignalPayload]

	// Updated is emitted on any exchange webhook event after parsing.
	Updated signals.Signal[*SignalPayload]

	// Paid is emitted when an order reaches the PAID status.
	Paid signals.Signal[*SignalPayload]

	// Authorized is emitted when an order reaches the AUTHORIZE or PARTLY_CAPTURED status.
	Authorized signals.Signal[*SignalPayload]

	// Verify is emitted when an order requires manual verification (VERIFY).
	Verify signals.Signal[*SignalPayload]

	// Cancelled is emitted when an order is cancelled, expired, denied, or failed.
	Cancelled signals.Signal[*SignalPayload]

	// Refunded is emitted when a refund is initiated or received.
	Refunded signals.Signal[*SignalPayload]

	// Chargeback is emitted when a chargeback event is received.
	Chargeback signals.Signal[*SignalPayload]
}

// NewSignals initializes the Pay.nl signal channels.
func NewSignals() Signals {
	return Signals{
		Started:    signals.Get[*SignalPayload]("paynl:payment:started"),
		Updated:    signals.Get[*SignalPayload]("paynl:payment:updated"),
		Paid:       signals.Get[*SignalPayload]("paynl:payment:paid"),
		Authorized: signals.Get[*SignalPayload]("paynl:payment:authorized"),
		Verify:     signals.Get[*SignalPayload]("paynl:payment:verify"),
		Cancelled:  signals.Get[*SignalPayload]("paynl:payment:cancelled"),
		Refunded:   signals.Get[*SignalPayload]("paynl:payment:refunded"),
		Chargeback: signals.Get[*SignalPayload]("paynl:payment:chargeback"),
	}
}

// EmitEvent dispatches the exchange event to the Updated signal and any topic-specific signal.
func (s Signals) EmitEvent(ctx context.Context, p prov.BaseProvider, ev *exchange.Event, order *models.Order, payment *models.Payment) error {
	var state = PayStateFromStatus(ev.Status)
	if ev.IsRefund() && state == paystate.STATUS_PENDING {
		state = paystate.STATUS_REFUNDING
	}

	var orderID = ev.Reference
	if orderID == "" {
		orderID = ev.Extra1
	}

	var payload = &SignalPayload{
		Provider:      p,
		TransactionID: ev.TransactionID(),
		OrderID:       orderID,
		State:         state,
		AmountCents:   ev.Amount,
		Order:         order,
		Payment:       payment,
		Event:         ev,
		Raw:           ev.Raw,
	}

	if err := s.Updated.Send(ctx, payload); err != nil {
		return err
	}

	switch ev.Topic() {
	case exchange.TopicPaid:
		return s.Paid.Send(ctx, payload)
	case exchange.TopicAuthorized:
		return s.Authorized.Send(ctx, payload)
	case exchange.TopicVerify:
		return s.Verify.Send(ctx, payload)
	case exchange.TopicCancelled:
		return s.Cancelled.Send(ctx, payload)
	case exchange.TopicRefund:
		return s.Refunded.Send(ctx, payload)
	case exchange.TopicChargeback:
		return s.Chargeback.Send(ctx, payload)
	}
	return nil
}
