// Example go-django shop application configured with the Pay.nl payment provider.
//
// Run with your Pay.nl sandbox credentials:
//
//	PAYNL_USERNAME=AT-xxxx-xxxx PAYNL_PASSWORD=your-token PAYNL_SERVICE_ID=SL-xxxx-xxxx go run ./examples/shop
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/Nigel2392/go-django/contrib/admin"
	"github.com/Nigel2392/go-django/contrib/auth"
	"github.com/Nigel2392/go-django/contrib/blocks"
	"github.com/Nigel2392/go-django/contrib/editor"
	"github.com/Nigel2392/go-django/contrib/messages"
	"github.com/Nigel2392/go-django/contrib/paynl"
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
	"github.com/Nigel2392/go-signals"
	"github.com/Nigel2392/mux"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shopspring/decimal"
)

func main() {
	var ctx = context.Background()
	db, err := drivers.Open(ctx, "sqlite3", "shop.sqlite3")
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	var app = django.App(
		django.Configure(map[string]any{
			django.APPVAR_ALLOWED_HOSTS:   []string{"*"},
			django.APPVAR_DEBUG:           true,
			django.APPVAR_HOST:            "127.0.0.1",
			django.APPVAR_PORT:            "8080",
			django.APPVAR_DATABASE:        db,
			migrator.APPVAR_MIGRATION_DIR: "./migrations",

			// Pay.nl configuration.
			paynl.APPVAR_USERNAME:         os.Getenv("PAYNL_USERNAME"),
			paynl.APPVAR_PASSWORD:         os.Getenv("PAYNL_PASSWORD"),
			paynl.APPVAR_SERVICE_ID:       os.Getenv("PAYNL_SERVICE_ID"),
			paynl.APPVAR_TEST_MODE:        true,
			paynl.APPVAR_RETURN_URL:       "http://127.0.0.1:8080/checkout/return",
			paynl.APPVAR_EXCHANGE_URL:     "https://your-public-host.example.com/paynl/exchange",
			paynl.APPVAR_VERIFY_SIGNATURE: true,
			paynl.APPVAR_SYNC_PAYMENTS:    true,
		}),
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
			shop.NewAppConfig,
			paynl.NewAppConfig,
		),
	)

	// Listen to Pay.nl events (triggered both on API actions and exchange webhooks).
	_, _ = paynl.PAYNL.SIGNALS.Paid.Listen(ctx, func(_ context.Context, _ signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		log.Printf("order %s paid via Pay.nl tx=%s (%d cents)", p.OrderID, p.TransactionID, p.AmountCents)
		return nil
	})
	_, _ = paynl.PAYNL.SIGNALS.Cancelled.Listen(ctx, func(_ context.Context, _ signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
		log.Printf("order %s cancelled tx=%s", p.OrderID, p.TransactionID)
		return nil
	})

	// shop.SHOP.SIGNALS.Payment.* is also emitted automatically on cancel/refund exchanges.
	_, _ = shop.SHOP.SIGNALS.Payment.Refunded.Listen(ctx, func(_ context.Context, _ signals.Signal[*shopSignals.PaymentSignalData], p *shopSignals.PaymentSignalData) error {
		if p.Payment != nil {
			log.Printf("shop payment %s marked as refunded", p.Payment.ID)
		}
		return nil
	})

	if err := app.Initialize(); err != nil {
		log.Fatalf("initialize app: %v", err)
	}

	// Mount shop checkout / return / refund routes onto go-django's Mux.
	app.Mux.Post("/checkout", mux.NewHandler(handleCheckout))
	app.Mux.Get("/checkout/return", mux.NewHandler(handleReturn))
	app.Mux.Post("/orders/{order_id}/refund", mux.NewHandler(handleRefund))

	// paynl.NewAppConfig automatically mounts POST/GET /paynl/exchange on app.Mux.
	log.Fatal(app.Serve())
}

func handleCheckout(w http.ResponseWriter, r *http.Request) {
	var ctx = r.Context()

	stateObj, err := queries.GetQuerySetWithContext(ctx, &models.OrderState{}).Create(&models.OrderState{
		Label:      "Pending",
		Action:     "checkout",
		WorkflowID: drivers.NewULID(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	paymentObj, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).Create(&models.Payment{
		Amount:    decimal.NewFromFloat(29.95),
		State:     paystate.STATUS_INIT,
		CreatedAt: drivers.CurrentTimestamp(),
		UpdatedAt: drivers.CurrentTimestamp(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	orderObj, err := queries.GetQuerySetWithContext(ctx, &models.Order{}).Create(&models.Order{
		State:   stateObj,
		Payment: paymentObj,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	orderObj.Payment = paymentObj

	// Create the transaction at Pay.nl and redirect the shopper to CheckoutURL.
	// Note: CheckoutURL is persisted in the user's HTTP session when session
	// middleware is active, falling back to cache otherwise.
	paymentObj, err = paynl.PAYNL.Provider.CreateTransaction(ctx, orderObj)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if _, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
		Filter("ID", paymentObj.ID).
		ExplicitSave().
		Update(paymentObj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var checkoutURL = paynl.PAYNL.Provider.CheckoutURL(ctx, paymentObj.TransactionID)
	if checkoutURL == "" {
		http.Error(w, "missing Pay.nl checkout URL", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, checkoutURL, http.StatusSeeOther)
}

func handleReturn(w http.ResponseWriter, r *http.Request) {
	// Pay.nl appends orderId / orderStatusId query parameters to the return URL.
	// Always rely on the verified exchange webhook (or SyncStatus) for authoritative state.
	var txID = r.URL.Query().Get("orderId")
	if txID == "" {
		txID = r.URL.Query().Get("id")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"transaction_id": txID,
		"message":        "Thank you! Payment status is confirmed asynchronously via /paynl/exchange.",
	})
}

func handleRefund(w http.ResponseWriter, r *http.Request) {
	var ctx = r.Context()
	var vars = mux.Vars(r)
	orderRow, err := queries.GetQuerySetWithContext(ctx, &models.Order{}).
		Filter("ID", vars.Get("order_id")).
		First()
	if err != nil {
		http.Error(w, fmt.Sprintf("order not found: %v", err), http.StatusNotFound)
		return
	}

	pRow, err := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
		Filter("ID", orderRow.Object.Payment.ID).
		First()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Pass a decimal.Zero amount to refund the full remaining amount.
	res, err := paynl.PAYNL.Provider.Refund(ctx, pRow.Object, decimal.NewFromFloat(10.00), "Customer refund")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
