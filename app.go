package paynl

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Nigel2392/go-django/contrib/paynl/client"
	"github.com/Nigel2392/go-django/contrib/paynl/exchange"
	"github.com/Nigel2392/go-django/contrib/paynl/internal/prov"
	"github.com/Nigel2392/go-django/contrib/shop"
	"github.com/Nigel2392/go-django/contrib/shop/models"
	"github.com/Nigel2392/go-django/contrib/shop/util/paystate"
	shopSignals "github.com/Nigel2392/go-django/contrib/shop/util/signals"
	queries "github.com/Nigel2392/go-django/queries/src"
	"github.com/Nigel2392/go-django/queries/src/drivers/errors"
	django "github.com/Nigel2392/go-django/src"
	"github.com/Nigel2392/go-django/src/apps"
	"github.com/Nigel2392/go-django/src/core/logger"
	"github.com/Nigel2392/goldcrest"
	"github.com/Nigel2392/mux"
	"github.com/justinas/nosurf"
)

// Settings keys read from django.Settings on initialization.
const (
	APPVAR_USERNAME         = "PAYNL_USERNAME"
	APPVAR_PASSWORD         = "PAYNL_PASSWORD"
	APPVAR_SERVICE_ID       = "PAYNL_SERVICE_ID"
	APPVAR_TEST_MODE        = "PAYNL_TEST_MODE"
	APPVAR_CONNECT_URL      = "PAYNL_CONNECT_URL"
	APPVAR_REST_URL         = "PAYNL_REST_URL"
	APPVAR_RETURN_URL       = "PAYNL_RETURN_URL"
	APPVAR_EXCHANGE_URL     = "PAYNL_EXCHANGE_URL"
	APPVAR_EXCHANGE_ROUTE   = "PAYNL_EXCHANGE_ROUTE"
	APPVAR_VERIFY_SIGNATURE = "PAYNL_VERIFY_SIGNATURE"
	APPVAR_SECRETS          = "PAYNL_SECRETS"
	APPVAR_SYNC_PAYMENTS    = "PAYNL_SYNC_PAYMENTS"
)

// DefaultExchangeRoute is the default route mounted by the app for exchange calls.
const DefaultExchangeRoute = "/paynl/exchange"

// AppConfig is the go-django application configuration for Pay.nl.
type AppConfig struct {
	*apps.AppConfig
	Provider *Provider
	Router   *exchange.Router
	Handler  *exchange.Handler
	SIGNALS  Signals
}

// PAYNL is the global AppConfig instance.
var PAYNL *AppConfig

// NewAppConfig creates and configures the Pay.nl go-django app.
//
// It registers the Pay.nl provider in `internal/prov.Payments`, mounts the
// exchange webhook endpoint, updates matching `models.Payment` records in the
// database when configured, and dispatches signals to both `PAYNL.SIGNALS`
// and `shop.SHOP.SIGNALS.Payment`.
func NewAppConfig() (django.AppConfig, error) {
	if PAYNL != nil && PAYNL.IsReady() {
		return PAYNL, nil
	}

	var sigs = NewSignals()
	var router = exchange.NewRouter()
	var provider = NewProvider(ProviderOptions{Signals: &sigs})
	prov.Payments.Set(provider.ProviderID(), provider)

	var handler = &exchange.Handler{
		Events: router,
		OnError: func(r *http.Request, ev *exchange.Event, err error) {
			logger.Errorf("paynl exchange error (%s %s): %v", r.Method, r.URL.Path, err)
		},
	}

	var cfg = &AppConfig{
		AppConfig: apps.NewAppConfig("paynl"),
		Provider:  provider,
		Router:    router,
		Handler:   handler,
		SIGNALS:   sigs,
	}

	router.OnAny(exchange.EventHandlerFunc(func(ctx context.Context, ev *exchange.Event) error {
		return cfg.handleExchangeEvent(ctx, ev)
	}))

	cfg.Init = func(settings django.Settings) error {
		var username = django.ConfigGet(settings, APPVAR_USERNAME, "")
		var password = django.ConfigGet(settings, APPVAR_PASSWORD, "")
		var serviceID = django.ConfigGet(settings, APPVAR_SERVICE_ID, "")
		var testMode = django.ConfigGet(settings, APPVAR_TEST_MODE, false)
		var connectURL = django.ConfigGet(settings, APPVAR_CONNECT_URL, "")
		var restURL = django.ConfigGet(settings, APPVAR_REST_URL, "")
		var returnURL = django.ConfigGet(settings, APPVAR_RETURN_URL, "")
		var exchangeURL = django.ConfigGet(settings, APPVAR_EXCHANGE_URL, "")
		var verifySig = django.ConfigGet(settings, APPVAR_VERIFY_SIGNATURE, false)

		cfg.Provider.returnURL = returnURL
		cfg.Provider.exchangeURL = exchangeURL

		if username != "" && password != "" {
			c, err := client.New(client.Config{
				Username:   username,
				Password:   password,
				ServiceID:  serviceID,
				TestMode:   testMode,
				ConnectURL: connectURL,
				RestURL:    restURL,
			})
			if err != nil {
				return err
			}
			cfg.Provider.SetClient(c)
		}

		var secrets = map[string]string{}
		if m, ok := django.ConfigGetOK[map[string]string](settings, APPVAR_SECRETS); ok {
			for k, v := range m {
				secrets[k] = v
			}
		}
		if username != "" && password != "" {
			if _, exists := secrets[username]; !exists {
				secrets[username] = password
			}
			if serviceID != "" {
				if _, exists := secrets[serviceID]; !exists {
					secrets[serviceID] = password
				}
			}
		}

		if len(secrets) > 0 || verifySig {
			cfg.Handler.Verifier = &exchange.Verifier{
				Secrets:  exchange.StaticSecrets(secrets),
				Required: verifySig,
			}
		}

		return nil
	}

	cfg.Routing = func(m mux.Multiplexer) {
		var route = DefaultExchangeRoute
		if django.Global != nil && django.Global.Settings != nil {
			route = django.ConfigGet(django.Global.Settings, APPVAR_EXCHANGE_ROUTE, DefaultExchangeRoute)
		}
		if route != "" {
			// Use m.Handle(mux.ANY, ...) instead of m.Any(...) because mux's
			// nameSpace.Any forwards http.MethodGet instead of mux.ANY.
			m.Handle(mux.ANY, route, cfg.Handler, "paynl_exchange")
			goldcrest.Register(
				django.HOOK_SETUP_NOSURF, 0,
				django.NosurfSetupHook(func(app *django.Application, csrf *nosurf.CSRFHandler) {
					csrf.ExemptPath(route)
				}),
			)
		}
	}

	PAYNL = cfg
	return PAYNL, nil
}

