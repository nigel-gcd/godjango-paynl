package paynl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nigel2392/cache"
	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
	"github.com/Nigel2392/go-django/contrib/paynl/internal/prov"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/contrib/shop/payments"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
	"github.com/Nigel2392/go-django/src/components"
	"github.com/Nigel2392/go-django/src/core/trans"
	"github.com/Nigel2392/mux/middleware/sessions"
	"github.com/shopspring/decimal"
)

const (
	ProviderID            = "paynl"
	DefaultCheckoutURLTTL = 30 * time.Minute
)

// PaymentProvider matches github.com/Nigel2392/go-django/contrib/shop/payments.PaymentProvider
// (using our local copy of internal/prov until prov is exported or paynl is merged).
type PaymentProvider interface {
	prov.BasePaymentProvider

	CreateTransaction(ctx context.Context, order *models.Order) (*models.Payment, error)
	ParseWebhook(ctx context.Context, rawPayload []byte) (*payments.PaymentSignalPayload, error)
	Cancel(ctx context.Context, payment *models.Payment) error
}

var _ PaymentProvider = (*Provider)(nil)

// ProviderOptions configures the Pay.nl payment provider.
type ProviderOptions struct {
	Client         *client.Client
	ReturnURL      string
	ExchangeURL    string
	PaymentMethod  int
	Currency       string
	CheckoutURLTTL time.Duration
	Signals        *Signals
}

// Provider implements PaymentProvider and additional Pay.nl operations
// (refunds, captures, status checks) for go-django's shop models.
type Provider struct {
	base           prov.BaseProvider
	client         *client.Client
	returnURL      string
	exchangeURL    string
	paymentMethod  int
	currency       string
	checkoutURLTTL time.Duration
	signals        Signals
}

// NewProvider creates a new Pay.nl payment provider.
func NewProvider(opts ProviderOptions) *Provider {
	var sigs = NewSignals()
	if opts.Signals != nil {
		sigs = *opts.Signals
	}
	var currency = opts.Currency
	if currency == "" {
		currency = "EUR"
	}
	var ttl = opts.CheckoutURLTTL
	if ttl <= 0 {
		ttl = DefaultCheckoutURLTTL
	}
	return &Provider{
		base: prov.BaseProvider{
			ID:       ProviderID,
			Label:    trans.S("Pay.nl"),
			Helptext: trans.S("Pay via Pay.nl (iDEAL, Cards, Bancontact, BNPL)"),
		},
		client:         opts.Client,
		returnURL:      opts.ReturnURL,
		exchangeURL:    opts.ExchangeURL,
		paymentMethod:  opts.PaymentMethod,
		currency:       currency,
		checkoutURLTTL: ttl,
		signals:        sigs,
	}
}

func (p *Provider) Base() prov.BaseProvider              { return p.base }
func (p *Provider) ProviderID() string                   { return p.base.ProviderID() }
func (p *Provider) TextLabel(ctx context.Context) string { return p.base.TextLabel(ctx) }
func (p *Provider) TextHelp(ctx context.Context) string  { return p.base.TextHelp(ctx) }
func (p *Provider) WidgetIcon() components.Component     { return p.base.WidgetIcon() }

// Client returns the underlying Pay.nl API client.
func (p *Provider) Client() *client.Client { return p.client }

// SetClient sets the underlying Pay.nl API client.
func (p *Provider) SetClient(c *client.Client) { p.client = c }

func checkoutURLKey(transactionID string) string {
	return "paynl:checkout_url:" + transactionID
}

// CheckoutURL returns the Pay.nl checkout redirect URL for the given transaction ID,
// reading from the request session in ctx when available or from the cache package.
func (p *Provider) CheckoutURL(ctx context.Context, transactionID string) string {
	if transactionID == "" {
		return ""
	}
	var key = checkoutURLKey(transactionID)
	if sess := sessions.SessionFromContext(ctx); sess != nil {
		if u, ok := sess.Get(key).(string); ok && u != "" {
			return u
		}
	}
	if v, err := cache.Get(ctx, key); err == nil {
		if u, ok := v.(string); ok {
			return u
		}
	}
	return ""
}

func (p *Provider) storeCheckoutURL(ctx context.Context, checkoutURL string, ids ...string) {
	if checkoutURL == "" {
		return
	}
	var sess = sessions.SessionFromContext(ctx)
	for _, id := range ids {
		if id == "" {
			continue
		}
		var key = checkoutURLKey(id)
		if sess != nil {
			sess.Set(key, checkoutURL)
		} else {
			_ = cache.Set(ctx, key, checkoutURL, p.checkoutURLTTL)
		}
	}
}

