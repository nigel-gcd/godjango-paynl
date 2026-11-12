package paynl_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nigel2392/cache"
	"github.com/Nigel2392/go-django/contrib/admin"
	"github.com/Nigel2392/go-django/contrib/auth"
	"github.com/Nigel2392/go-django/contrib/blocks"
	"github.com/Nigel2392/go-django/contrib/editor"
	"github.com/Nigel2392/go-django/contrib/messages"
	"github.com/Nigel2392/go-django/contrib/paynl"
	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
	"github.com/Nigel2392/go-django/contrib/reports"
	auditlogs "github.com/Nigel2392/go-django/contrib/reports/audit_logs"
	"github.com/Nigel2392/go-django/contrib/session"
	"github.com/Nigel2392/go-django/contrib/shop"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
	shopSignals "github.com/Nigel2392/go-django/contrib/shop/util/signals"
	queries "github.com/Nigel2392/go-django/queries/src"
	"github.com/Nigel2392/go-django/queries/src/drivers"
	"github.com/Nigel2392/go-django/queries/src/migrator"
	django "github.com/Nigel2392/go-django/src"
	"github.com/Nigel2392/go-django/src/core/attrs"
	"github.com/Nigel2392/go-django/src/core/filesystem/mediafiles"
	mediafs "github.com/Nigel2392/go-django/src/core/filesystem/mediafiles/fs"
	"github.com/Nigel2392/go-signals"
	"github.com/Nigel2392/mux"
	"github.com/Nigel2392/mux/middleware/sessions"
	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shopspring/decimal"
)

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

