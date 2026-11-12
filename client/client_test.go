package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nigel2392/go-django/contrib/paynl/client"
)

type recorded struct {
	method, path, user, pass string
	body                     map[string]any
}

// handlerTransport serves requests directly through an http.Handler,
// so tests do not need to open sockets.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var w = httptest.NewRecorder()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	t.h.ServeHTTP(w, r)
	var resp = w.Result()
	resp.Request = r
	return resp, nil
}

type fakeServer struct {
	URL    string
	Client *http.Client
}

func newServer(t *testing.T, status int, response string, rec *recorded) *fakeServer {
	t.Helper()
	var handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path = r.Method, r.URL.Path
		rec.user, rec.pass, _ = r.BasicAuth()
		var b, _ = io.ReadAll(r.Body)
		rec.body = nil
		if len(b) > 0 {
			if err := json.Unmarshal(b, &rec.body); err != nil {
				t.Errorf("invalid json body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, response)
	})
	return &fakeServer{
		URL:    "https://paynl.test",
		Client: &http.Client{Transport: handlerTransport{handler}},
	}
}

func newClient(t *testing.T, srv *fakeServer, test bool) *client.Client {
	t.Helper()
	var c, err = client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "secret",
		ServiceID:  "SL-1111-2222",
		TestMode:   test,
		ConnectURL: srv.URL,
		RestURL:    srv.URL,
		HTTPClient: srv.Client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const orderResponse = `{
	"id": "67f531c2-5114-8389-203a-051007856049",
	"orderId": "51007856048X14b0",
	"reference": "REF1234",
	"status": {"code": 20, "action": "PENDING"},
	"amount": {"value": 1500, "currency": "EUR"},
	"links": {
		"status": "https://connect.pay.nl/v1/orders/67f531c2/status",
		"checkout": "https://checkout.pay.nl/to/payment/67f531c2",
		"redirect": "https://checkout.pay.nl/to/payment/67f531c2"
	}
}`

func TestCreateOrder(t *testing.T) {
	var rec recorded
	var srv = newServer(t, http.StatusCreated, orderResponse, &rec)
	var c = newClient(t, srv, true)

	order, err := c.CreateOrder(context.Background(), &client.CreateOrderRequest{
		Amount:      client.Amount{Value: 1500},
		Reference:   "REF1234",
		Description: "Order #1234",
		ReturnURL:   "https://example.com/return",
		ExchangeURL: "https://example.com/exchange",
	})
	if err != nil {
		t.Fatal(err)
	}

	if rec.method != http.MethodPost || rec.path != "/v1/orders" {
		t.Fatalf("unexpected request %s %s", rec.method, rec.path)
	}
	if rec.user != "AT-1234-5678" || rec.pass != "secret" {
		t.Fatalf("unexpected basic auth %q:%q", rec.user, rec.pass)
	}
	if rec.body["serviceId"] != "SL-1111-2222" {
		t.Fatalf("expected default serviceId, got %v", rec.body["serviceId"])
	}
	if amt := rec.body["amount"].(map[string]any); amt["currency"] != "EUR" || amt["value"].(float64) != 1500 {
		t.Fatalf("unexpected amount %v", amt)
	}
	if integ, _ := rec.body["integration"].(map[string]any); integ == nil || integ["test"] != true {
		t.Fatalf("expected integration.test=true, got %v", rec.body["integration"])
	}

	if order.OrderID != "51007856048X14b0" || order.CheckoutURL() != "https://checkout.pay.nl/to/payment/67f531c2" {
		t.Fatalf("unexpected order %+v", order)
	}
	if !order.Status.IsPending() || order.Status.IsFinal() {
		t.Fatalf("expected pending status, got %+v", order.Status)
	}
	if len(order.Raw) == 0 {
		t.Fatal("expected raw response to be kept")
	}
}

func TestCreateOrderValidation(t *testing.T) {
	var rec recorded
	var srv = newServer(t, http.StatusCreated, orderResponse, &rec)
	var c = newClient(t, srv, false)

	if _, err := c.CreateOrder(context.Background(), &client.CreateOrderRequest{}); err == nil {
		t.Fatal("expected error for zero amount")
	}
	if rec.method != "" {
		t.Fatal("no request should have been made")
	}
}

func TestOrderActions(t *testing.T) {
	var tests = []struct {
		name string
		call func(c *client.Client) (*client.Order, error)
		meth string
		path string
		body map[string]any
	}{
		{"status", func(c *client.Client) (*client.Order, error) { return c.GetOrder(context.Background(), "abc") }, http.MethodGet, "/v1/orders/abc/status", nil},
		{"capture", func(c *client.Client) (*client.Order, error) { return c.CaptureOrder(context.Background(), "abc", 0) }, http.MethodPatch, "/v1/orders/abc/capture", nil},
		{"capture-amount", func(c *client.Client) (*client.Order, error) { return c.CaptureOrder(context.Background(), "abc", 500) }, http.MethodPatch, "/v1/orders/abc/capture/amount", map[string]any{"amount": float64(500)}},
		{"void", func(c *client.Client) (*client.Order, error) { return c.VoidOrder(context.Background(), "abc") }, http.MethodPatch, "/v1/orders/abc/void", nil},
		{"abort", func(c *client.Client) (*client.Order, error) { return c.AbortOrder(context.Background(), "abc") }, http.MethodPatch, "/v1/orders/abc/abort", nil},
		{"approve", func(c *client.Client) (*client.Order, error) { return c.ApproveOrder(context.Background(), "abc") }, http.MethodPatch, "/v1/orders/abc/approve", nil},
		{"decline", func(c *client.Client) (*client.Order, error) { return c.DeclineOrder(context.Background(), "abc") }, http.MethodPatch, "/v1/orders/abc/decline", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec recorded
			var srv = newServer(t, http.StatusOK, orderResponse, &rec)
			if _, err := tt.call(newClient(t, srv, false)); err != nil {
				t.Fatal(err)
			}
			if rec.method != tt.meth || rec.path != tt.path {
				t.Fatalf("expected %s %s, got %s %s", tt.meth, tt.path, rec.method, rec.path)
			}
			if tt.body != nil && rec.body["amount"] != tt.body["amount"] {
				t.Fatalf("unexpected body %v", rec.body)
			}
		})
	}
}

func TestRefund(t *testing.T) {
	var rec recorded
	var srv = newServer(t, http.StatusOK, `{"description":"refund","processDate":"2026-11-12"}`, &rec)
	var c = newClient(t, srv, false)

	res, err := c.Refund(context.Background(), "51007856048X14b0", &client.RefundRequest{
		Amount: 500, Description: "Returned item",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.path != "/v2/transactions/51007856048X14b0/refund" {
		t.Fatalf("unexpected request %s %s", rec.method, rec.path)
	}
	if rec.body["amount"].(float64) != 500 || rec.body["description"] != "Returned item" {
		t.Fatalf("unexpected body %v", rec.body)
	}
	if res.TransactionID != "51007856048X14b0" || res.ProcessDate != "2026-11-12" || len(res.Raw) == 0 {
		t.Fatalf("unexpected response %+v", res)
	}
}

func TestAPIError(t *testing.T) {
	var rec recorded
	var srv = newServer(t, http.StatusUnprocessableEntity, `{
		"type": "https://developer.pay.nl/docs/errors",
		"title": "Validation failed",
		"detail": "amount: This value should be greater than 0.",
		"violations": [{"propertyPath": "amount", "message": "This value should be greater than 0.", "code": "PAY-1234"}]
	}`, &rec)

	_, err := newClient(t, srv, false).GetOrder(context.Background(), "abc")
	var apiErr *client.APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T %v", err, err)
	}
	if apiErr.StatusCode != 422 || apiErr.Title != "Validation failed" || len(apiErr.Violations) != 1 {
		t.Fatalf("unexpected error %+v", apiErr)
	}
}

func TestErrorHelpers(t *testing.T) {
	var rec recorded
	var srv = newServer(t, http.StatusNotFound, `{"title":"Not found"}`, &rec)
	_, err := newClient(t, srv, false).GetOrder(context.Background(), "abc")
	if !client.IsNotFound(err) || client.IsUnauthorized(err) {
		t.Fatalf("unexpected helper results for %v", err)
	}
}

func TestStatusFallback(t *testing.T) {
	var tests = []struct {
		status client.Status
		check  func(client.Status) bool
	}{
		{client.Status{Code: 100}, client.Status.IsPaid},
		{client.Status{Code: -90}, client.Status.IsCancelled},
		{client.Status{Code: -81}, client.Status.IsRefund},
		{client.Status{Code: 95}, client.Status.IsAuthorized},
		{client.Status{Action: "paid"}, client.Status.IsPaid},
		{client.Status{Code: 20}, client.Status.IsPending},
	}
	for _, tt := range tests {
		if !tt.check(tt.status) {
			t.Errorf("unexpected result for %+v", tt.status)
		}
	}
}

func TestClientNewConfigAndHelpers(t *testing.T) {
	if _, err := client.New(client.Config{Password: "secret"}); err == nil {
		t.Fatal("expected error when username is missing")
	}
	if _, err := client.New(client.Config{Username: "AT-1234"}); err == nil {
		t.Fatal("expected error when password is missing")
	}

	var c, err = client.New(client.Config{
		Username:   "AT-1234",
		Password:   "secret",
		ConnectURL: "https://connect.example.com/",
		RestURL:    "https://rest.example.com///",
	})
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}
	var cfg = c.Config()
	if cfg.ConnectURL != "https://connect.example.com" || cfg.RestURL != "https://rest.example.com" {
		t.Fatalf("expected trailing slashes trimmed, got %+v", cfg)
	}

	var amt = client.EUR(1234)
	if amt.Value != 1234 || amt.Currency != "EUR" {
		t.Fatalf("unexpected EUR amount: %+v", amt)
	}

	var ord = &client.Order{Links: client.Links{Redirect: "https://redirect.example.com"}}
	if ord.CheckoutURL() != "https://redirect.example.com" {
		t.Fatalf("expected redirect URL fallback, got %q", ord.CheckoutURL())
	}
	ord.Links.Checkout = "https://checkout.example.com"
	if ord.CheckoutURL() != "https://checkout.example.com" {
		t.Fatalf("expected checkout URL precedence, got %q", ord.CheckoutURL())
	}
}

func TestClientValidationErrors(t *testing.T) {
	var called int
	var h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
	})
	var srv, err = client.New(client.Config{
		Username:   "AT-1234",
		Password:   "secret",
		HTTPClient: &http.Client{Transport: handlerTransport{h}},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	var ctx = context.Background()
	if _, err := srv.CreateOrder(ctx, nil); err == nil {
		t.Fatal("expected error for nil CreateOrderRequest")
	}
	if _, err := srv.CreateOrder(ctx, &client.CreateOrderRequest{}); err == nil {
		t.Fatal("expected error for empty ServiceID")
	}
	if _, err := srv.GetOrder(ctx, ""); err == nil {
		t.Fatal("expected error for empty GetOrder id")
	}
	if _, err := srv.CaptureOrder(ctx, "", 0); err == nil {
		t.Fatal("expected error for empty CaptureOrder id")
	}
	if _, err := srv.VoidOrder(ctx, ""); err == nil {
		t.Fatal("expected error for empty VoidOrder id")
	}
	if _, err := srv.AbortOrder(ctx, ""); err == nil {
		t.Fatal("expected error for empty AbortOrder id")
	}
	if _, err := srv.ApproveOrder(ctx, ""); err == nil {
		t.Fatal("expected error for empty ApproveOrder id")
	}
	if _, err := srv.DeclineOrder(ctx, ""); err == nil {
		t.Fatal("expected error for empty DeclineOrder id")
	}
	if _, err := srv.Refund(ctx, "", &client.RefundRequest{Amount: 100}); err == nil {
		t.Fatal("expected error for empty Refund transaction id")
	}
	if _, err := srv.Refund(ctx, "TX-1", &client.RefundRequest{Amount: -1}); err == nil {
		t.Fatal("expected error for negative Refund amount")
	}
	if called != 0 {
		t.Fatalf("expected zero HTTP calls on validation failures, got %d", called)
	}
}

func TestAPIErrorFormats(t *testing.T) {
	var cases = []struct {
		name       string
		status     int
		body       string
		wantSubstr string
		unauth     bool
		notFound   bool
	}{
		{
			name:       "rest json error",
			status:     http.StatusUnauthorized,
			body:       `{"message":"Unauthorized request","errors":{"general":{"code":"E401","message":"bad key"}}}`,
			wantSubstr: "Unauthorized request",
			unauth:     true,
		},
		{
			name:       "forbidden status",
			status:     http.StatusForbidden,
			body:       `{"title":"Forbidden"}`,
			wantSubstr: "Forbidden",
			unauth:     true,
		},
		{
			name:       "detail only",
			status:     http.StatusNotFound,
			body:       `{"detail":"missing order"}`,
			wantSubstr: "missing order",
			notFound:   true,
		},
		{
			name:       "plain text fallback",
			status:     http.StatusBadGateway,
			body:       `upstream unavailable`,
			wantSubstr: "Bad Gateway",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			var c, newErr = client.New(client.Config{
				Username:   "AT-1234",
				Password:   "secret",
				HTTPClient: &http.Client{Transport: handlerTransport{h}},
			})
			if newErr != nil {
				t.Fatalf("client.New: %v", newErr)
			}
			var _, err = c.GetOrder(context.Background(), "ORD-ERR")
			if err == nil {
				t.Fatal("expected error")
			}
			var apiErr *client.APIError
			if !asAPIError(err, &apiErr) {
				t.Fatalf("expected *client.APIError, got %T", err)
			}
			if !strings.Contains(apiErr.Error(), tc.wantSubstr) {
				t.Fatalf("expected %q in error string %q", tc.wantSubstr, apiErr.Error())
			}
			if client.IsUnauthorized(err) != tc.unauth {
				t.Fatalf("IsUnauthorized() = %v, want %v", client.IsUnauthorized(err), tc.unauth)
			}
			if client.IsNotFound(err) != tc.notFound {
				t.Fatalf("IsNotFound() = %v, want %v", client.IsNotFound(err), tc.notFound)
			}
		})
	}
}

