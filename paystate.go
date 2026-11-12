package paynl

import (
	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
)

// PayStateFromStatus maps a Pay.nl client.Status to a go-django shop paystate.PayState.
func PayStateFromStatus(s client.Status) paystate.PayState {
	switch s.Normalized().Action {
	case client.ActionInit:
		return paystate.STATUS_INIT
	case client.ActionPending:
		return paystate.STATUS_PENDING
	case client.ActionCancel:
		return paystate.STATUS_CANCEL
	case client.ActionExpired:
		return paystate.STATUS_EXPIRED
	case client.ActionDenied:
		return paystate.STATUS_DENIED
	case client.ActionFailure:
		return paystate.STATUS_FAILURE
	case client.ActionPaidCheckAmount:
		return paystate.STATUS_PAID_CHECKAMOUNT
	case client.ActionPartialPayment:
		return paystate.STATUS_PARTIAL_PAYMENT
	case client.ActionVerify:
		return paystate.STATUS_VERIFY
	case client.ActionAuthorize:
		return paystate.STATUS_AUTHORIZE
	case client.ActionPartlyCaptured:
		return paystate.STATUS_PARTLY_CAPTURED
	case client.ActionPaid:
		return paystate.STATUS_PAID
	case client.ActionChargeback:
		return paystate.STATUS_CHARGEBACK
	case client.ActionRefunding:
		return paystate.STATUS_REFUNDING
	case client.ActionRefund:
		return paystate.STATUS_REFUND
	case client.ActionPartialRefund:
		return paystate.STATUS_PARTIAL_REFUND
	default:
		return paystate.STATUS_PENDING
	}
}