func newTestProvider(t *testing.T, handler http.HandlerFunc) *paynl.Provider {
	t.Helper()
	var c, err = client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "s3cret",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		ConnectURL: "https://connect.pay.test",
		RestURL:    "https://rest.pay.test",
		HTTPClient: &http.Client{Transport: handlerTransport{handler}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return paynl.NewProvider(paynl.ProviderOptions{
		Client:      c,
		ReturnURL:   "https://shop.test/return",
		ExchangeURL: "https://shop.test/paynl/exchange",
	})
}

func TestProviderCreateRefundCancelCapture(t *testing.T) {
	var lastPath, lastMethod string
	var lastBody map[string]any

	var prov = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastMethod = r.URL.Path, r.Method
		var b, _ = io.ReadAll(r.Body)
		lastBody = nil
		if len(b) > 0 {
			_ = json.Unmarshal(b, &lastBody)
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orders":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{
				"id": "uuid-order-1",
				"orderId": "51007856048X14b0",
				"reference": "01J00000000000000000000000",
				"status": {"code": 20, "action": "PENDING"},
				"amount": {"value": 2500, "currency": "EUR"},
				"links": {"checkout": "https://checkout.pay.nl/to/payment/uuid-order-1"}
			}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/refund"):
			io.WriteString(w, `{"refundId":"RF-1234","description":"Partial refund"}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/abort"):
			io.WriteString(w, `{"id":"uuid-order-1","status":{"code":-90,"action":"CANCEL"},"amount":{"value":2500,"currency":"EUR"}}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/capture"):
			io.WriteString(w, `{"id":"uuid-order-1","status":{"code":100,"action":"PAID"},"amount":{"value":2500,"currency":"EUR"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	var startedCount, refundedCount, cancelledCount int
	var sigs = paynl.NewSignals()
	_, _ = sigs.Started.Listen(context.Background(), func(ctx context.Context, s signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		startedCount++
		return nil
	})
	_, _ = sigs.Refunded.Listen(context.Background(), func(ctx context.Context, s signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		refundedCount++
		return nil
	})
	_, _ = sigs.Cancelled.Listen(context.Background(), func(ctx context.Context, s signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		cancelledCount++
		return nil
	})

	var order = &models.Order{
		ID: drivers.NewULID(),
		Payment: &models.Payment{
			Amount: decimal.NewFromFloat(25.00),
		},
	}

	var ctx = context.Background()
	payment, err := prov.CreateTransaction(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if payment.TransactionID != "uuid-order-1" || payment.State != paystate.STATUS_PENDING {
		t.Fatalf("unexpected payment: %+v", payment)
	}
	if got := prov.CheckoutURL(ctx, payment.TransactionID); got != "https://checkout.pay.nl/to/payment/uuid-order-1" {
		t.Fatalf("unexpected checkout url: %q", got)
	}
	if !cache.Has(ctx, "paynl:checkout_url:uuid-order-1") {
		t.Fatal("expected checkout url to be stored in cache when no session exists in ctx")
	}
	if startedCount != 1 {
		t.Fatalf("expected Started signal, got %d", startedCount)
	}

	// Capture
	if _, err := prov.Capture(context.Background(), payment, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	if payment.State != paystate.STATUS_PAID || lastPath != "/v1/orders/uuid-order-1/capture" {
		t.Fatalf("expected PAID after capture, got %s (%s)", payment.State, lastPath)
	}

	// Refund
	refRes, err := prov.Refund(context.Background(), payment, decimal.NewFromFloat(10.00), "Partial refund")
	if err != nil {
		t.Fatal(err)
	}
	if refRes.RefundID != "RF-1234" || payment.State != paystate.STATUS_REFUNDING || refundedCount != 1 {
		t.Fatalf("unexpected refund result: %+v, state=%s, count=%d", refRes, payment.State, refundedCount)
	}
	if lastBody["amount"].(float64) != 1000 {
		t.Fatalf("expected 1000 cents refund, got %v", lastBody["amount"])
	}

	// Cancel
	if err := prov.Cancel(ctx, payment); err != nil {
		t.Fatal(err)
	}
	if payment.State != paystate.STATUS_CANCEL || cancelledCount != 1 || lastMethod != http.MethodPatch {
		t.Fatalf("expected STATUS_CANCEL after cancel, got %s", payment.State)
	}
	if got := prov.CheckoutURL(ctx, payment.TransactionID); got != "" {
		t.Fatalf("expected checkout url to be deleted after cancel, got %q", got)
	}
}

func TestCheckoutURLSessionStorage(t *testing.T) {
	var prov = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{
			"id": "uuid-session-1",
			"orderId": "5100X777",
			"reference": "ORD-SESS",
			"status": {"code": 20, "action": "PENDING"},
			"amount": {"value": 1250, "currency": "EUR"},
			"links": {"checkout": "https://checkout.pay.nl/to/payment/uuid-session-1"}
		}`)
	})

	var mgr = scs.New()
	mgr.Store = memstore.New()

	var m = mux.New()
	m.Use(sessions.SessionMiddleware(mgr))
	m.Post("/checkout", mux.NewHandler(func(w http.ResponseWriter, r *http.Request) {
		var order = &models.Order{
			ID:      drivers.NewULID(),
			Payment: &models.Payment{Amount: decimal.NewFromFloat(12.50)},
		}
		p, err := prov.CreateTransaction(r.Context(), order)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		io.WriteString(w, p.TransactionID)
	}))
	m.Get("/checkout-url", mux.NewHandler(func(w http.ResponseWriter, r *http.Request) {
		var u = prov.CheckoutURL(r.Context(), r.URL.Query().Get("tx"))
		io.WriteString(w, u)
	}))

	// 1. Create transaction inside a session-backed request
	var req = httptest.NewRequest(http.MethodPost, "/checkout", nil)
	var w = httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "uuid-session-1" {
		t.Fatalf("unexpected checkout response: %d %q", w.Code, w.Body.String())
	}

	// Should live in session, NOT in cache
	if cache.Has(context.Background(), "paynl:checkout_url:uuid-session-1") {
		t.Fatal("expected session-stored checkout url not to pollute global cache")
	}

	var cookies = w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie to be set")
	}

	// 2. Subsequent request carrying the same session cookie resolves CheckoutURL from session
	req = httptest.NewRequest(http.MethodGet, "/checkout-url?tx=uuid-session-1", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w = httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "https://checkout.pay.nl/to/payment/uuid-session-1" {
		t.Fatalf("unexpected checkout-url response: %d %q", w.Code, w.Body.String())
	}

	// 3. Request without session cookie does not see the checkout URL
	req = httptest.NewRequest(http.MethodGet, "/checkout-url?tx=uuid-session-1", nil)
	w = httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Body.String() != "" {
		t.Fatalf("expected empty checkout url for anonymous request, got %q", w.Body.String())
	}
}

