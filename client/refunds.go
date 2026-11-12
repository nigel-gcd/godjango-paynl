package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Refund refunds (part of) a transaction.
//
// transactionID is the Pay.nl transaction / order id (the `orderId` field of
// an Order, e.g. "51007856048X14b0", or an EX-xxxx-xxxx-xxxx code).
//
// Refunds are processed asynchronously; their progress is reported to the
// exchange url (refund:add, refund:received, ...).
func (c *Client) Refund(ctx context.Context, transactionID string, req *RefundRequest) (*RefundResponse, error) {
	if transactionID == "" {
		return nil, errors.New("paynl: transaction id is required")
	}
	if req == nil {
		req = &RefundRequest{}
	}
	if req.Amount < 0 {
		return nil, errors.New("paynl: refund amount cannot be negative")
	}

	var out RefundResponse
	var path = "/v2/transactions/" + url.PathEscape(transactionID) + "/refund"
	if err := c.do(ctx, http.MethodPatch, c.cfg.RestURL, path, req, &out); err != nil {
		return nil, err
	}
	if out.TransactionID == "" {
		out.TransactionID = transactionID
	}
	return &out, nil
}
