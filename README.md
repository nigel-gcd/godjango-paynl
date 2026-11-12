# go-django Pay.nl (`paynl`)

Pay.nl (`PAY.`) payment gateway integration for `go-django` and `contrib/shop`, plus standalone API client (`client`) and exchange webhook (`exchange`) packages.

## Packages

- **`github.com/Nigel2392/go-django/contrib/paynl`** — `go-django` `AppConfig`, `Provider` for `contrib/shop/models`, session/cache checkout URL management, automated exchange route registration (with CSRF exemption), database synchronization, and payment lifecycle signals.
- **`github.com/Nigel2392/go-django/contrib/paynl/client`** — Standalone Pay.nl Order API (`connect.pay.nl/v1/orders`) and REST API (`rest.pay.nl/v2/transactions/{id}/refund`) client with HTTP Basic auth, typed status codes/actions, and structured `APIError` handling.
- **`github.com/Nigel2392/go-django/contrib/paynl/exchange`** — Standalone exchange webhook parser (signed TGU JSON and legacy GET / form-POST), HMAC-SHA256 / HMAC-SHA512 signature verifier, `TRUE|` / `FALSE|` HTTP handler, and topic-based event router.

---

## Using `paynl` in Your Own Shop

### 1. Register `paynl.NewAppConfig` and Configure Settings

Add `paynl.NewAppConfig` alongside `shop.NewAppConfig` (and `go-django` core apps such as `session`, `auth`, `migrator`) when configuring your application:

```go
db, err := drivers.Open(ctx, "sqlite3", "shop.sqlite3")
if err != nil {
    log.Fatal(err)
}

app := django.App(
    django.Configure(map[string]any{
        django.APPVAR_DATABASE:        db,
        migrator.APPVAR_MIGRATION_DIR: "./migrations",

        // Pay.nl credentials and URLs
        paynl.APPVAR_USERNAME:         os.Getenv("PAYNL_USERNAME"), // e.g. AT-xxxx-xxxx
        paynl.APPVAR_PASSWORD:         os.Getenv("PAYNL_PASSWORD"), // API token
        paynl.APPVAR_SERVICE_ID:       os.Getenv("PAYNL_SERVICE_ID"), // SL-xxxx-xxxx
        paynl.APPVAR_TEST_MODE:        true,
        paynl.APPVAR_RETURN_URL:       "https://shop.example.com/checkout/return",
        paynl.APPVAR_EXCHANGE_URL:     "https://shop.example.com/paynl/exchange",
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
```

A complete runnable sample application is available in [examples/shop/main.go](examples/shop/main.go).

#### Configuration Keys (`APPVAR_*`)

| Constant | Key | Default | Description |
| --- | --- | --- | --- |
| `paynl.APPVAR_USERNAME` | `"PAYNL_USERNAME"` | `""` | Pay.nl API token code (`AT-xxxx-xxxx`) or sales location ID (`SL-xxxx-xxxx`). |
| `paynl.APPVAR_PASSWORD` | `"PAYNL_PASSWORD"` | `""` | Pay.nl API token or service secret. |
| `paynl.APPVAR_SERVICE_ID` | `"PAYNL_SERVICE_ID"` | `""` | Sales location / service ID (`SL-xxxx-xxxx`) for order creation. |
| `paynl.APPVAR_TEST_MODE` | `"PAYNL_TEST_MODE"` | `false` | Marks created orders as sandbox/test transactions (`integration.test`). |
| `paynl.APPVAR_CONNECT_URL` | `"PAYNL_CONNECT_URL"` | `https://connect.pay.nl` | Override Order API base URL. |
| `paynl.APPVAR_REST_URL` | `"PAYNL_REST_URL"` | `https://rest.pay.nl` | Override REST API base URL (used for refunds). |
| `paynl.APPVAR_RETURN_URL` | `"PAYNL_RETURN_URL"` | `""` | URL the shopper is redirected back to after payment. |
| `paynl.APPVAR_EXCHANGE_URL` | `"PAYNL_EXCHANGE_URL"` | `""` | Public webhook URL Pay.nl calls on status/refund updates. |
| `paynl.APPVAR_EXCHANGE_ROUTE` | `"PAYNL_EXCHANGE_ROUTE"` | `"/paynl/exchange"` | Route mounted on `app.Mux` (`""` disables auto-mounting). Automatically exempted from `nosurf` CSRF checks. |
| `paynl.APPVAR_VERIFY_SIGNATURE` | `"PAYNL_VERIFY_SIGNATURE"` | `false` | Require HMAC signature verification on exchange calls (rejects unsigned legacy calls when `true`). |
| `paynl.APPVAR_SECRETS` | `"PAYNL_SECRETS"` | `map[string]string{}` | Additional `Signature-KeyId -> secret` map. `APPVAR_USERNAME` and `APPVAR_SERVICE_ID` mapped to `APPVAR_PASSWORD` are added automatically. |
| `paynl.APPVAR_SYNC_PAYMENTS` | `"PAYNL_SYNC_PAYMENTS"` | `true` | Automatically update matching `models.Payment` records in the DB on exchange events when `APPVAR_DATABASE` is configured. |