func TestProviderParseWebhook(t *testing.T) {
	var prov = paynl.NewProvider(paynl.ProviderOptions{})

	var rawJSON = []byte(`{
		"event": "status_changed",
		"type": "order",
		"id": "uuid-123",
		"object": {
			"id": "uuid-123",
			"orderId": "5100X123",
			"reference": "ORD-42",
			"status": {"code": 100, "action": "PAID"},
			"amount": {"value": 1995, "currency": "EUR"}
		}
	}`)

	sig, err := prov.ParseWebhook(context.Background(), rawJSON)
	if err != nil {
		t.Fatal(err)
	}
	if sig.ProviderName != "paynl" || sig.TransactionID != "uuid-123" || sig.OrderID != "ORD-42" || sig.Status != paystate.STATUS_PAID || sig.Amount != 1995 {
		t.Fatalf("unexpected signal payload: %+v", sig)
	}

	var rawLegacy = []byte("action=refund:received&order_id=5100X123&extra1=ORD-42&amount=1995")
	sig, err = prov.ParseWebhook(context.Background(), rawLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Status != paystate.STATUS_REFUND || sig.OrderID != "ORD-42" || sig.TransactionID != "5100X123" {
		t.Fatalf("unexpected legacy refund payload: %+v", sig)
	}
}

func TestDjangoAppExchangeWorkflow(t *testing.T) {
	paynl.PAYNL = nil
	var appCfg, err = paynl.NewAppConfig()
	if err != nil {
		t.Fatal(err)
	}

	var settings = django.Config(map[string]any{
		paynl.APPVAR_USERNAME:         "AT-1234-5678",
		paynl.APPVAR_PASSWORD:         "topsecret",
		paynl.APPVAR_SERVICE_ID:       "SL-1111-2222",
		paynl.APPVAR_TEST_MODE:        true,
		paynl.APPVAR_VERIFY_SIGNATURE: false,
		paynl.APPVAR_SYNC_PAYMENTS:    false,
	})
	if err := paynl.PAYNL.Init(settings); err != nil {
		t.Fatal(err)
	}
	_ = appCfg

	var paidEvents, refundEvents int
	_, _ = paynl.PAYNL.SIGNALS.Paid.Listen(context.Background(), func(ctx context.Context, s signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		paidEvents++
		return nil
	})
	_, _ = paynl.PAYNL.SIGNALS.Refunded.Listen(context.Background(), func(ctx context.Context, s signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		refundEvents++
		return nil
	})

	// 1. Signed JSON PAID exchange
	var body = `{"event":"status_changed","type":"order","id":"uuid-1","object":{"id":"uuid-1","orderId":"1X2","reference":"ORD-1","status":{"code":100,"action":"PAID"},"amount":{"value":1500,"currency":"EUR"}}}`
	var req = httptest.NewRequest(http.MethodPost, "/paynl/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, "topsecret", []byte(body)))
	req.Header.Set(exchange.HeaderSignatureKeyID, "SL-1111-2222")

	var w = httptest.NewRecorder()
	paynl.PAYNL.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("expected 200 TRUE|, got %d %q", w.Code, w.Body.String())
	}
	if paidEvents != 1 {
		t.Fatalf("expected 1 paid signal, got %d", paidEvents)
	}

	// 2. Legacy refund callback
	req = httptest.NewRequest(http.MethodGet, "/paynl/exchange?action=refund:received&order_id=1X2&extra1=ORD-1&amount=1500", nil)
	w = httptest.NewRecorder()
	paynl.PAYNL.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("expected 200 TRUE|, got %d %q", w.Code, w.Body.String())
	}
	if refundEvents != 1 {
		t.Fatalf("expected 1 refund signal, got %d", refundEvents)
	}
}

