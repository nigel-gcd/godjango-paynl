package client

import "strings"

// Action is the textual status of an order / transaction as reported by Pay.nl.
type Action string

const (
	ActionInit            Action = "INIT"
	ActionPending         Action = "PENDING"
	ActionCancel          Action = "CANCEL"
	ActionExpired         Action = "EXPIRED"
	ActionDenied          Action = "DENIED"
	ActionFailure         Action = "FAILURE"
	ActionPaidCheckAmount Action = "PAID_CHECKAMOUNT"
	ActionPartialPayment  Action = "PARTIAL_PAYMENT"
	ActionVerify          Action = "VERIFY"
	ActionAuthorize       Action = "AUTHORIZE"
	ActionPartlyCaptured  Action = "PARTLY_CAPTURED"
	ActionPaid            Action = "PAID"
	ActionChargeback      Action = "CHARGEBACK"
	ActionRefunding       Action = "REFUNDING"
	ActionRefund          Action = "REFUND"
	ActionPartialRefund   Action = "PARTIAL_REFUND"
)

// codeActions maps numeric Pay.nl status codes to their action.
//
// The action string returned by the API is authoritative; this map is only
// used as a fallback when an action is missing (e.g. legacy exchange calls).
//
// NOTE: verify against https://developer.pay.nl when adding codes.
var codeActions = map[int]Action{
	20:  ActionPending,
	25:  ActionPending,
	50:  ActionPending,
	90:  ActionPending,
	98:  ActionPending,
	80:  ActionPartialPayment,
	85:  ActionVerify,
	95:  ActionAuthorize,
	97:  ActionPartlyCaptured,
	100: ActionPaid,
	-51: ActionPaidCheckAmount,
	-60: ActionFailure,
	-63: ActionDenied,
	-64: ActionDenied,
	-71: ActionChargeback,
	-80: ActionExpired,
	-81: ActionRefund,
	-82: ActionPartialRefund,
	-90: ActionCancel,
}

// ActionForCode returns the action for a numeric status code.
func ActionForCode(code int) (Action, bool) {
	a, ok := codeActions[code]
	return a, ok
}

// Status is the status object returned by the API.
type Status struct {
	Code   int    `json:"code"`
	Action Action `json:"action"`
}

// Normalized returns the status with an upper-cased action, falling back to
// the action derived from the code if the action is empty.
func (s Status) Normalized() Status {
	s.Action = Action(strings.ToUpper(strings.TrimSpace(string(s.Action))))
	if s.Action == "" {
		if a, ok := ActionForCode(s.Code); ok {
			s.Action = a
		}
	}
	return s
}

func (s Status) action() Action { return s.Normalized().Action }

// IsPaid reports whether the order has been (fully) paid.
func (s Status) IsPaid() bool { return s.action() == ActionPaid }

// IsAuthorized reports whether the payment is reserved and can be captured.
func (s Status) IsAuthorized() bool {
	var a = s.action()
	return a == ActionAuthorize || a == ActionPartlyCaptured
}

// IsPending reports whether the order is still awaiting payment.
func (s Status) IsPending() bool {
	switch s.action() {
	case ActionInit, ActionPending, ActionVerify, ActionPartialPayment:
		return true
	}
	return false
}

// IsCancelled reports whether the order was cancelled, expired, denied or failed.
func (s Status) IsCancelled() bool {
	switch s.action() {
	case ActionCancel, ActionExpired, ActionDenied, ActionFailure:
		return true
	}
	return false
}

// IsRefund reports whether the order is (being) refunded.
func (s Status) IsRefund() bool {
	switch s.action() {
	case ActionRefund, ActionPartialRefund, ActionRefunding:
		return true
	}
	return false
}

// IsChargeback reports whether a chargeback occurred.
func (s Status) IsChargeback() bool { return s.action() == ActionChargeback }

// IsFinal reports whether the status will not change anymore without
// merchant interaction (refunds and chargebacks can still happen after PAID).
func (s Status) IsFinal() bool {
	return s.IsPaid() || s.IsCancelled() || s.action() == ActionRefund
}