func (a *AppConfig) handleExchangeEvent(ctx context.Context, ev *exchange.Event) error {
	var payment *models.Payment
	var lastPayment *models.Payment

	var syncDB = true
	if django.Global != nil && django.Global.Settings != nil {
		syncDB = django.ConfigGet(django.Global.Settings, APPVAR_SYNC_PAYMENTS, true)
		if _, hasDB := django.Global.Settings.Get(django.APPVAR_DATABASE); !hasDB {
			syncDB = false
		}
	} else {
		syncDB = false
	}

	if syncDB {
		var state = PayStateFromStatus(ev.Status)
		if ev.IsRefund() && state == paystate.STATUS_PENDING {
			state = paystate.STATUS_REFUNDING
		}

		p, last, err := updateShopPayment(ctx, ev, state)
		if err != nil && !errors.Is(err, errors.NoResults) {
			return err
		}
		payment = p
		lastPayment = last
	}

	if err := a.SIGNALS.EmitEvent(ctx, a.Provider.Base(), ev, nil, payment); err != nil {
		return err
	}

	// Also forward to shop.SHOP.SIGNALS.Payment when shop is present.
	if shop.SHOP != nil {
		var sigData = &shopSignals.PaymentSignalData{
			Last:     lastPayment,
			Payment:  payment,
			RawBytes: ev.Raw,
		}
		switch ev.Topic() {
		case exchange.TopicCancelled:
			if err := shop.SHOP.SIGNALS.Payment.Cancelled.Send(ctx, sigData); err != nil {
				return err
			}
		case exchange.TopicRefund:
			if err := shop.SHOP.SIGNALS.Payment.Refunded.Send(ctx, sigData); err != nil {
				return err
			}
		}
	}

	return nil
}

func updateShopPayment(ctx context.Context, ev *exchange.Event, newState paystate.PayState) (current *models.Payment, previous *models.Payment, err error) {
	var ids = make([]string, 0, 2)
	if ev.ID != "" {
		ids = append(ids, ev.ID)
	}
	if ev.OrderID != "" && ev.OrderID != ev.ID {
		ids = append(ids, ev.OrderID)
	}

	var ref = ev.Reference
	if ref == "" {
		ref = ev.Extra1
	}

	// First try querying by Order ID (Reference / Extra1) and loading its Payment.
	if ref != "" {
		if orderRow, oErr := queries.GetQuerySetWithContext(ctx, &models.Order{}).
			Filter("ID", ref).
			First(); oErr == nil && orderRow != nil && orderRow.Object != nil && orderRow.Object.Payment != nil {
			pRow, pErr := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
				Filter("ID", orderRow.Object.Payment.ID).
				First()
			if pErr != nil {
				return nil, nil, pErr
			}
			var prevCopy = *pRow.Object
			previous = &prevCopy
			current = pRow.Object
			current.State = newState
			_, uErr := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
				Filter("ID", current.ID).
				ExplicitSave().
				Update(current)
			return current, previous, uErr
		}
	}

	if len(ids) == 0 {
		return nil, nil, errors.NoResults
	}

	// If TransactionID is defined in FieldDefs, also try filtering by TransactionID.
	if defs := (&models.Payment{}).FieldDefs(ctx); defs != nil {
		if _, hasTxField := defs.Field("TransactionID"); hasTxField {
			for _, id := range ids {
				row, qErr := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
					Filter("TransactionID", id).
					First()
				if qErr != nil {
					if errors.Is(qErr, errors.NoResults) {
						continue
					}
					return nil, nil, qErr
				}
				if row == nil || row.Object == nil {
					continue
				}

				var prevCopy = *row.Object
				previous = &prevCopy
				current = row.Object
				current.State = newState

				_, uErr := queries.GetQuerySetWithContext(ctx, &models.Payment{}).
					Filter("ID", current.ID).
					ExplicitSave().
					Update(current)
				if uErr != nil {
					return current, previous, uErr
				}
				return current, previous, nil
			}
		}
	}

	return nil, nil, errors.NoResults
}

func parseLegacyRaw(raw string, rawBytes []byte) (*exchange.Event, error) {
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	ev, err := exchange.ParseLegacy(vals)
	if err != nil {
		return nil, err
	}
	ev.Raw = rawBytes
	return ev, nil
}
