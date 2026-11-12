package paynl_test

import (
	"context"
	"os"
	"testing"

	"github.com/Nigel2392/go-django/contrib/paynl"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
	queries "github.com/Nigel2392/go-django/queries/src"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestLiveSandboxPaymentWorkflow exercises real Pay.nl test-mode endpoints
// (CreateOrder -> GetOrder status -> AbortOrder) backed by go-django and SQLite.
//
// Provide real Pay.nl credentials via environment variables to run it:
//
//	PAYNL_USERNAME=AT-xxxx-xxxx
//	PAYNL_PASSWORD=<token>
//	PAYNL_SERVICE_ID=SL-xxxx-xxxx
func TestLiveSandboxPaymentWorkflow(t *testing.T) {
	var username = os.Getenv("PAYNL_USERNAME")
	var password = os.Getenv("PAYNL_PASSWORD")
	var serviceID = os.Getenv("PAYNL_SERVICE_ID")
	if username == "" || password == "" || serviceID == "" {
		t.Skip("set PAYNL_USERNAME, PAYNL_PASSWORD, and PAYNL_SERVICE_ID to run live Pay.nl sandbox test")
	}

	var ctx = context.Background()
	var cfg = map[string]any{
		paynl.APPVAR_USERNAME:     username,
		paynl.APPVAR_PASSWORD:     password,
		paynl.APPVAR_SERVICE_ID:   serviceID,
		paynl.APPVAR_RETURN_URL:   envOrDefault("PAYNL_RETURN_URL", "https://example.com/return"),
		paynl.APPVAR_EXCHANGE_URL: envOrDefault("PAYNL_EXCHANGE_URL", "https://example.com/paynl/exchange"),
	}
	if v := os.Getenv("PAYNL_CONNECT_URL"); v != "" {
		cfg[paynl.APPVAR_CONNECT_URL] = v
	}
	if v := os.Getenv("PAYNL_REST_URL"); v != "" {
		cfg[paynl.APPVAR_REST_URL] = v
	}

	_ = setupShopApp(t, ctx, cfg)

	// 1. Create Order & Payment in SQLite
	orderObj, paymentObj := createTestOrderAndPayment(t, ctx, 1.00)

	// 2. Initiate live sandbox transaction
	paymentObj, err := paynl.PAYNL.Provider.CreateTransaction(ctx, orderObj)
	if err != nil {
		t.Fatalf("CreateTransaction (sandbox): %v", err)
	}
	if paymentObj.TransactionID == "" {
		t.Fatal("expected non-empty Pay.nl TransactionID")
	}
	var checkoutURL = paynl.PAYNL.Provider.CheckoutURL(ctx, paymentObj.TransactionID)
	if checkoutURL == "" {
		t.Fatal("expected non-empty CheckoutURL")
	}
	t.Logf("created sandbox order tx=%s state=%s checkoutURL=%s", paymentObj.TransactionID, paymentObj.State, checkoutURL)

	// 3. Check live order status
	statusOrder, err := paynl.PAYNL.Provider.SyncStatus(ctx, paymentObj)
	if err != nil {
		t.Fatalf("SyncStatus (sandbox): %v", err)
	}
	t.Logf("synced sandbox order id=%s orderId=%s status=%d (%s) paystate=%s",
		statusOrder.ID, statusOrder.OrderID, statusOrder.Status.Code, statusOrder.Status.Action, paymentObj.State)

	// 4. Abort open sandbox order and persist final cancelled state to SQLite
	if err := paynl.PAYNL.Provider.Cancel(ctx, paymentObj); err != nil {
		t.Fatalf("Cancel (sandbox): %v", err)
	}
	if paymentObj.State != paystate.STATUS_CANCEL {
		t.Fatalf("expected STATUS_CANCEL after abort, got %s", paymentObj.State)
	}

	if _, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
		Filter("ID", paymentObj.ID).
		ExplicitSave().
		Update(paymentObj); err != nil {
		t.Fatalf("update payment in DB: %v", err)
	}

	dbPaymentRow, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).Filter("ID", paymentObj.ID).First()
	if err != nil {
		t.Fatalf("reload payment from DB: %v", err)
	}
	if dbPaymentRow.Object.State != paystate.STATUS_CANCEL {
		t.Fatalf("expected DB state STATUS_CANCEL, got %s", dbPaymentRow.Object.State)
	}
}