func setupShopApp(t *testing.T, ctx context.Context, paynlCfg map[string]any) *django.Application {
	t.Helper()
	var tmpDir = t.TempDir()
	var mediaBackend = mediafs.NewBackend(tmpDir+"/media", 0)
	mediafiles.RegisterBackend("filesystem", mediaBackend)
	mediafiles.SetDefault("filesystem")

	db, err := drivers.Open(ctx, "sqlite3", tmpDir+"/db.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	django.Global = nil
	paynl.PAYNL = nil

	var settings = map[string]any{
		django.APPVAR_ALLOWED_HOSTS:   []string{"*"},
		django.APPVAR_DEBUG:           false,
		django.APPVAR_HOST:            "127.0.0.1",
		django.APPVAR_PORT:            "8080",
		django.APPVAR_DATABASE:        db,
		django.APPVAR_RECOVERER:       false,
		auth.APPVAR_AUTH_EMAIL_LOGIN:  true,
		migrator.APPVAR_MIGRATION_DIR: tmpDir + "/migrations",
		paynl.APPVAR_TEST_MODE:        true,
		paynl.APPVAR_VERIFY_SIGNATURE: true,
		paynl.APPVAR_SYNC_PAYMENTS:    true,
	}
	for k, v := range paynlCfg {
		settings[k] = v
	}

	var app = django.App(
		django.Configure(settings),
		django.Apps(
			session.NewAppConfig,
			messages.NewAppConfig,
			auth.NewAppConfig,
			admin.NewAppConfig,
			auditlogs.NewAppConfig,
			reports.NewAppConfig,
			migrator.NewAppConfig,
			editor.NewAppConfig,
			blocks.NewAppConfig,
			func() (django.AppConfig, error) {
				cfg, err := shop.NewAppConfig()
				if err != nil {
					return cfg, err
				}

				var origReady = shop.SHOP.Ready
				t.Cleanup(func() { shop.SHOP.Ready = origReady })
				shop.SHOP.Ready = func() error {
					schemaEditor, err := migrator.GetSchemaEditor(db.Driver())
					if err != nil {
						return err
					}
					for _, model := range shop.SHOP.ModelObjects {
						var tbl = migrator.NewModelTable(model)
						if err := schemaEditor.CreateTable(ctx, tbl, true); err != nil {
							return err
						}
					}
					if origReady != nil {
						return origReady()
					}
					return nil
				}

				return cfg, nil
			},
			paynl.NewAppConfig,
		),
		django.Flag(
			django.FlagSkipCmds,
			django.FlagSkipChecks,
		),
	)
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	return app
}

func createTestOrderAndPayment(t *testing.T, ctx context.Context, amount float64) (*models.Order, *models.Payment) {
	t.Helper()
	stateObj, err := queries.GetQuerySetWithContext(ctx, &models.OrderState{}).Create(&models.OrderState{
		Label:      "New",
		Action:     "order_workflow",
		WorkflowID: drivers.NewULID(),
	})
	if err != nil {
		t.Fatalf("create order state: %v", err)
	}

	paymentObj, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).Create(&models.Payment{
		Amount:    decimal.NewFromFloat(amount),
		State:     paystate.STATUS_INIT,
		CreatedAt: drivers.CurrentTimestamp(),
		UpdatedAt: drivers.CurrentTimestamp(),
	})
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	orderObj, err := queries.GetQuerySetWithContext(ctx, &models.Order{}).Create(&models.Order{
		State:   stateObj,
		Payment: paymentObj,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	orderObj.Payment = paymentObj
	return orderObj, paymentObj
}

func TestE2EShopDatabaseCheckoutAndRefundWorkflow(t *testing.T) {
	var ctx = context.Background()
	var app = setupShopApp(t, ctx, map[string]any{
		paynl.APPVAR_USERNAME:     "AT-1234-5678",
		paynl.APPVAR_PASSWORD:     "e2esecret",
		paynl.APPVAR_SERVICE_ID:   "SL-1111-2222",
		paynl.APPVAR_RETURN_URL:   "https://shop.test/return",
		paynl.APPVAR_EXCHANGE_URL: "https://shop.test/paynl/exchange",
	})

	t.Run("CheckoutAndRefund", func(t *testing.T) {
		runE2ECheckoutAndRefund(t, ctx, app)
	})
	t.Run("AuthCaptureAndCancel", func(t *testing.T) {
		runE2EAuthCaptureAndCancel(t, ctx, app)
	})
}

func runE2ECheckoutAndRefund(t *testing.T, ctx context.Context, app *django.Application) {

	// Wire fake Pay.nl API server into PAYNL.Provider
	var fakePaynl = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orders":
			var reqBody map[string]any
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        "e2e-uuid-99",
				"orderId":   "5100X999",
				"reference": reqBody["reference"],
				"status":    map[string]any{"code": 20, "action": "PENDING"},
				"amount":    reqBody["amount"],
				"links":     map[string]any{"checkout": "https://checkout.pay.nl/to/payment/e2e-uuid-99"},
			})
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/refund"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"refundId":    "RF-999",
				"description": "Customer returned item",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c, err := client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "e2esecret",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		ConnectURL: "https://connect.pay.test",
		RestURL:    "https://rest.pay.test",
		HTTPClient: &http.Client{Transport: handlerTransport{fakePaynl}},
	})
	if err != nil {
		t.Fatal(err)
	}
	paynl.PAYNL.Provider.SetClient(c)

	// 1. Create Order & Payment in DB and initiate Pay.nl transaction
	orderObj, paymentObj := createTestOrderAndPayment(t, ctx, 49.95)

	paymentObj, err = paynl.PAYNL.Provider.CreateTransaction(ctx, orderObj)
	if err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	if _, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
		Filter("ID", paymentObj.ID).
		ExplicitSave().
		Update(paymentObj); err != nil {
		t.Fatalf("save pending payment: %v", err)
	}
	if paymentObj.State != paystate.STATUS_PENDING {
		t.Fatalf("expected STATUS_PENDING, got %s", paymentObj.State)
	}

	// 2. Receive Signed JSON exchange webhook through go-django's Mux router -> marks DB payment as STATUS_PAID
	var paidWebhook = map[string]any{
		"event":   "status_changed",
		"type":    "order",
		"version": 1,
		"id":      "e2e-uuid-99",
		"object": map[string]any{
			"id":        "e2e-uuid-99",
			"orderId":   "5100X999",
			"reference": orderObj.ID.String(),
			"status":    map[string]any{"code": 100, "action": "PAID"},
			"amount":    map[string]any{"value": 4995, "currency": "EUR"},
		},
	}
	rawWebhook, _ := json.Marshal(paidWebhook)

	var req = httptest.NewRequest(http.MethodPost, "/paynl/exchange", strings.NewReader(string(rawWebhook)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, "e2esecret", rawWebhook))
	req.Header.Set(exchange.HeaderSignatureKeyID, "SL-1111-2222")

	var w = httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("exchange webhook failed: %d %q", w.Code, w.Body.String())
	}

	// Verify DB row updated to STATUS_PAID
	dbPaymentRow, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).Filter("ID", paymentObj.ID).First()
	if err != nil {
		t.Fatalf("reload payment from DB: %v", err)
	}
	if dbPaymentRow.Object.State != paystate.STATUS_PAID {
		t.Fatalf("expected DB payment state STATUS_PAID, got %s", dbPaymentRow.Object.State)
	}
	// models.Payment.FieldDefs in contrib/shop does not map TransactionID yet,
	// so carry forward the Pay.nl transaction ID from CreateTransaction for Refund.
	if dbPaymentRow.Object.TransactionID == "" {
		dbPaymentRow.Object.TransactionID = paymentObj.TransactionID
	}

	// 3. Initiate Refund via Provider & receive signed Refund exchange callback -> updates DB and fires shop.SHOP.SIGNALS.Payment.Refunded
	var shopRefundSignalFired bool
	_, _ = shop.SHOP.SIGNALS.Payment.Refunded.Listen(ctx, func(ctx context.Context, s signals.Signal[*shopSignals.PaymentSignalData], psd *shopSignals.PaymentSignalData) error {
		shopRefundSignalFired = true
		return nil
	})

	if _, err := paynl.PAYNL.Provider.Refund(ctx, dbPaymentRow.Object, decimal.NewFromFloat(49.95), "Customer returned item"); err != nil {
		t.Fatalf("Refund: %v", err)
	}

	var refundWebhook = map[string]any{
		"event":   "status_changed",
		"type":    "order",
		"version": 1,
		"id":      "e2e-uuid-99",
		"object": map[string]any{
			"id":        "e2e-uuid-99",
			"orderId":   "5100X999",
			"reference": orderObj.ID.String(),
			"status":    map[string]any{"code": -81, "action": "REFUND"},
			"amount":    map[string]any{"value": 4995, "currency": "EUR"},
		},
	}
	rawRefund, _ := json.Marshal(refundWebhook)
	req = httptest.NewRequest(http.MethodPost, "/paynl/exchange", strings.NewReader(string(rawRefund)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, "e2esecret", rawRefund))
	req.Header.Set(exchange.HeaderSignatureKeyID, "SL-1111-2222")

	w = httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("refund webhook failed: %d %q", w.Code, w.Body.String())
	}

	dbPaymentRow, err = queries.GetQuerySetWithContext(ctx, &models.Payment{}).Filter("ID", paymentObj.ID).First()
	if err != nil {
		t.Fatalf("reload payment after refund: %v", err)
	}
	if dbPaymentRow.Object.State != paystate.STATUS_REFUND {
		t.Fatalf("expected DB payment state STATUS_REFUND, got %s", dbPaymentRow.Object.State)
	}
	if !shopRefundSignalFired {
		t.Fatal("expected shop.SHOP.SIGNALS.Payment.Refunded to fire")
	}
}

