// Package paynl integrates Pay.nl (PAY.) payments with go-django and contrib/shop.
//
// Register [NewAppConfig] in your go-django application's app list alongside
// shop.NewAppConfig to:
//
//   - Configure a [Provider] implementing checkout transaction creation,
//     cancellations, captures, voids, status synchronization, and refunds.
//   - Automatically mount the Pay.nl exchange webhook endpoint (default
//     /paynl/exchange, configurable via [APPVAR_EXCHANGE_ROUTE]) with nosurf
//     CSRF exemption.
//   - Verify signed TGU exchange webhooks (HMAC-SHA256 / SHA-512) and handle
//     legacy GET / form-POST exchange calls.
//   - Sync [github.com/Nigel2392/go-django/contrib/shop/models.Payment] state
//     changes to the database and emit both Pay.nl ([Signals]) and
//     shop.SHOP.SIGNALS.Payment signals.
//
// Lower-level API access and standalone webhook handling live in the
// [github.com/Nigel2392/go-django/contrib/paynl/client] and
// [github.com/Nigel2392/go-django/contrib/paynl/exchange] subpackages.
package paynl