func (p *Provider) deleteCheckoutURL(ctx context.Context, transactionID string) {
	if transactionID == "" {
		return
	}
	var key = checkoutURLKey(transactionID)
	if sess := sessions.SessionFromContext(ctx); sess != nil {
		sess.Delete(key)
	}
	_ = cache.Delete(ctx, key)
}

// CreateTransaction creates a new order on Pay.nl for the given shop *models.Order
// and returns the populated *models.Payment.
func (p *Provider) CreateTransaction(ctx context.Context, order *models.Order) (*models.Payment, error) {
	if p.client == nil {
		return nil, errors.New("paynl: client is not configured")
	}
	if order == nil {
		return nil, errors.New("paynl: order cannot be nil")
	}

	var amount decimal.Decimal
	var items []*models.OrderItem
	if order.OrderItems != nil {
		items = order.OrderItems.AsList()
	}

	if order.Payment != nil && !order.Payment.Amount.IsZero() {
		amount = order.Payment.Amount
	} else {
		for _, item := range items {
			if item != nil {
				amount = amount.Add(item.ItemPrice.Mul(decimal.NewFromInt(int64(item.Amount))))
			}
		}
	}

	var orderID = order.ID.String()
	var req = &client.CreateOrderRequest{
		Reference:   orderID,
		Description: fmt.Sprintf("Order %s", orderID),
		ReturnURL:   p.returnURL,
		ExchangeURL: p.exchangeURL,
		Amount: client.Amount{
			Value:    decimalToCents(amount),
			Currency: p.currency,
		},
		Stats: &client.Stats{
			Extra1: orderID,
		},
	}

	if p.paymentMethod > 0 {
		req.PaymentMethod = &client.PaymentMethod{ID: p.paymentMethod}
	}

	if len(items) > 0 {
		var products = make([]client.Product, 0, len(items))
		for _, item := range items {
			if item == nil {
				continue
			}
			var prodID = item.ID.String()
			if item.Product != nil {
				prodID = item.Product.ID.String()
			}
			products = append(products, client.Product{
				ID:          prodID,
				Description: prodID,
				Type:        client.ProductTypeArticle,
				Price:       client.Amount{Value: decimalToCents(item.ItemPrice), Currency: p.currency},
				Quantity:    item.Amount,
			})
		}
		if len(products) > 0 {
			req.Order = &client.OrderDetails{Products: products}
		}
	}

	res, err := p.client.CreateOrder(ctx, req)
	if err != nil {
		return nil, err
	}

	var txID = res.ID
	if txID == "" {
		txID = res.OrderID
	}

	var payment = order.Payment
	if payment == nil {
		payment = &models.Payment{}
	}
	payment.Amount = centsToDecimal(res.Amount.Value)
	payment.TransactionID = txID
	payment.State = PayStateFromStatus(res.Status)
	order.Payment = payment

	if u := res.CheckoutURL(); u != "" {
		p.storeCheckoutURL(ctx, u, txID, res.OrderID)
	}

	_ = p.signals.Started.Send(ctx, &SignalPayload{
		Provider:      p.base,
		TransactionID: txID,
		OrderID:       orderID,
		State:         payment.State,
		AmountCents:   res.Amount.Value,
		Order:         order,
		Payment:       payment,
		Raw:           res.Raw,
	})

	return payment, nil
}

// ParseWebhook parses a raw exchange payload (JSON or query-encoded legacy payload)
// into a shop/payments.PaymentSignalPayload.
func (p *Provider) ParseWebhook(ctx context.Context, rawPayload []byte) (*payments.PaymentSignalPayload, error) {
	var trimmed = strings.TrimSpace(string(rawPayload))
	var ev *exchange.Event
	var err error

	if strings.HasPrefix(trimmed, "{") {
		ev, err = exchange.ParseJSON(rawPayload)
	} else {
		ev, err = exchange.ParseJSON(rawPayload)
		if err != nil {
			ev, err = parseLegacyRaw(trimmed, rawPayload)
		}
	}
	if err != nil {
		return nil, err
	}

	var orderID = ev.Reference
	if orderID == "" {
		orderID = ev.Extra1
	}

	var state = PayStateFromStatus(ev.Status)
	if ev.IsRefund() && state == paystate.STATUS_PENDING {
		state = paystate.STATUS_REFUNDING
	}

	var amt uint64
	if ev.Amount > 0 {
		amt = uint64(ev.Amount)
	}

	return &payments.PaymentSignalPayload{
		ProviderName:  p.ProviderID(),
		TransactionID: ev.TransactionID(),
		OrderID:       orderID,
		Status:        state,
		Amount:        amt,
		RawPayload:    rawPayload,
	}, nil
}