func TestPayStateFromStatus(t *testing.T) {
	var cases = []struct {
		name   string
		status client.Status
		want   paystate.PayState
	}{
		{"init", client.Status{Action: client.ActionInit}, paystate.STATUS_INIT},
		{"pending", client.Status{Action: client.ActionPending}, paystate.STATUS_PENDING},
		{"cancel", client.Status{Action: client.ActionCancel}, paystate.STATUS_CANCEL},
		{"expired", client.Status{Action: client.ActionExpired}, paystate.STATUS_EXPIRED},
		{"denied", client.Status{Action: client.ActionDenied}, paystate.STATUS_DENIED},
		{"failure", client.Status{Action: client.ActionFailure}, paystate.STATUS_FAILURE},
		{"paid_checkamount", client.Status{Action: client.ActionPaidCheckAmount}, paystate.STATUS_PAID_CHECKAMOUNT},
		{"partial_payment", client.Status{Action: client.ActionPartialPayment}, paystate.STATUS_PARTIAL_PAYMENT},
		{"verify", client.Status{Action: client.ActionVerify}, paystate.STATUS_VERIFY},
		{"authorize", client.Status{Action: client.ActionAuthorize}, paystate.STATUS_AUTHORIZE},
		{"partly_captured", client.Status{Action: client.ActionPartlyCaptured}, paystate.STATUS_PARTLY_CAPTURED},
		{"paid", client.Status{Action: client.ActionPaid}, paystate.STATUS_PAID},
		{"chargeback", client.Status{Action: client.ActionChargeback}, paystate.STATUS_CHARGEBACK},
		{"refunding", client.Status{Action: client.ActionRefunding}, paystate.STATUS_REFUNDING},
		{"refund", client.Status{Action: client.ActionRefund}, paystate.STATUS_REFUND},
		{"partial_refund", client.Status{Action: client.ActionPartialRefund}, paystate.STATUS_PARTIAL_REFUND},
		{"code_lookup", client.Status{Code: 100}, paystate.STATUS_PAID},
		{"unknown_fallback", client.Status{Code: 9999, Action: client.Action("UNKNOWN")}, paystate.STATUS_PENDING},
	}

	for _, tc := range cases {
		if got := paynl.PayStateFromStatus(tc.status); got != tc.want {
			t.Errorf("%s: PayStateFromStatus(%+v) = %q, want %q", tc.name, tc.status, got, tc.want)
		}
	}
}