---

### 2. Checkout Flow: Create Transaction & Redirect

When checking out a `*models.Order`, call `paynl.PAYNL.Provider.CreateTransaction(r.Context(), order)`, persist the updated `*models.Payment`, and redirect the customer to `paynl.PAYNL.Provider.CheckoutURL(...)`:

```go
func handleCheckout(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Create / load your shop order & payment records first ...
    paymentObj, err := paynl.PAYNL.Provider.CreateTransaction(ctx, orderObj)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }

    // Persist updated Payment (Amount, State = STATUS_PENDING)
    _, err = queries.GetQuerySetWithContext(ctx, &models.Payment{}).
        Filter("ID", paymentObj.ID).
        ExplicitSave().
        Update(paymentObj)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    checkoutURL := paynl.PAYNL.Provider.CheckoutURL(ctx, paymentObj.TransactionID)
    http.Redirect(w, r, checkoutURL, http.StatusSeeOther)
}
```

#### Amount & Line-Item Resolution
- If `order.Payment != nil && !order.Payment.Amount.IsZero()`, `CreateTransaction` uses `order.Payment.Amount`.
- Otherwise, it sums `item.ItemPrice * item.Amount` across `order.OrderItems.AsList()` and attaches `order.products` line items (`client.ProductTypeArticle`) to the Pay.nl order request (helpful for BNPL methods).

#### Checkout URL Storage (Session + Cache)
- `CreateTransaction` stores the returned `CheckoutURL()` under key `paynl:checkout_url:<transactionID>` (and under `res.OrderID` if different).
- When `sessions.SessionFromContext(ctx)` is present (for example when `mux/middleware/sessions` is active), the URL is stored in the **user's HTTP session**.
- Otherwise (e.g. background jobs or requests without session middleware), it falls back to `github.com/Nigel2392/cache` with TTL `ProviderOptions.CheckoutURLTTL` (defaults to `paynl.DefaultCheckoutURLTTL` = 30 minutes).
- `CheckoutURL(ctx, transactionID)` always checks the session first, then falls back to cache.

> **Note:** `contrib/shop`'s `models.Payment` does not map `TransactionID` as a database column yet. In your checkout code, store `paymentObj.TransactionID` in your own table or rely on `orderObj.ID` (sent as Pay.nl `reference` and `stats.extra1`), which `app.go` automatically uses to look up and update `models.Payment` on exchange webhooks.

---

### 3. Exchange Webhooks (Status & Refund Synchronization)

`paynl.NewAppConfig` mounts `paynl.PAYNL.Handler` at `APPVAR_EXCHANGE_ROUTE` (default `/paynl/exchange`) accepting both `GET` (legacy query calls) and `POST` (signed TGU JSON or legacy form posts), and exempts the path from `nosurf` CSRF checks.

When Pay.nl calls your exchange endpoint:
1. If `Verifier` is configured, HMAC signatures (`Signature`, `Signature-Method`, `Signature-Algorithm`, `Signature-KeyId`) are verified over the raw request body.
2. The payload is normalized into an `*exchange.Event`.
3. If `APPVAR_SYNC_PAYMENTS` is enabled and `APPVAR_DATABASE` is configured, `app.go` finds the order/payment by `Reference` / `Extra1` (or `TransactionID` if mapped), maps the Pay.nl status via `paynl.PayStateFromStatus`, and saves `payment.State` to the DB.
4. `paynl.PAYNL.SIGNALS` is emitted (`Updated` plus `Paid`, `Authorized`, `Verify`, `Cancelled`, `Refunded`, or `Chargeback`).
5. When `shop.SHOP != nil`, `shop.SHOP.SIGNALS.Payment.Cancelled` and `shop.SHOP.SIGNALS.Payment.Refunded` are also emitted.
6. Pay.nl receives `TRUE|...` (HTTP 200) on success, or `FALSE|...` on error so Pay.nl retries later.

---

### 4. Listening to Signals

Register listeners during app initialization (before `app.Serve()`):

```go
paynl.PAYNL.SIGNALS.Paid.Listen(ctx, func(ctx context.Context, _ signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
    log.Printf("Order %s paid (tx=%s, amount=%d cents)", p.OrderID, p.TransactionID, p.AmountCents)
    return nil
})

paynl.PAYNL.SIGNALS.Verify.Listen(ctx, func(ctx context.Context, _ signals.Signal[*paynl.SignalPayload], p *paynl.SignalPayload) error {
    log.Printf("Order %s flagged for manual verification (tx=%s)", p.OrderID, p.TransactionID)
    return nil
})

shop.SHOP.SIGNALS.Payment.Refunded.Listen(ctx, func(ctx context.Context, _ signals.Signal[*shopSignals.PaymentSignalData], p *shopSignals.PaymentSignalData) error {
    log.Printf("Shop payment %s refunded", p.Payment.ID)
    return nil
})
```