// Cancel aborts an unpaid payment on Pay.nl and updates payment.State.
func (p *Provider) Cancel(ctx context.Context, payment *models.Payment) error {
	if p.client == nil {
		return errors.New("paynl: client is not configured")
	}
	if payment == nil || payment.TransactionID == "" {
		return errors.New("paynl: payment with TransactionID is required")
	}

	res, err := p.client.AbortOrder(ctx, payment.TransactionID)
	if err != nil {
		return err
	}

	payment.State = PayStateFromStatus(res.Status)
	p.deleteCheckoutURL(ctx, payment.TransactionID)
	return p.signals.Cancelled.Send(ctx, &SignalPayload{
		Provider:      p.base,
		TransactionID: payment.TransactionID,
		State:         payment.State,
		AmountCents:   res.Amount.Value,
		Payment:       payment,
		Raw:           res.Raw,
	})
}

// Refund initiates a full or partial refund for a payment.
// If amount is zero, the full remaining amount of the payment is refunded.
func (p *Provider) Refund(ctx context.Context, payment *models.Payment, amount decimal.Decimal, description string) (*client.RefundResponse, error) {
	if p.client == nil {
		return nil, errors.New("paynl: client is not configured")
	}
	if payment == nil || payment.TransactionID == "" {
		return nil, errors.New("paynl: payment with TransactionID is required")
	}

	var cents = decimalToCents(amount)
	res, err := p.client.Refund(ctx, payment.TransactionID, &client.RefundRequest{
		Amount:      cents,
		Description: description,
		ExchangeURL: p.exchangeURL,
	})
	if err != nil {
		return nil, err
	}

	payment.State = paystate.STATUS_REFUNDING
	_ = p.signals.Refunded.Send(ctx, &SignalPayload{
		Provider:      p.base,
		TransactionID: payment.TransactionID,
		State:         payment.State,
		AmountCents:   cents,
		Payment:       payment,
		Raw:           res.Raw,
	})

	return res, nil
}

// Capture captures (part of) an authorized payment. Zero amount captures the full authorization.
func (p *Provider) Capture(ctx context.Context, payment *models.Payment, amount decimal.Decimal) (*client.Order, error) {
	if p.client == nil {
		return nil, errors.New("paynl: client is not configured")
	}
	if payment == nil || payment.TransactionID == "" {
		return nil, errors.New("paynl: payment with TransactionID is required")
	}

	res, err := p.client.CaptureOrder(ctx, payment.TransactionID, decimalToCents(amount))
	if err != nil {
		return nil, err
	}
	payment.State = PayStateFromStatus(res.Status)
	return res, nil
}

// Void voids an authorized payment.
func (p *Provider) Void(ctx context.Context, payment *models.Payment) (*client.Order, error) {
	if p.client == nil {
		return nil, errors.New("paynl: client is not configured")
	}
	if payment == nil || payment.TransactionID == "" {
		return nil, errors.New("paynl: payment with TransactionID is required")
	}

	res, err := p.client.VoidOrder(ctx, payment.TransactionID)
	if err != nil {
		return nil, err
	}
	payment.State = PayStateFromStatus(res.Status)
	return res, nil
}

// SyncStatus fetches the latest order status from Pay.nl and updates payment.State.
func (p *Provider) SyncStatus(ctx context.Context, payment *models.Payment) (*client.Order, error) {
	if p.client == nil {
		return nil, errors.New("paynl: client is not configured")
	}
	if payment == nil || payment.TransactionID == "" {
		return nil, errors.New("paynl: payment with TransactionID is required")
	}

	res, err := p.client.GetOrder(ctx, payment.TransactionID)
	if err != nil {
		return nil, err
	}
	payment.State = PayStateFromStatus(res.Status)
	return res, nil
}

func decimalToCents(d decimal.Decimal) int64 {
	return d.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
}

func centsToDecimal(cents int64) decimal.Decimal {
	return decimal.New(cents, -2)
}