func TestSignalsEmitEvent(t *testing.T) {
	var ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var sigs = paynl.NewSignals()
	var updated, paid, authorized, verify, cancelled, refunded, chargeback int

	var listen = func(sig signals.Signal[*paynl.SignalPayload], counter *int) {
		_, _ = sig.Listen(ctx, func(_ context.Context, _ signals.Signal[*paynl.SignalPayload], _ *paynl.SignalPayload) error {
			*counter++
			return nil
		})
	}

	listen(sigs.Updated, &updated)
	listen(sigs.Paid, &paid)
	listen(sigs.Authorized, &authorized)
	listen(sigs.Verify, &verify)
	listen(sigs.Cancelled, &cancelled)
	listen(sigs.Refunded, &refunded)
	listen(sigs.Chargeback, &chargeback)

	var prov = paynl.NewProvider(paynl.ProviderOptions{})
	var actions = []client.Action{
		client.ActionPaid,
		client.ActionAuthorize,
		client.ActionVerify,
		client.ActionCancel,
		client.ActionRefund,
		client.ActionChargeback,
	}
	for _, a := range actions {
		if err := sigs.EmitEvent(ctx, prov.Base(), &exchange.Event{Status: client.Status{Action: a}}, nil, nil); err != nil {
			t.Fatalf("EmitEvent(%s): %v", a, err)
		}
	}

	if updated != len(actions) || paid != 1 || authorized != 1 || verify != 1 || cancelled != 1 || refunded != 1 || chargeback != 1 {
		t.Fatalf("unexpected signal counts: updated=%d paid=%d auth=%d verify=%d cancel=%d refund=%d cb=%d",
			updated, paid, authorized, verify, cancelled, refunded, chargeback)
	}
}