Available `paynl.PAYNL.SIGNALS` channels:
`Started`, `Updated`, `Paid`, `Authorized`, `Verify`, `Cancelled`, `Refunded`, `Chargeback`.

---

### 5. Refunds, Captures, Voids, Cancellations & Sync

Use `paynl.PAYNL.Provider` methods with a `*models.Payment` whose `TransactionID` is populated:

```go
// Partial refund of EUR 12.50 (pass decimal.Zero to refund the full remaining amount)
refundRes, err := paynl.PAYNL.Provider.Refund(ctx, payment, decimal.NewFromFloat(12.50), "Returned item")

// Capture an authorized payment (pass decimal.Zero to capture full remaining amount)
orderRes, err := paynl.PAYNL.Provider.Capture(ctx, payment, decimal.NewFromFloat(25.00))

// Void an authorized payment
orderRes, err := paynl.PAYNL.Provider.Void(ctx, payment)

// Abort an unpaid transaction
err := paynl.PAYNL.Provider.Cancel(ctx, payment)

// Fetch latest status from Pay.nl and update payment.State in memory
orderRes, err := paynl.PAYNL.Provider.SyncStatus(ctx, payment)
```

---

### 6. Status Mapping (`PayStateFromStatus`)

| Pay.nl Action | Example Code(s) | `paystate.PayState` |
| --- | --- | --- |
| `INIT` | — | `STATUS_INIT` |
| `PENDING` (or unknown fallback) | `20`, `25`, `50`, `90`, `98` | `STATUS_PENDING` |
| `CANCEL` | `-90` | `STATUS_CANCEL` |
| `EXPIRED` | `-80` | `STATUS_EXPIRED` |
| `DENIED` | `-63`, `-64` | `STATUS_DENIED` |
| `FAILURE` | `-60` | `STATUS_FAILURE` |
| `PAID_CHECKAMOUNT` | `-51` | `STATUS_PAID_CHECKAMOUNT` |
| `PARTIAL_PAYMENT` | `80` | `STATUS_PARTIAL_PAYMENT` |
| `VERIFY` | `85` | `STATUS_VERIFY` |
| `AUTHORIZE` | `95` | `STATUS_AUTHORIZE` |
| `PARTLY_CAPTURED` | `97` | `STATUS_PARTLY_CAPTURED` |
| `PAID` | `100` | `STATUS_PAID` |
| `CHARGEBACK` | `-71` | `STATUS_CHARGEBACK` |
| `REFUNDING` | — | `STATUS_REFUNDING` |
| `REFUND` | `-81` | `STATUS_REFUND` |
| `PARTIAL_REFUND` | `-82` | `STATUS_PARTIAL_REFUND` |

---

## Standalone Usage (`client` and `exchange`)

Both subpackages work independently of `go-django`:

```go
// 1. Direct API client
c, err := client.New(client.Config{
    Username:  "AT-1234-5678",
    Password:  "your-api-token",
    ServiceID: "SL-1111-2222",
    TestMode:  true,
})
order, err := c.CreateOrder(ctx, &client.CreateOrderRequest{
    Reference:   "ORD-1001",
    Description: "Order #1001",
    ReturnURL:   "https://shop.example.com/return",
    ExchangeURL: "https://shop.example.com/exchange",
    Amount:      client.EUR(1995),
})

// 2. Standalone webhook handler
router := exchange.NewRouter()
router.OnFunc(exchange.TopicPaid, func(ctx context.Context, ev *exchange.Event) error {
    // mark order ev.Reference / ev.Extra1 as paid
    return nil
})

mux.Handle("/exchange", &exchange.Handler{
    Verifier: &exchange.Verifier{
        Secrets:  exchange.StaticSecrets(map[string]string{"SL-1111-2222": "your-api-token"}),
        Required: true,
    },
    Events: router,
})
```

---

## Testing

Run unit, example, and SQLite end-to-end tests:

```bash
go test -count=1 -v ./...
```

Run the live Pay.nl sandbox integration test (`TestLiveSandboxPaymentWorkflow` in [sandbox_test.go](sandbox_test.go), which creates a test-mode order on Pay.nl, syncs its status, and aborts it):

```bash
PAYNL_USERNAME=AT-xxxx-xxxx PAYNL_PASSWORD=your-token PAYNL_SERVICE_ID=SL-xxxx-xxxx \
  go test -count=1 -v -run TestLiveSandboxPaymentWorkflow .
```
