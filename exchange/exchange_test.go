package exchange_test

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
)

const jsonExchange = `{
	"event": "status_changed",
	"type": "order",
	"version": 1,
	"id": "67f531c2-5114-8389-203a-051007856049",
	"object": {
		"id": "67f531c2-5114-8389-203a-051007856049",
		"orderId": "51007856048X14b0",
		"reference": "REF1234",
		"status": {"code": 100, "action": "PAID"},
		"amount": {"value": 1500, "currency": "EUR"},
		"stats": {"extra1": "order-1"}
	}
}`

func signedRequest(body, keyID, secret string) *http.Request {
	var r = httptest.NewRequest(http.MethodPost, "/exchange", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, secret, []byte(body)))
	r.Header.Set(exchange.HeaderSignatureMethod, "HMAC")
	r.Header.Set(exchange.HeaderSignatureAlgorithm, "sha256")
	r.Header.Set(exchange.HeaderSignatureKeyID, keyID)
	return r
}

func TestParseJSON(t *testing.T) {
	ev, err := exchange.ParseJSON([]byte(jsonExchange))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Format != exchange.FormatJSON || ev.Name != "status_changed" || ev.Type != "order" {
		t.Fatalf("unexpected event %+v", ev)
	}
	if ev.ID != "67f531c2-5114-8389-203a-051007856049" || ev.OrderID != "51007856048X14b0" {
		t.Fatalf("unexpected ids %q %q", ev.ID, ev.OrderID)
	}
	if ev.Topic() != exchange.TopicPaid || ev.Amount != 1500 || ev.Extra1 != "order-1" || ev.Reference != "REF1234" {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestParseLegacy(t *testing.T) {
	var tests = []struct {
		query  string
		topic  exchange.Topic
		amount int64
	}{
		{"action=new_ppt&order_id=123X456&amount=12.50&extra1=a", exchange.TopicPaid, 1250},
		{"action=pending&order_id=123X456", exchange.TopicPending, 0},
		{"action=cancel&order_id=123X456", exchange.TopicCancelled, 0},
		{"action=refund:add&order_id=123X456&amount=500", exchange.TopicRefund, 500},
		{"action=refund:received&order_id=123X456", exchange.TopicRefund, 0},
		{"action=refund:storno&order_id=123X456", exchange.TopicRefund, 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			var r = httptest.NewRequest(http.MethodGet, "/exchange?"+tt.query, nil)
			ev, err := exchange.ParseRequest(r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Format != exchange.FormatLegacy || ev.OrderID != "123X456" {
				t.Fatalf("unexpected event %+v", ev)
			}
			if ev.Topic() != tt.topic || ev.Amount != tt.amount {
				t.Fatalf("expected topic %s amount %d, got %s %d", tt.topic, tt.amount, ev.Topic(), ev.Amount)
			}
		})
	}
}

func TestParseLegacyFormPost(t *testing.T) {
	var body = "action=new_ppt&order_id=999X1"
	var r = httptest.NewRequest(http.MethodPost, "/exchange", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ev, err := exchange.ParseRequest(r, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.OrderID != "999X1" || !ev.Status.IsPaid() {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestParseEmpty(t *testing.T) {
	var r = httptest.NewRequest(http.MethodGet, "/exchange", nil)
	if _, err := exchange.ParseRequest(r, nil); !errors.Is(err, exchange.ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestVerifier(t *testing.T) {
	var v = &exchange.Verifier{
		Secrets:  exchange.StaticSecrets(map[string]string{"SL-1111-2222": "s3cret"}),
		Required: true,
	}

	var r = signedRequest(jsonExchange, "SL-1111-2222", "s3cret")
	if key, err := v.Verify(r.Header, []byte(jsonExchange)); err != nil || key != "SL-1111-2222" {
		t.Fatalf("expected valid signature, got %q %v", key, err)
	}

	r = signedRequest(jsonExchange, "SL-1111-2222", "wrong")
	if _, err := v.Verify(r.Header, []byte(jsonExchange)); !errors.Is(err, exchange.ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}

	r = signedRequest(jsonExchange, "SL-9999-9999", "s3cret")
	if _, err := v.Verify(r.Header, []byte(jsonExchange)); !errors.Is(err, exchange.ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey, got %v", err)
	}

	if _, err := v.Verify(http.Header{}, []byte(jsonExchange)); !errors.Is(err, exchange.ErrMissingSignature) {
		t.Fatalf("expected ErrMissingSignature, got %v", err)
	}

	v.Required = false
	if key, err := v.Verify(http.Header{}, nil); err != nil || key != "" {
		t.Fatalf("expected unsigned call to pass, got %q %v", key, err)
	}
}

func TestHandler(t *testing.T) {
	var got []*exchange.Event
	var router = exchange.NewRouter().
		OnFunc(exchange.TopicPaid, func(ctx context.Context, ev *exchange.Event) error {
			got = append(got, ev)
			return nil
		}).
		OnFunc(exchange.TopicRefund, func(ctx context.Context, ev *exchange.Event) error {
			return errors.New("refund handler failed")
		})

	var h = &exchange.Handler{
		Verifier: &exchange.Verifier{Secrets: exchange.StaticSecrets(map[string]string{"SL-1": "s"})},
		Events:   router,
	}

	// signed json, paid
	var w = httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(jsonExchange, "SL-1", "s"))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE") {
		t.Fatalf("expected TRUE, got %d %q", w.Code, w.Body.String())
	}
	if len(got) != 1 || !got[0].Verified || got[0].KeyID != "SL-1" {
		t.Fatalf("expected one verified event, got %+v", got)
	}

	// bad signature
	w = httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(jsonExchange, "SL-1", "bad"))
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Body.String(), "FALSE") {
		t.Fatalf("expected FALSE 401, got %d %q", w.Code, w.Body.String())
	}

	// handler error -> FALSE so pay.nl retries
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/exchange?action=refund:add&order_id=1X2", nil))
	if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Body.String(), "FALSE") {
		t.Fatalf("expected FALSE 500, got %d %q", w.Code, w.Body.String())
	}

	// unrouted topic is acknowledged
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/exchange?action=pending&order_id=1X2", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected TRUE for unrouted topic, got %d %q", w.Code, w.Body.String())
	}
}

func TestEventTransactionID(t *testing.T) {
	var ev = &exchange.Event{OrderID: "1X2"}
	if ev.TransactionID() != "1X2" {
		t.Fatal("expected order id fallback")
	}
	ev.ID = "uuid"
	if ev.TransactionID() != "uuid" {
		t.Fatal("expected id")
	}
}

func TestParseJSONEdgeCases(t *testing.T) {
	if _, err := exchange.ParseJSON([]byte("   \n\t ")); !errors.Is(err, exchange.ErrEmpty) {
		t.Fatalf("expected ErrEmpty for whitespace, got %v", err)
	}
	if _, err := exchange.ParseJSON([]byte("{invalid")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, err := exchange.ParseJSON([]byte(`{"type":"order"}`)); !errors.Is(err, exchange.ErrEmpty) {
		t.Fatalf("expected ErrEmpty when id and orderId are absent, got %v", err)
	}

	// Legacy-shaped JSON payload (action + order_id)
	var legacyJSON = []byte(`{"action":"new_ppt","order_id":"51007X1234","amount":"12.50","extra1":"ORD-1"}`)
	var ev, err = exchange.ParseJSON(legacyJSON)
	if err != nil {
		t.Fatalf("unexpected error parsing legacy JSON: %v", err)
	}
	if ev.OrderID != "51007X1234" || ev.Status.Action != client.ActionPaid || ev.Amount != 1250 || ev.Extra1 != "ORD-1" {
		t.Fatalf("unexpected legacy JSON event: %+v", ev)
	}

	// object.id fallback + object.stats extra1..3
	var objFallback = []byte(`{
		"type":"order",
		"object":{
			"id":"OBJ-1",
			"orderId":"51007X999",
			"status":{"code":100,"action":"PAID"},
			"amount":{"value":2500,"currency":"EUR"},
			"stats":{"extra1":"A","extra2":"B","extra3":"C"}
		}
	}`)
	ev, err = exchange.ParseJSON(objFallback)
	if err != nil {
		t.Fatalf("unexpected error parsing object fallback JSON: %v", err)
	}
	if ev.ID != "OBJ-1" || ev.TransactionID() != "OBJ-1" || ev.Extra1 != "A" || ev.Extra2 != "B" || ev.Extra3 != "C" {
		t.Fatalf("unexpected object fallback event: %+v", ev)
	}
}

func TestParseLegacyEdgeCases(t *testing.T) {
	var body = []byte("status_code=85&extra2=two")
	var req = httptest.NewRequest(http.MethodPost, "/exchange?action=verify&orderId=99X11&amount=12%2C50", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var ev, err = exchange.ParseRequest(req, body)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}
	if ev.OrderID != "99X11" || ev.Amount != 1250 || ev.Status.Code != 85 || ev.Status.Action != client.ActionVerify || ev.Extra2 != "two" {
		t.Fatalf("unexpected parsed legacy event: %+v", ev)
	}

	var negReq = httptest.NewRequest(http.MethodGet, "/exchange?action=refund:send&order_id=88X22&amount=-5.5", nil)
	ev, err = exchange.ParseRequest(negReq, nil)
	if err != nil {
		t.Fatalf("ParseRequest negative amount failed: %v", err)
	}
	if ev.Amount != -550 || !ev.IsRefund() || ev.Topic() != exchange.TopicRefund {
		t.Fatalf("unexpected negative refund event: %+v", ev)
	}
}

func TestEventTopics(t *testing.T) {
	var cases = []struct {
		ev   exchange.Event
		want exchange.Topic
	}{
		{exchange.Event{Status: client.Status{Action: client.ActionVerify}}, exchange.TopicVerify},
		{exchange.Event{Status: client.Status{Action: client.ActionAuthorize}}, exchange.TopicAuthorized},
		{exchange.Event{Status: client.Status{Action: client.ActionChargeback}}, exchange.TopicChargeback},
		{exchange.Event{Status: client.Status{Action: client.ActionInit}}, exchange.TopicPending},
		{exchange.Event{Status: client.Status{Action: client.Action("UNKNOWN")}}, exchange.TopicUnknown},
	}
	for _, tc := range cases {
		if got := tc.ev.Topic(); got != tc.want {
			t.Errorf("Event(%+v).Topic() = %q, want %q", tc.ev, got, tc.want)
		}
	}
}

func TestVerifierAlgorithmsAndErrors(t *testing.T) {
	var payload = []byte(`{"id":"1"}`)
	var sig512 = exchange.Sign(sha512.New, "secret-key", payload)

	var v = &exchange.Verifier{Secrets: exchange.StaticSecrets(map[string]string{"k1": "secret-key"})}
	var h = make(http.Header)
	h.Set(exchange.HeaderSignatureMethod, "hmac")
	h.Set(exchange.HeaderSignatureKeyID, "k1")
	h.Set(exchange.HeaderSignatureAlgorithm, "sha512")
	h.Set(exchange.HeaderSignature, sig512)
	if keyID, err := v.Verify(h, payload); err != nil || keyID != "k1" {
		t.Fatalf("expected sha512 signature to verify for k1, got keyID=%q err=%v", keyID, err)
	}

	h.Set(exchange.HeaderSignatureMethod, "rsa")
	if _, err := v.Verify(h, payload); err == nil || !strings.Contains(err.Error(), "unsupported signature method") {
		t.Fatalf("expected unsupported signature method error, got: %v", err)
	}

	h.Set(exchange.HeaderSignatureMethod, "hmac")
	h.Set(exchange.HeaderSignatureAlgorithm, "md5")
	if _, err := v.Verify(h, payload); err == nil || !strings.Contains(err.Error(), "unsupported signature algorithm") {
		t.Fatalf("expected unsupported signature algorithm error, got: %v", err)
	}

	var noSecrets = &exchange.Verifier{}
	h.Set(exchange.HeaderSignatureAlgorithm, "sha256")
	if _, err := noSecrets.Verify(h, payload); !errors.Is(err, exchange.ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey with nil Secrets, got: %v", err)
	}
}

func TestHandlerErrorsMaxBodyAndRouterOnAny(t *testing.T) {
	var anyCount int
	var router = exchange.NewRouter()
	router.OnAny(exchange.EventHandlerFunc(func(ctx context.Context, ev *exchange.Event) error {
		anyCount++
		return errors.New("any-err")
	}))
	router.OnFunc(exchange.TopicPaid, func(ctx context.Context, ev *exchange.Event) error {
		return errors.New("paid-err")
	})

	var handlerErr error
	var h = &exchange.Handler{
		MaxBodyBytes: 16,
		Events:       router,
		OnError: func(r *http.Request, ev *exchange.Event, err error) {
			handlerErr = err
		},
	}

	// Exceeding MaxBodyBytes returns 400 FALSE|
	var bigReq = httptest.NewRequest(http.MethodPost, "/exchange", strings.NewReader(strings.Repeat("a", 64)))
	var bigRec = httptest.NewRecorder()
	h.ServeHTTP(bigRec, bigReq)
	if bigRec.Code != http.StatusBadRequest || !strings.HasPrefix(bigRec.Body.String(), "FALSE|") {
		t.Fatalf("expected 400 FALSE| on oversized body, got %d %q", bigRec.Code, bigRec.Body.String())
	}
	if handlerErr == nil {
		t.Fatal("expected OnError callback for oversized body")
	}

	// Router combines OnFunc(TopicPaid) + OnAny errors and triggers OnError with 500
	handlerErr = nil
	h.MaxBodyBytes = 1024
	var paidReq = httptest.NewRequest(http.MethodGet, "/exchange?action=new_ppt&order_id=1X2", nil)
	var paidRec = httptest.NewRecorder()
	h.ServeHTTP(paidRec, paidReq)
	if paidRec.Code != http.StatusInternalServerError || anyCount != 1 {
		t.Fatalf("expected 500 and anyCount=1, got code=%d anyCount=%d", paidRec.Code, anyCount)
	}
	if handlerErr == nil || !strings.Contains(handlerErr.Error(), "paid-err") || !strings.Contains(handlerErr.Error(), "any-err") {
		t.Fatalf("expected joined errors in OnError, got %v", handlerErr)
	}
}
