package exchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nigel2392/go-django/contrib/paynl/client"
)

// ErrEmpty is returned when an exchange call carries no usable data.
var ErrEmpty = errors.New("paynl/exchange: empty exchange call")

// jsonEnvelope is the TGU exchange body.
type jsonEnvelope struct {
	Event   string          `json:"event"`
	Type    string          `json:"type"`
	Version json.Number     `json:"version"`
	ID      string          `json:"id"`
	Object  json.RawMessage `json:"object"`

	// Some (older) JSON exchanges post the legacy fields as JSON.
	Action  string `json:"action"`
	OrderID string `json:"order_id"`
}

// ParseRequest parses an exchange request. body must be the raw request body
// (it may be nil for GET requests). The request body itself is not read.
func ParseRequest(r *http.Request, body []byte) (*Event, error) {
	var ct, _, _ = mime.ParseMediaType(r.Header.Get("Content-Type"))
	var trimmed = bytes.TrimSpace(body)

	if ct == "application/json" || (len(trimmed) > 0 && trimmed[0] == '{') {
		return ParseJSON(body)
	}

	var params = url.Values{}
	for k, v := range r.URL.Query() {
		params[k] = append(params[k], v...)
	}

	if len(trimmed) > 0 {
		form, err := url.ParseQuery(string(trimmed))
		if err != nil {
			return nil, fmt.Errorf("paynl/exchange: parse form: %w", err)
		}
		for k, v := range form {
			params[k] = append(params[k], v...)
		}
	}

	var ev, err = ParseLegacy(params)
	if err != nil {
		return nil, err
	}
	ev.Raw = body
	return ev, nil
}

// ParseJSON parses a JSON exchange body.
func ParseJSON(body []byte) (*Event, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, ErrEmpty
	}

	var env jsonEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("paynl/exchange: decode json: %w", err)
	}

	// Legacy fields posted as JSON.
	if env.Event == "" && env.Object == nil && env.Action != "" {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		var params = url.Values{}
		for k, v := range m {
			params.Set(k, fmt.Sprint(v))
		}
		ev, err := ParseLegacy(params)
		if err != nil {
			return nil, err
		}
		ev.Raw = body
		return ev, nil
	}

	var ev = &Event{
		Format: FormatJSON,
		Name:   strings.ToLower(env.Event),
		Type:   strings.ToLower(env.Type),
		ID:     env.ID,
		Raw:    body,
	}

	if len(env.Object) > 0 && string(env.Object) != "null" {
		var order client.Order
		if err := json.Unmarshal(env.Object, &order); err != nil {
			return nil, fmt.Errorf("paynl/exchange: decode object: %w", err)
		}
		order.Raw = append(json.RawMessage(nil), env.Object...)

		var stats struct {
			Stats client.Stats `json:"stats"`
		}
		_ = json.Unmarshal(env.Object, &stats)

		ev.Order = &order
		ev.OrderID = order.OrderID
		ev.Reference = order.Reference
		ev.Status = order.Status.Normalized()
		ev.Amount = order.Amount.Value
		ev.Extra1 = stats.Stats.Extra1
		ev.Extra2 = stats.Stats.Extra2
		ev.Extra3 = stats.Stats.Extra3
		if ev.ID == "" {
			ev.ID = order.ID
		}
	}

	if ev.ID == "" && ev.OrderID == "" {
		return nil, ErrEmpty
	}

	return ev, nil
}

// ParseLegacy parses legacy exchange parameters.
func ParseLegacy(params url.Values) (*Event, error) {
	var action = strings.ToLower(strings.TrimSpace(params.Get("action")))
	var orderID = strings.TrimSpace(params.Get("order_id"))
	if orderID == "" {
		orderID = strings.TrimSpace(params.Get("orderId"))
	}

	if action == "" && orderID == "" {
		return nil, ErrEmpty
	}

	var ev = &Event{
		Format:  FormatLegacy,
		Name:    action,
		OrderID: orderID,
		Extra1:  params.Get("extra1"),
		Extra2:  params.Get("extra2"),
		Extra3:  params.Get("extra3"),
		Params:  params,
	}

	if a, ok := legacyActionStatus[action]; ok {
		ev.Status = client.Status{Action: a}
	}

	if code, err := strconv.Atoi(params.Get("status_code")); err == nil {
		ev.Status.Code = code
		ev.Status = ev.Status.Normalized()
	}

	if amt := params.Get("amount"); amt != "" {
		ev.Amount = parseLegacyAmount(amt)
	}

	return ev, nil
}

// parseLegacyAmount parses legacy amounts, which are sent either in cents
// ("1250") or as a decimal ("12.50").
func parseLegacyAmount(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	if !strings.Contains(s, ".") {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "00")[:2]
	w, _ := strconv.ParseInt(whole, 10, 64)
	f, _ := strconv.ParseInt(frac, 10, 64)
	if strings.HasPrefix(whole, "-") {
		return w*100 - f
	}
	return w*100 + f
}