func TestProviderOrderItemsVoidSyncAndErrors(t *testing.T) {
	var capturedCreate map[string]any
	var voidCalls, statusCalls int

	var prov = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orders":
			var body, _ = io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &capturedCreate)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.Order{
				ID:      "tx-items-1",
				OrderID: "5100X777",
				Status:  client.Status{Code: 90, Action: client.ActionPending},
				Amount:  client.Amount{Value: 3000, Currency: "EUR"},
				Links:   client.Links{Redirect: "https://pay.nl/redirect/items"},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/orders/tx-items-1/void":
			voidCalls++
			_ = json.NewEncoder(w).Encode(client.Order{
				ID:     "tx-items-1",
				Status: client.Status{Code: -90, Action: client.ActionCancel},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/orders/tx-items-1/status":
			statusCalls++
			_ = json.NewEncoder(w).Encode(client.Order{
				ID:     "tx-items-1",
				Status: client.Status{Code: 100, Action: client.ActionPaid},
				Amount: client.Amount{Value: 3000, Currency: "EUR"},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	var ctx = context.Background()
	if prov.ProviderID() != "paynl" || prov.TextLabel(ctx) == "" || prov.TextHelp(ctx) == "" || prov.Base().ProviderID() != "paynl" || prov.Client() == nil {
		t.Fatal("expected provider metadata and accessors to be non-empty")
	}
	_ = prov.WidgetIcon()

	// Validation error paths on unconfigured/nil inputs
	var emptyProv = paynl.NewProvider(paynl.ProviderOptions{})
	if _, err := emptyProv.CreateTransaction(ctx, &models.Order{}); err == nil {
		t.Fatal("expected error with nil client")
	}
	if _, err := prov.CreateTransaction(ctx, nil); err == nil {
		t.Fatal("expected error with nil order")
	}
	if _, err := prov.Void(ctx, nil); err == nil {
		t.Fatal("expected error on Void(nil)")
	}
	if _, err := prov.SyncStatus(ctx, nil); err == nil {
		t.Fatal("expected error on SyncStatus(nil)")
	}
	if err := prov.Cancel(ctx, &models.Payment{}); err == nil {
		t.Fatal("expected error on Cancel with empty TransactionID")
	}
	if _, err := prov.Refund(ctx, &models.Payment{}, decimal.NewFromInt(1), ""); err == nil {
		t.Fatal("expected error on Refund with empty TransactionID")
	}
	if _, err := prov.Capture(ctx, &models.Payment{}, decimal.NewFromInt(1)); err == nil {
		t.Fatal("expected error on Capture with empty TransactionID")
	}

	// Order with zero Order.Price/Payment.Amount and OrderItems populated -> calculates total & order products
	var orderObj = &models.Order{
		ID:         drivers.NewULID(),
		Payment:    &models.Payment{ID: drivers.NewULID()},
		OrderItems: &queries.RelRevFK[*models.OrderItem]{},
	}
	var item1 = &models.OrderItem{
		ID:        drivers.NewULID(),
		Order:     orderObj,
		Product:   &models.ProductSku{ID: drivers.NewULID(), Title: "Widget", Price: decimal.NewFromFloat(10.00)},
		ItemPrice: decimal.NewFromFloat(10.00),
		Amount:    2,
	}
	var item2 = &models.OrderItem{
		ID:        drivers.NewULID(),
		Order:     orderObj,
		Product:   &models.ProductSku{ID: drivers.NewULID(), Title: "Cable", Price: decimal.NewFromFloat(10.00)},
		ItemPrice: decimal.NewFromFloat(10.00),
		Amount:    1,
	}
	orderObj.OrderItems.SetValues([]attrs.Definer{item1, item2})

	var p, err = prov.CreateTransaction(ctx, orderObj)
	if err != nil {
		t.Fatalf("CreateTransaction with items: %v", err)
	}
	if !p.Amount.Equal(decimal.NewFromFloat(30.00)) {
		t.Fatalf("expected Amount=30.00, got %s", p.Amount)
	}
	if amtMap, ok := capturedCreate["amount"].(map[string]any); !ok || int64(amtMap["value"].(float64)) != 3000 {
		t.Fatalf("expected amount.value=3000 from OrderItems, got %+v", capturedCreate["amount"])
	}
	var ordMap, ok = capturedCreate["order"].(map[string]any)
	if !ok {
		t.Fatalf("expected order block with products, got %+v", capturedCreate)
	}
	if prods, ok := ordMap["products"].([]any); !ok || len(prods) != 2 {
		t.Fatalf("expected 2 products in create payload, got %+v", ordMap["products"])
	}

	// CheckoutURL redirect fallback cached for tx-items-1
	if gotURL := prov.CheckoutURL(ctx, "tx-items-1"); gotURL != "https://pay.nl/redirect/items" {
		t.Fatalf("expected redirect checkout URL, got %q", gotURL)
	}

	// SyncStatus updates state
	var ordRes *client.Order
	ordRes, err = prov.SyncStatus(ctx, p)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if statusCalls != 1 || ordRes.Status.Action != client.ActionPaid || p.State != paystate.STATUS_PAID {
		t.Fatalf("unexpected SyncStatus result: calls=%d status=%+v state=%s", statusCalls, ordRes.Status, p.State)
	}

	// Void cancels authorized/pending transaction
	ordRes, err = prov.Void(ctx, p)
	if err != nil {
		t.Fatalf("Void: %v", err)
	}
	if voidCalls != 1 || ordRes.Status.Action != client.ActionCancel || p.State != paystate.STATUS_CANCEL {
		t.Fatalf("unexpected Void result: calls=%d state=%s", voidCalls, p.State)
	}
}

func TestCheckoutURLCacheExpiryAndPrecedence(t *testing.T) {
	var c, err = client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "secret",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		ConnectURL: "https://connect.pay.test",
		RestURL:    "https://rest.pay.test",
		HTTPClient: &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.Order{
				ID:      "tx-ttl-1",
				OrderID: "5100Xttl",
				Status:  client.Status{Code: 90, Action: client.ActionPending},
				Links:   client.Links{Checkout: "https://pay.nl/checkout/cache-ttl"},
			})
		})}},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	var prov = paynl.NewProvider(paynl.ProviderOptions{
		Client:         c,
		CheckoutURLTTL: 25 * time.Millisecond,
	})

	var ctx = context.Background()
	t.Cleanup(func() { _ = cache.Delete(ctx, "paynl:checkout_url:tx-ttl-1") })

	var ord = &models.Order{
		ID:      drivers.NewULID(),
		Payment: &models.Payment{ID: drivers.NewULID(), Amount: decimal.NewFromFloat(10.00)},
	}
	if _, err := prov.CreateTransaction(ctx, ord); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	if got := prov.CheckoutURL(ctx, "tx-ttl-1"); got != "https://pay.nl/checkout/cache-ttl" {
		t.Fatalf("expected cached checkout URL immediately, got %q", got)
	}

	// Session value overrides cache when both exist
	var mgr = scs.New()
	mgr.Store = memstore.New()
	var m = mux.New()
	m.Use(sessions.SessionMiddleware(mgr))
	m.Get("/set-and-read", mux.NewHandler(func(w http.ResponseWriter, r *http.Request) {
		var sess = sessions.SessionFromContext(r.Context())
		sess.Set("paynl:checkout_url:tx-ttl-1", "https://pay.nl/checkout/from-session")
		io.WriteString(w, prov.CheckoutURL(r.Context(), "tx-ttl-1"))
	}))
	var rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/set-and-read", nil))
	if rec.Body.String() != "https://pay.nl/checkout/from-session" {
		t.Fatalf("expected session value precedence over cache, got %q", rec.Body.String())
	}

	// Wait past TTL -> cache entry expires
	time.Sleep(50 * time.Millisecond)
	if got := prov.CheckoutURL(ctx, "tx-ttl-1"); got != "" {
		t.Fatalf("expected cache entry to expire after TTL, got %q", got)
	}
}

