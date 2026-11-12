// Package exchange handles Pay.nl exchange calls (webhooks).
//
// Pay.nl calls the exchange url whenever the status of an order / transaction
// changes, and for refund events. Two formats are supported:
//
//   - Signed JSON (TGU / Order API): a JSON body like
//     {"event":"status_changed","type":"order","id":"...","object":{...}}
//     with Signature-* headers (HMAC over the raw body).
//   - Legacy GET / POST (form) calls with parameters such as
//     action=new_ppt&order_id=...&extra1=...
//
// Both are normalized into an *Event. The exchange must answer with a body
// starting with "TRUE" for Pay.nl to consider the call handled, otherwise the
// call is retried. [Handler] takes care of that.
//
// This package depends only on the standard library and the client package.
package exchange

import (
	"strings"

	"github.com/Nigel2392/go-django/contrib/paynl/client"
)

// Format is the wire format of an exchange call.
type Format string

const (
	FormatJSON   Format = "json"   // TGU signed JSON
	FormatLegacy Format = "legacy" // GET / form POST
)

// Topic is a coarse classification of an event, useful for routing.
type Topic string

const (
	TopicUnknown    Topic = "unknown"
	TopicPending    Topic = "pending"
	TopicVerify     Topic = "verify"
	TopicAuthorized Topic = "authorized"
	TopicPaid       Topic = "paid"
	TopicCancelled  Topic = "cancelled"
	TopicRefund     Topic = "refund"
	TopicChargeback Topic = "chargeback"
)

// Legacy exchange actions.
const (
	LegacyActionNewPPT         = "new_ppt"
	LegacyActionPending        = "pending"
	LegacyActionCancel         = "cancel"
	LegacyActionVerify         = "verify"
	LegacyActionCapture        = "capture"
	LegacyActionRefundAdd      = "refund:add"
	LegacyActionRefundSend     = "refund:send"
	LegacyActionRefundReceived = "refund:received"
	LegacyActionRefundStorno   = "refund:storno"
)

var legacyActionStatus = map[string]client.Action{
	LegacyActionNewPPT:         client.ActionPaid,
	LegacyActionPending:        client.ActionPending,
	LegacyActionCancel:         client.ActionCancel,
	LegacyActionVerify:         client.ActionVerify,
	LegacyActionCapture:        client.ActionPaid,
	LegacyActionRefundAdd:      client.ActionRefunding,
	LegacyActionRefundSend:     client.ActionRefunding,
	LegacyActionRefundReceived: client.ActionRefund,
}

// Event is a normalized exchange call.
type Event struct {
	Format Format

	// Name is the event / action name, e.g. "status_changed" (JSON) or
	// "new_ppt", "refund:add" (legacy). Always lower case.
	Name string

	// Type is the object type for JSON events (e.g. "order"), empty for legacy.
	Type string

	// ID is the Order API id (UUID-like). Only set for JSON events.
	ID string

	// OrderID is the Pay.nl transaction id (e.g. "51007856048X14b0").
	OrderID string

	// Reference is the merchant reference given when creating the order.
	Reference string

	// Status is the (normalized) status of the order / transaction.
	Status client.Status

	// Amount in cents, if known.
	Amount int64

	// Extra1..3 are the stats extra fields given when creating the order.
	Extra1, Extra2, Extra3 string

	// Order is the full order object for JSON events (may be nil).
	Order *client.Order

	// Params holds all raw parameters for legacy calls.
	Params map[string][]string

	// Raw is the raw request body (empty for legacy GET calls).
	Raw []byte

	// KeyID is the Signature-KeyId of a verified signed call.
	KeyID string

	// Verified reports whether the call's signature was verified.
	Verified bool
}

// IsRefund reports whether this is a refund related event.
func (e *Event) IsRefund() bool {
	return strings.HasPrefix(e.Name, "refund") || e.Status.IsRefund()
}

// Topic classifies the event.
func (e *Event) Topic() Topic {
	switch {
	case e.IsRefund():
		return TopicRefund
	case e.Status.IsChargeback():
		return TopicChargeback
	case e.Status.IsPaid():
		return TopicPaid
	case e.Status.IsAuthorized():
		return TopicAuthorized
	case e.Status.IsCancelled():
		return TopicCancelled
	case e.Status.Normalized().Action == client.ActionVerify:
		return TopicVerify
	case e.Status.IsPending():
		return TopicPending
	}
	return TopicUnknown
}

// TransactionID returns the best available identifier: the Order API id,
// or the Pay.nl transaction id for legacy calls.
func (e *Event) TransactionID() string {
	if e.ID != "" {
		return e.ID
	}
	return e.OrderID
}
