package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// CreateOrder creates a new order (transaction).
//
// If the request has no ServiceID, the client's configured ServiceID is used.
// If the client is in test mode, the order is marked as a test order.
// Redirect the customer to the returned order's CheckoutURL().
func (c *Client) CreateOrder(ctx context.Context, req *CreateOrderRequest) (*Order, error) {
	if req == nil {
		return nil, errors.New("paynl: nil CreateOrderRequest")
	}

	var r = *req
	if r.ServiceID == "" {
		r.ServiceID = c.cfg.ServiceID
	}
	if r.ServiceID == "" {
		return nil, errors.New("paynl: serviceId is required")
	}
	if r.Amount.Value <= 0 {
		return nil, errors.New("paynl: amount must be greater than zero")
	}
	if r.Amount.Currency == "" {
		r.Amount.Currency = "EUR"
	}
	if c.cfg.TestMode && r.Integration == nil {
		r.Integration = &Integration{Test: true}
	}

	var out Order
	if err := c.do(ctx, http.MethodPost, c.cfg.ConnectURL, "/v1/orders", &r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrder retrieves the status of an order by its id (the UUID-like `id`
// returned when creating the order).
func (c *Client) GetOrder(ctx context.Context, id string) (*Order, error) {
	if id == "" {
		return nil, errors.New("paynl: order id is required")
	}
	var out Order
	if err := c.do(ctx, http.MethodGet, c.cfg.ConnectURL, "/v1/orders/"+url.PathEscape(id)+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) patchOrder(ctx context.Context, id, action string, body any) (*Order, error) {
	if id == "" {
		return nil, errors.New("paynl: order id is required")
	}
	var out Order
	if err := c.do(ctx, http.MethodPatch, c.cfg.ConnectURL, "/v1/orders/"+url.PathEscape(id)+"/"+action, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CaptureOrder captures an authorized order. If amount is zero, the full
// authorized amount is captured, otherwise only the given amount (in cents).
func (c *Client) CaptureOrder(ctx context.Context, id string, amount int64) (*Order, error) {
	if amount > 0 {
		return c.patchOrder(ctx, id, "capture/amount", map[string]int64{"amount": amount})
	}
	return c.patchOrder(ctx, id, "capture", nil)
}

// VoidOrder releases an authorization (the reserved amount is not captured).
func (c *Client) VoidOrder(ctx context.Context, id string) (*Order, error) {
	return c.patchOrder(ctx, id, "void", nil)
}

// AbortOrder aborts an order which has not been paid yet.
func (c *Client) AbortOrder(ctx context.Context, id string) (*Order, error) {
	return c.patchOrder(ctx, id, "abort", nil)
}

// ApproveOrder approves an order which is in the VERIFY state.
func (c *Client) ApproveOrder(ctx context.Context, id string) (*Order, error) {
	return c.patchOrder(ctx, id, "approve", nil)
}

// DeclineOrder declines an order which is in the VERIFY state.
func (c *Client) DeclineOrder(ctx context.Context, id string) (*Order, error) {
	return c.patchOrder(ctx, id, "decline", nil)
}