func runE2EAuthCaptureAndCancel(t *testing.T, parentCtx context.Context, app *django.Application) {
	var ctx, cancel = context.WithCancel(parentCtx)
	t.Cleanup(cancel)

	var paynlAuthSignalFired bool
	var shopCancelSignalFired bool
	_, _ = paynl.PAYNL.SIGNALS.Authorized.Listen(ctx, func(_ context.Context, _ signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		if p != nil && p.TransactionID == "e2e-auth-1" {
			paynlAuthSignalFired = true
		}
		return nil
	})
	_, _ = shop.SHOP.SIGNALS.Payment.Cancelled.Listen(ctx, func(_ context.Context, _ signals.Signal[*shopSignals.PaymentSignalData], p *shopSignals.PaymentSignalData) error {
		if p != nil && p.Last != nil && p.Payment != nil && p.Last.State == paystate.STATUS_AUTHORIZE && p.Payment.State == paystate.STATUS_CANCEL {
			shopCancelSignalFired = true
		}
		return nil
	})

	var captureCalled bool
	var c, err = client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "e2esecret",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		ConnectURL: "https://connect.pay.test",
		RestURL:    "https://rest.pay.test",
		HTTPClient: &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/orders":
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(client.Order{
					ID:      "e2e-auth-1",
					OrderID: "5100Xauth",
					Status:  client.Status{Code: 90, Action: client.ActionPending},
					Links:   client.Links{Checkout: "https://pay.nl/checkout/e2e-auth-1"},
				})
			case r.Method == http.MethodPatch && r.URL.Path == "/v1/orders/e2e-auth-1/capture/amount":
				captureCalled = true
				_ = json.NewEncoder(w).Encode(client.Order{
					ID:     "e2e-auth-1",
					Status: client.Status{Code: 100, Action: client.ActionPaid},
				})
			default:
				t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})}},
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	paynl.PAYNL.Provider.SetClient(c)

	orderObj, paymentObj := createTestOrderAndPayment(t, ctx, 25.00)
	paymentObj, err = paynl.PAYNL.Provider.CreateTransaction(ctx, orderObj)
	if err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	if _, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
		Filter("ID", paymentObj.ID).
		ExplicitSave().
		Update(paymentObj); err != nil {
		t.Fatalf("save pending payment: %v", err)
	}

	// 1. AUTHORIZE webhook -> DB state becomes STATUS_AUTHORIZE and Pay.nl Authorized signal fires
	var authBody, _ = json.Marshal(map[string]any{
		"event":   "status_changed",
		"type":    "order",
		"version": 1,
		"id":      "e2e-auth-1",
		"object": map[string]any{
			"id":        "e2e-auth-1",
			"orderId":   "5100Xauth",
			"reference": orderObj.ID.String(),
			"status":    map[string]any{"code": 95, "action": "AUTHORIZE"},
			"amount":    map[string]any{"value": 2500, "currency": "EUR"},
		},
	})
	var req = httptest.NewRequest(http.MethodPost, "/paynl/exchange", strings.NewReader(string(authBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, "e2esecret", authBody))
	req.Header.Set(exchange.HeaderSignatureKeyID, "SL-1111-2222")
	var w = httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("authorize webhook failed: %d %q", w.Code, w.Body.String())
	}

	dbPaymentRow, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).Filter("ID", paymentObj.ID).First()
	if err != nil || dbPaymentRow.Object.State != paystate.STATUS_AUTHORIZE {
		t.Fatalf("expected DB state STATUS_AUTHORIZE, err=%v row=%+v", err, dbPaymentRow)
	}
	if !paynlAuthSignalFired {
		t.Fatal("expected paynl.PAYNL.SIGNALS.Authorized to fire")
	}

	// 2. Capture authorized payment
	dbPaymentRow.Object.TransactionID = paymentObj.TransactionID
	if _, err := paynl.PAYNL.Provider.Capture(ctx, dbPaymentRow.Object, decimal.NewFromFloat(25.00)); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !captureCalled || dbPaymentRow.Object.State != paystate.STATUS_PAID {
		t.Fatalf("expected captureCalled=true and STATUS_PAID, got capture=%v state=%s", captureCalled, dbPaymentRow.Object.State)
	}

	// 3. CANCEL webhook -> DB state becomes STATUS_CANCEL and shop Cancelled signal fires
	var cancelBody, _ = json.Marshal(map[string]any{
		"event":   "status_changed",
		"type":    "order",
		"version": 1,
		"id":      "e2e-auth-1",
		"object": map[string]any{
			"id":        "e2e-auth-1",
			"orderId":   "5100Xauth",
			"reference": orderObj.ID.String(),
			"status":    map[string]any{"code": -90, "action": "CANCEL"},
			"amount":    map[string]any{"value": 2500, "currency": "EUR"},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "/paynl/exchange", strings.NewReader(string(cancelBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.HeaderSignature, exchange.Sign(sha256.New, "e2esecret", cancelBody))
	req.Header.Set(exchange.HeaderSignatureKeyID, "SL-1111-2222")
	w = httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "TRUE|") {
		t.Fatalf("cancel webhook failed: %d %q", w.Code, w.Body.String())
	}

	dbPaymentRow, err = queries.GetQuerySetWithContext(ctx, &models.Payment{}).Filter("ID", paymentObj.ID).First()
	if err != nil || dbPaymentRow.Object.State != paystate.STATUS_CANCEL {
		t.Fatalf("expected DB state STATUS_CANCEL, err=%v row=%+v", err, dbPaymentRow)
	}
	if !shopCancelSignalFired {
		t.Fatal("expected shop.SHOP.SIGNALS.Payment.Cancelled to fire")
	}
}