func TestActionForCodeAndStatusFlags(t *testing.T) {
	var codeCases = []struct {
		code   int
		action client.Action
		wantOK bool
		final  bool
		cb     bool
	}{
		{0, "", false, false, false},
		{20, client.ActionPending, true, false, false},
		{25, client.ActionPending, true, false, false},
		{50, client.ActionPending, true, false, false},
		{90, client.ActionPending, true, false, false},
		{98, client.ActionPending, true, false, false},
		{-90, client.ActionCancel, true, true, false},
		{-80, client.ActionExpired, true, true, false},
		{-63, client.ActionDenied, true, true, false},
		{-64, client.ActionDenied, true, true, false},
		{-60, client.ActionFailure, true, true, false},
		{-51, client.ActionPaidCheckAmount, true, false, false},
		{80, client.ActionPartialPayment, true, false, false},
		{85, client.ActionVerify, true, false, false},
		{95, client.ActionAuthorize, true, false, false},
		{97, client.ActionPartlyCaptured, true, false, false},
		{100, client.ActionPaid, true, true, false},
		{-71, client.ActionChargeback, true, false, true},
		{-81, client.ActionRefund, true, true, false},
		{-82, client.ActionPartialRefund, true, false, false},
		{999, "", false, false, false},
	}
	for _, tc := range codeCases {
		var got, ok = client.ActionForCode(tc.code)
		if got != tc.action || ok != tc.wantOK {
			t.Errorf("ActionForCode(%d) = (%q, %v), want (%q, %v)", tc.code, got, ok, tc.action, tc.wantOK)
		}
		var st = client.Status{Code: tc.code, Action: got}
		if st.IsFinal() != tc.final {
			t.Errorf("Status(%d/%q).IsFinal() = %v, want %v", tc.code, got, st.IsFinal(), tc.final)
		}
		if st.IsChargeback() != tc.cb {
			t.Errorf("Status(%d/%q).IsChargeback() = %v, want %v", tc.code, got, st.IsChargeback(), tc.cb)
		}
	}
}

func asAPIError(err error, target **client.APIError) bool {
	e, ok := err.(*client.APIError)
	if ok {
		*target = e
	}
	return ok
}
