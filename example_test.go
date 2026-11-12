package paynl_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Nigel2392/go-django/contrib/paynl"
	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/queries/src/drivers"
	"github.com/Nigel2392/mux"
	"github.com/Nigel2392/mux/middleware/sessions"
	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/shopspring/decimal"
)

func ExampleClient() {
	var paynlAPI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{
			"id": "ex-uuid-1",
			"orderId": "51007856048X14b0",
			"status": {"code": 20, "action": "PENDING"},
			"amount": {"value": 1995, "currency": "EUR"},
			"links": {"checkout": "https://checkout.pay.nl/to/payment/ex-uuid-1"}
		}`)
	})

	c, err := client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "your-api-token",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		HTTPClient: &http.Client{Transport: handlerTransport{paynlAPI}},
	})
	if err != nil {
		panic(err)
	}

	ord, err := c.CreateOrder(context.Background(), &client.CreateOrderRequest{
		Reference:   "ORD-1001",
		Description: "Order #1001",
		ReturnURL:   "https://shop.example.com/checkout/return",
		ExchangeURL: "https://shop.example.com/paynl/exchange",
		Amount:      client.EUR(1995),
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(ord.ID, ord.Status.Action, ord.CheckoutURL())
	// Output:
	// ex-uuid-1 PENDING https://checkout.pay.nl/to/payment/ex-uuid-1
}

func ExampleProvider() {
	var paynlAPI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{
			"id": "ex-uuid-2",
			"orderId": "51007856048X2222",
			"status": {"code": 20, "action": "PENDING"},
			"amount": {"value": 2500, "currency": "EUR"},
			"links": {"checkout": "https://checkout.pay.nl/to/payment/ex-uuid-2"}
		}`)
	})

	c, _ := client.New(client.Config{
		Username:   "AT-1234-5678",
		Password:   "your-api-token",
		ServiceID:  "SL-1111-2222",
		TestMode:   true,
		HTTPClient: &http.Client{Transport: handlerTransport{paynlAPI}},
	})

	var provider = paynl.NewProvider(paynl.ProviderOptions{
		Client:      c,
		ReturnURL:   "https://shop.example.com/checkout/return",
		ExchangeURL: "https://shop.example.com/paynl/exchange",
	})

	// Configure session middleware so CheckoutURL is persisted in the user's session.
	var mgr = scs.New()
	mgr.Store = memstore.New()
	var m = mux.New()
	m.Use(sessions.SessionMiddleware(mgr))
	m.Post("/checkout", mux.NewHandler(func(w http.ResponseWriter, r *http.Request) {
		var order = &models.Order{
			ID:      drivers.NewULID(),
			Payment: &models.Payment{Amount: decimal.NewFromFloat(25.00)},
		}
		p, err := provider.CreateTransaction(r.Context(), order)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var url = provider.CheckoutURL(r.Context(), p.TransactionID)
		fmt.Fprintln(w, p.State, url)
	}))

	var w = httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/checkout", nil))
	fmt.Print(w.Body.String())
	// Output:
	// STATUS_PENDING https://checkout.pay.nl/to/payment/ex-uuid-2
}

func ExampleRouter() {
	var router = exchange.NewRouter()
	router.OnFunc(exchange.TopicPaid, func(ctx context.Context, ev *exchange.Event) error {
		fmt.Printf("paid ref=%s amount=%d\n", ev.Extra1, ev.Amount)
		return nil
	})

	var h = &exchange.Handler{Events: router}
	var req = httptest.NewRequest(http.MethodGet, "/paynl/exchange?action=new_ppt&order_id=5100X1&extra1=ORD-42&amount=19.95", nil)
	var w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	fmt.Println(strings.TrimSpace(w.Body.String()))
	// Output:
	// paid ref=ORD-42 amount=1995
	// TRUE|
}
